// Package auth logs in to Vault and stores the resulting token.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rnsc/vaultr/internal/vault"
)

// Method is a login method.
type Method string

// Supported methods.
const (
	OIDC     Method = "oidc"
	LDAP     Method = "ldap"
	Userpass Method = "userpass"
	Token    Method = "token"
)

// Methods lists the supported methods, in display order.
var Methods = []Method{OIDC, LDAP, Userpass, Token}

// ParseMethod validates a method name.
func ParseMethod(s string) (Method, error) {
	for _, m := range Methods {
		if string(m) == strings.ToLower(strings.TrimSpace(s)) {
			return m, nil
		}
	}
	return "", fmt.Errorf("unknown login method %q (want oidc, ldap, userpass or token)", s)
}

// DefaultCallbackPort matches the vault CLI's OIDC callback.
const DefaultCallbackPort = 8250

// Request describes one login.
type Request struct {
	Method    Method
	Mount     string // auth mount path; defaults to the method name
	Namespace string // namespace to log in to ("" = root)

	Username string // ldap, userpass
	Password string // ldap, userpass
	Token    string // token
	Role     string // oidc; empty uses the mount's default role

	CallbackPort int // oidc; 0 = DefaultCallbackPort
	// OpenURL is called with the OIDC authorization URL (open a browser,
	// display it). Required for OIDC.
	OpenURL func(url string)
}

// Result of a successful login.
type Result struct {
	Token     string
	Namespace string // namespace the token belongs to
	Policies  []string
	TTL       time.Duration
}

// Login performs the login. c provides the server address and TLS
// settings; its own token is not used or changed.
func Login(ctx context.Context, c *vault.Client, r Request) (Result, error) {
	r.Namespace = strings.Trim(r.Namespace, "/")
	if r.Mount == "" {
		r.Mount = string(r.Method)
	}
	r.Mount = strings.Trim(r.Mount, "/")
	switch r.Method {
	case Token:
		return loginToken(ctx, c, r)
	case LDAP, Userpass:
		if r.Username == "" {
			return Result{}, errors.New("username is required")
		}
		if r.Password == "" {
			return Result{}, errors.New("password is required")
		}
		resp, err := c.Unauthenticated(ctx, r.Namespace, http.MethodPost,
			"auth/"+r.Mount+"/login/"+r.Username, nil, map[string]string{"password": r.Password})
		if err != nil {
			return Result{}, loginError(err)
		}
		return result(resp, r.Namespace)
	case OIDC:
		return loginOIDC(ctx, c, r)
	}
	return Result{}, fmt.Errorf("unknown login method %q", r.Method)
}

func loginError(err error) error {
	if errors.Is(err, vault.ErrForbidden) || strings.Contains(err.Error(), "HTTP 400") {
		return fmt.Errorf("login failed: %w", err)
	}
	return err
}

func result(resp *vault.Response, ns string) (Result, error) {
	var a struct {
		ClientToken   string   `json:"client_token"`
		Policies      []string `json:"policies"`
		LeaseDuration int64    `json:"lease_duration"`
	}
	if len(resp.Auth) == 0 || json.Unmarshal(resp.Auth, &a) != nil || a.ClientToken == "" {
		return Result{}, errors.New("login response contained no token")
	}
	return Result{Token: a.ClientToken, Namespace: ns, Policies: a.Policies, TTL: time.Duration(a.LeaseDuration) * time.Second}, nil
}

func loginToken(ctx context.Context, c *vault.Client, r Request) (Result, error) {
	tok := strings.TrimSpace(r.Token)
	if tok == "" {
		return Result{}, errors.New("token is required")
	}
	ns := r.Namespace
	probe := c.WithToken(tok, ns)
	ti, err := probe.LookupSelf(ctx)
	if err != nil {
		return Result{}, err
	}
	res := Result{Token: tok, Namespace: ns}
	if !ti.ExpireTime.IsZero() {
		res.TTL = time.Until(ti.ExpireTime).Round(time.Second)
	}
	return res, nil
}

