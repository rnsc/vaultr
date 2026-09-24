package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/vault"
)

// fakeVault serves userpass/ldap login, token lookup and the OIDC flow.
type fakeVault struct {
	t      *testing.T
	srv    *httptest.Server
	nonce  string
	sawNS  string
	sawTok bool
	idpErr bool
}

func newFake(t *testing.T) (*fakeVault, *vault.Client) {
	f := &fakeVault{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	c, err := vault.New(vault.Config{Addr: f.srv.URL, Token: "hvs.old"})
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func (f *fakeVault) serve(w http.ResponseWriter, r *http.Request) {
	f.sawNS = r.Header.Get("X-Vault-Namespace")
	f.sawTok = r.Header.Get("X-Vault-Token") != ""
	authResp := func(tok string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{
			"client_token": tok, "policies": []string{"default"}, "lease_duration": 3600,
		}})
	}
	switch {
	case r.URL.Path == "/v1/auth/userpass/login/jdoe" || r.URL.Path == "/v1/auth/corp/login/jdoe":
		var body struct{ Password string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Password != "right" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"errors":["invalid username or password"]}`))
			return
		}
		authResp("hvs.userpass")
	case r.URL.Path == "/v1/auth/token/lookup-self":
		if r.Header.Get("X-Vault-Token") != "hvs.pasted" {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ttl": 600}})
	case r.URL.Path == "/v1/auth/oidc/oidc/auth_url":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.nonce = body["client_nonce"]
		if body["role"] == "no-redirect" {
			_, _ = w.Write([]byte(`{"data":{"auth_url":""}}`))
			return
		}
		u := f.srv.URL + "/idp?" + url.Values{"redirect_uri": {body["redirect_uri"]}, "state": {"st8"}}.Encode()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"auth_url": u}})
	case r.URL.Path == "/idp":
		q := url.Values{"state": {r.URL.Query().Get("state")}, "code": {"c0de"}}
		if f.idpErr {
			q = url.Values{"error": {"access_denied"}, "error_description": {"user said no"}}
		}
		http.Redirect(w, r, r.URL.Query().Get("redirect_uri")+"?"+q.Encode(), http.StatusFound)
	case r.URL.Path == "/v1/auth/oidc/oidc/callback":
		q := r.URL.Query()
		if q.Get("state") != "st8" || q.Get("code") != "c0de" || q.Get("client_nonce") != f.nonce || f.nonce == "" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"errors":["bad callback"]}`))
			return
		}
		authResp("hvs.oidc")
	default:
		w.WriteHeader(404)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// visit plays the browser: follow the auth URL through the redirect to
// the local callback.
func visit(u string) {
	go func() {
		resp, err := http.Get(u)
		if err == nil {
			resp.Body.Close()
		}
	}()
}

func TestUserpassAndLDAP(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	res, err := Login(ctx, c, Request{Method: Userpass, Username: "jdoe", Password: "right", Namespace: "/team-a/"})
	if err != nil || res.Token != "hvs.userpass" || res.TTL != time.Hour || res.Namespace != "team-a" {
		t.Fatalf("userpass: %+v %v", res, err)
	}
	if f.sawNS != "team-a" || f.sawTok {
		t.Errorf("login sent namespace %q, token %v", f.sawNS, f.sawTok)
	}
	if c.Token() != "hvs.old" {
		t.Error("Login changed the client's token")
	}
	// Custom mount (ldap on /corp).
	if res, err := Login(ctx, c, Request{Method: LDAP, Mount: "/corp/", Username: "jdoe", Password: "right"}); err != nil || res.Token != "hvs.userpass" {
		t.Errorf("ldap on custom mount: %+v %v", res, err)
	}
	if _, err := Login(ctx, c, Request{Method: Userpass, Username: "jdoe", Password: "wrong"}); err == nil || !strings.Contains(err.Error(), "login failed") {
		t.Errorf("wrong password: %v", err)
	}
	for _, r := range []Request{{Method: Userpass, Password: "x"}, {Method: LDAP, Username: "jdoe"}} {
		if _, err := Login(ctx, c, r); err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("%+v: %v", r, err)
		}
	}
}

func TestTokenMethod(t *testing.T) {
	_, c := newFake(t)
	res, err := Login(context.Background(), c, Request{Method: Token, Token: "  hvs.pasted\n"})
	if err != nil || res.Token != "hvs.pasted" || res.TTL < 9*time.Minute {
		t.Fatalf("token: %+v %v", res, err)
	}
	if _, err := Login(context.Background(), c, Request{Method: Token, Token: "hvs.bad"}); !errors.Is(err, vault.ErrTokenInvalid) {
		t.Errorf("bad token: %v", err)
	}
	if _, err := Login(context.Background(), c, Request{Method: Token}); err == nil {
		t.Error("empty token accepted")
	}
}

func TestOIDC(t *testing.T) {
	f, c := newFake(t)
	port := freePort(t)
	var opened string
	res, err := Login(context.Background(), c, Request{
		Method: OIDC, CallbackPort: port,
		OpenURL: func(u string) { opened = u; visit(u) },
	})
	if err != nil || res.Token != "hvs.oidc" {
		t.Fatalf("oidc: %+v %v", res, err)
	}
	if !strings.Contains(opened, url.QueryEscape("http://localhost:"+itoa(port)+"/oidc/callback")) {
		t.Errorf("redirect URI not passed: %s", opened)
	}
	// The callback server is gone afterwards.
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+itoa(port), 200*time.Millisecond); err == nil {
		t.Error("callback listener still open")
	}

	f.idpErr = true
	_, err = Login(context.Background(), c, Request{Method: OIDC, CallbackPort: port, OpenURL: visit})
	if err == nil || !strings.Contains(err.Error(), "user said no") {
		t.Errorf("idp error: %v", err)
	}
}

func TestOIDCErrors(t *testing.T) {
	_, c := newFake(t)
	port := freePort(t)
	if _, err := Login(context.Background(), c, Request{Method: OIDC, Role: "no-redirect", CallbackPort: port, OpenURL: visit}); err == nil || !strings.Contains(err.Error(), "allowed_redirect_uris") {
		t.Errorf("empty auth_url: %v", err)
	}
	if _, err := Login(context.Background(), c, Request{Method: OIDC, CallbackPort: port}); err == nil {
		t.Error("oidc without OpenURL accepted")
	}
	// Cancelled while waiting for the browser.
	ctx, cancel := context.WithCancel(context.Background())
	_, err := Login(ctx, c, Request{Method: OIDC, CallbackPort: port, OpenURL: func(string) { cancel() }})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %v", err)
	}
	// Port in use.
	l, _ := net.Listen("tcp", "127.0.0.1:"+itoa(port))
	defer l.Close()
	if _, err := Login(context.Background(), c, Request{Method: OIDC, CallbackPort: port, OpenURL: visit}); err == nil || !strings.Contains(err.Error(), "cannot listen") {
		t.Errorf("port in use: %v", err)
	}
}

func TestParseMethod(t *testing.T) {
	for _, s := range []string{"oidc", "LDAP", " userpass ", "token"} {
		if _, err := ParseMethod(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	if _, err := ParseMethod("kerberos"); err == nil {
		t.Error("unknown method accepted")
	}
}

func TestSaveToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("VAULT_TOKEN", "")
	warn, err := SaveToken("hvs.saved")
	if err != nil || warn != "" {
		t.Fatalf("%q %v", warn, err)
	}
	p := filepath.Join(home, ".vault-token")
	b, _ := os.ReadFile(p)
	st, _ := os.Stat(p)
	if string(b) != "hvs.saved" || st.Mode().Perm() != 0o600 {
		t.Errorf("content %q mode %v", b, st.Mode().Perm())
	}
	t.Setenv("VAULT_TOKEN", "hvs.env")
	if warn, _ := SaveToken("hvs.2"); !strings.Contains(warn, "VAULT_TOKEN") {
		t.Errorf("no VAULT_TOKEN warning: %q", warn)
	}
}

func TestOpenBrowserUsesBROWSER(t *testing.T) {
	out := filepath.Join(t.TempDir(), "url")
	script := filepath.Join(t.TempDir(), "browser.sh")
	_ = os.WriteFile(script, []byte("#!/bin/sh\necho \"$1\" > "+out+"\n"), 0o755)
	t.Setenv("BROWSER", script)
	if err := OpenBrowser("https://example.com/x"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) == "https://example.com/x" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("BROWSER command not run with the URL")
}

func itoa(n int) string { return strconv.Itoa(n) }