func loginOIDC(ctx context.Context, c *vault.Client, r Request) (Result, error) {
	if r.OpenURL == nil {
		return Result{}, errors.New("oidc login needs a way to open the browser")
	}
	port := r.CallbackPort
	if port == 0 {
		port = DefaultCallbackPort
	}
	redirect := fmt.Sprintf("http://localhost:%d/oidc/callback", port)

	type cb struct {
		q   url.Values
		err error
	}
	got := make(chan cb, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/oidc/callback", func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm() // form_post responses arrive as a POST
		q := req.Form
		if e := q.Get("error"); e != "" {
			msg := e + ": " + q.Get("error_description")
			writePage(w, "Login failed", msg)
			select {
			case got <- cb{err: errors.New("identity provider: " + msg)}:
			default:
			}
			return
		}
		writePage(w, "Signed in to Vault", "You can close this window and return to vaultr.")
		select {
		case got <- cb{q: q}:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	var listeners []net.Listener
	for _, host := range []string{"127.0.0.1", "::1"} {
		l, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
		if err != nil {
			if host == "127.0.0.1" {
				return Result{}, fmt.Errorf("oidc callback: cannot listen on port %d: %w", port, err)
			}
			continue // no IPv6 loopback
		}
		listeners = append(listeners, l)
		go func() { _ = srv.Serve(l) }()
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		for _, l := range listeners {
			_ = l.Close()
		}
	}()

	nonce := randHex(16)
	resp, err := c.Unauthenticated(ctx, r.Namespace, http.MethodPost, "auth/"+r.Mount+"/oidc/auth_url", nil, map[string]string{
		"role":         r.Role,
		"redirect_uri": redirect,
		"client_nonce": nonce,
	})
	if err != nil {
		return Result{}, loginError(err)
	}
	var d struct {
		AuthURL string `json:"auth_url"`
	}
	if err := json.Unmarshal(resp.Data, &d); err != nil || d.AuthURL == "" {
		return Result{}, fmt.Errorf("vault returned no authorization URL: check the role and that %s is in its allowed_redirect_uris", redirect)
	}
	r.OpenURL(d.AuthURL)

	var q url.Values
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case res := <-got:
		if res.err != nil {
			return Result{}, res.err
		}
		q = res.q
	}
	params := url.Values{"state": {q.Get("state")}, "code": {q.Get("code")}, "client_nonce": {nonce}}
	if idt := q.Get("id_token"); idt != "" {
		params.Set("id_token", idt)
	}
	resp, err = c.Unauthenticated(ctx, r.Namespace, http.MethodGet, "auth/"+r.Mount+"/oidc/callback", params, nil)
	if err != nil {
		return Result{}, loginError(err)
	}
	return result(resp, r.Namespace)
}

func writePage(w http.ResponseWriter, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>vaultr</title>
<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem">
<h2>%s</h2><p>%s</p></body>`, html.EscapeString(title), html.EscapeString(body))
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// OpenBrowser opens url in the user's browser. $BROWSER (a command, with
// optional arguments) takes precedence over the OS default.
func OpenBrowser(u string) error {
	var cmd *exec.Cmd
	if b := strings.Fields(os.Getenv("BROWSER")); len(b) > 0 {
		cmd = exec.Command(b[0], append(b[1:], u)...)
	} else {
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", u)
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
		default:
			cmd = exec.Command("xdg-open", u)
		}
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// TokenFile is where `vault login` keeps the token: ~/.vault-token.
func TokenFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".vault-token"), nil
}

// SaveToken writes the token to ~/.vault-token (mode 0600), like
// `vault login`. It returns a warning when VAULT_TOKEN is set, since that
// takes precedence over the file in later runs.
func SaveToken(token string) (string, error) {
	p, err := TokenFile()
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".vault-token-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.WriteString(token); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return "", err
	}
	if os.Getenv("VAULT_TOKEN") != "" {
		return "VAULT_TOKEN is set in your environment and overrides " + p + " in new shells; unset it to use this login", nil
	}
	return "", nil
}
