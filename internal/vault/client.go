// Package vault is a minimal Vault HTTP client covering only what vaultr
// needs: listing, reading, cubbyhole storage and token introspection.
package vault

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned for 404 responses.
var ErrNotFound = errors.New("not found")

// ErrForbidden is returned for 403 responses.
var ErrForbidden = errors.New("permission denied")

// ErrTokenInvalid means the token is missing, expired or revoked: logging
// in again is the fix. Errors wrapping it also wrap ErrForbidden.
var ErrTokenInvalid = errors.New("token is missing, expired or invalid")

// Client talks to a single Vault server with a single token.
//
// Namespace is where secrets are read. The token itself may live in a
// different (usually parent) namespace, e.g. after logging in at the root
// namespace and working in a team namespace. Token-scoped calls (lookup,
// cubbyhole) go to the token's own namespace, detected on first use unless
// set explicitly.
type Client struct {
	Addr      string
	Namespace string
	token     string
	http      *http.Client

	mu        sync.Mutex
	tokenNS   string
	tokenNSOK bool       // tokenNS is known
	tokenNSBy string     // how it was determined
	lastInfo  *TokenInfo // from the last successful lookup of this token
}

// Config holds connection settings.
type Config struct {
	Addr      string
	Token     string
	Namespace string
	// TokenNamespace is the namespace the token was issued in. Nil means
	// detect it; "" is the root namespace.
	TokenNamespace *string
	CACert         string
	ClientCert     string
	ClientKey      string
	SkipVerify     bool
}

// New builds a client from a config.
func New(cfg Config) (*Client, error) {
	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.SkipVerify} //nolint:gosec // opt-in via VAULT_SKIP_VERIFY
	if cfg.CACert != "" {
		pem, err := os.ReadFile(cfg.CACert)
		if err != nil {
			return nil, fmt.Errorf("reading VAULT_CACERT: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("VAULT_CACERT contains no certificates")
		}
		tlsCfg.RootCAs = pool
	}
	if cfg.ClientCert != "" && cfg.ClientKey != "" {
		cert, err := tls.LoadX509KeyPair(cfg.ClientCert, cfg.ClientKey)
		if err != nil {
			return nil, fmt.Errorf("loading client certificate: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     tlsCfg,
		MaxIdleConns:        128,
		MaxIdleConnsPerHost: 128,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	c := &Client{
		Addr:      strings.TrimRight(cfg.Addr, "/"),
		Namespace: strings.Trim(cfg.Namespace, "/"),
		token:     cfg.Token,
		http:      &http.Client{Transport: tr, Timeout: 30 * time.Second},
	}
	if cfg.TokenNamespace != nil {
		c.tokenNS, c.tokenNSOK, c.tokenNSBy = strings.Trim(*cfg.TokenNamespace, "/"), true, "configured"
	}
	return c, nil
}

// Token returns the token in use.
func (c *Client) Token() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// WithToken returns a client with the same server, TLS settings and
// secrets namespace but another token, issued in namespace ns.
func (c *Client) WithToken(token, ns string) *Client {
	n := &Client{Addr: c.Addr, Namespace: c.Namespace, http: c.http}
	n.SetToken(token, ns)
	return n
}

// InNamespace returns a client for the same server and token that reads
// secrets in namespace ns ("" = root). The token namespace carries over.
func (c *Client) InNamespace(ns string) *Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return &Client{
		Addr: c.Addr, Namespace: strings.Trim(ns, "/"), token: c.token, http: c.http,
		tokenNS: c.tokenNS, tokenNSOK: c.tokenNSOK, tokenNSBy: c.tokenNSBy, lastInfo: c.lastInfo,
	}
}

// SetToken switches to a new token issued in namespace ns ("" = root), as
// after a login.
func (c *Client) SetToken(token, ns string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
	c.tokenNS, c.tokenNSOK, c.tokenNSBy = strings.Trim(ns, "/"), true, "login"
	c.lastInfo = nil
}

// Response is the generic Vault response envelope.
type Response struct {
	Data     json.RawMessage `json:"data"`
	Auth     json.RawMessage `json:"auth"`
	Errors   []string        `json:"errors"`
	Warnings []string        `json:"warnings"`
	// ServerTime is the Date header of the response, used as a clock that
	// cannot be tampered with locally.
	ServerTime time.Time `json:"-"`
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (*Response, error) {
	return c.doIn(ctx, c.Namespace, method, path, query, body)
}

func (c *Client) doIn(ctx context.Context, ns, method, path string, query url.Values, body any) (*Response, error) {
	return c.send(ctx, true, ns, method, path, query, body)
}

// Unauthenticated sends a request without a token (login endpoints) in
// namespace ns.
func (c *Client) Unauthenticated(ctx context.Context, ns, method, path string, query url.Values, body any) (*Response, error) {
	return c.send(ctx, false, strings.Trim(ns, "/"), method, path, query, body)
}

func (c *Client) send(ctx context.Context, withToken bool, ns, method, path string, query url.Values, body any) (*Response, error) {
	u := c.Addr + "/v1/" + escapePath(strings.TrimLeft(path, "/"))
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	if withToken {
		req.Header.Set("X-Vault-Token", c.Token())
	}
	req.Header.Set("X-Vault-Request", "true")
	if ns != "" {
		req.Header.Set("X-Vault-Namespace", ns)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	out := &Response{}
	if d, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		out.ServerTime = d
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil && resp.StatusCode < 300 {
			return nil, fmt.Errorf("%s %s: decoding response: %w", method, path, err)
		}
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return out, fmt.Errorf("%s: %w", path, ErrNotFound)
	case resp.StatusCode == http.StatusForbidden:
		return out, fmt.Errorf("%s: %w", path, ErrForbidden)
	case resp.StatusCode >= 300:
		return out, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.Join(out.Errors, "; "))
	}
	return out, nil
}

// escapePath escapes each segment so names containing '#', '?', '%' or
// spaces reach Vault intact.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// Read performs a GET and returns the response.
func (c *Client) Read(ctx context.Context, path string) (*Response, error) {
	return c.do(ctx, http.MethodGet, path, nil, nil)
}

// Write performs a POST with a JSON body.
func (c *Client) Write(ctx context.Context, path string, body any) (*Response, error) {
	return c.do(ctx, http.MethodPost, path, nil, body)
}

// Delete performs a DELETE.
func (c *Client) Delete(ctx context.Context, path string) error {
	_, err := c.do(ctx, http.MethodDelete, path, nil, nil)
	return err
}

// List returns the child keys under path. Folders end with "/".
func (c *Client) List(ctx context.Context, path string) ([]string, error) {
	resp, err := c.do(ctx, http.MethodGet, path, url.Values{"list": {"true"}}, nil)
	if err != nil {
		return nil, err
	}
	var d struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return nil, fmt.Errorf("list %s: %w", path, err)
	}
	return d.Keys, nil
}

// Mount describes a secrets engine mount.
type Mount struct {
	Path      string // with trailing slash, e.g. "secret/"
	Type      string
	KVVersion int // 1 or 2 for KV mounts, 0 otherwise
}

type mountInfo struct {
	Type    string            `json:"type"`
	Options map[string]string `json:"options"`
}

// KVMounts returns all KV secrets engines visible to the token. It tries
// sys/internal/ui/mounts first (readable by any token and filtered by
// policy), then sys/mounts.
func (c *Client) KVMounts(ctx context.Context) ([]Mount, error) {
	var mounts map[string]mountInfo
	if resp, err := c.Read(ctx, "sys/internal/ui/mounts"); err == nil {
		var d struct {
			Secret map[string]mountInfo `json:"secret"`
		}
		if err := json.Unmarshal(resp.Data, &d); err == nil && len(d.Secret) > 0 {
			mounts = d.Secret
		}
	}
	if mounts == nil {
		resp, err := c.Read(ctx, "sys/mounts")
		if err != nil {
			return nil, fmt.Errorf("discovering mounts (set VAULTR_MOUNTS to skip discovery): %w", err)
		}
		if err := json.Unmarshal(resp.Data, &mounts); err != nil {
			return nil, fmt.Errorf("decoding sys/mounts: %w", err)
		}
	}
	var out []Mount
	for p, m := range mounts {
		v := kvVersion(m)
		if v == 0 {
			continue
		}
		out = append(out, Mount{Path: strings.TrimLeft(p, "/"), Type: m.Type, KVVersion: v})
	}
	return out, nil
}

func kvVersion(m mountInfo) int {
	switch m.Type {
	case "kv":
		if m.Options["version"] == "2" {
			return 2
		}
		return 1
	case "generic":
		return 1
	}
	return 0
}

// MountFor asks Vault which mount a path belongs to and its KV version,
// using the same preflight call as the vault CLI.
func (c *Client) MountFor(ctx context.Context, path string) (Mount, error) {
	resp, err := c.Read(ctx, "sys/internal/ui/mounts/"+strings.TrimLeft(path, "/"))
	if err != nil {
		return Mount{}, err
	}
	var d struct {
		Path string `json:"path"`
		mountInfo
	}
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return Mount{}, err
	}
	v := kvVersion(d.mountInfo)
	if v == 0 {
		return Mount{}, fmt.Errorf("%s is not a KV mount (type %q)", d.Path, d.Type)
	}
	return Mount{Path: d.Path, Type: d.Type, KVVersion: v}, nil
}

// ReadSecret reads a KV secret's data. rel is the path relative to the mount.
func (c *Client) ReadSecret(ctx context.Context, m Mount, rel string) (map[string]any, error) {
	p := m.Path + rel
	if m.KVVersion == 2 {
		p = m.Path + "data/" + rel
	}
	resp, err := c.Read(ctx, p)
	if err != nil {
		return nil, err
	}
	if m.KVVersion == 2 {
		var d struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(resp.Data, &d); err != nil {
			return nil, err
		}
		return d.Data, nil
	}
	var d map[string]any
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return nil, err
	}
	return d, nil
}

// ListSecrets lists children of a KV folder. rel is relative to the mount
// and is either empty or ends with "/".
func (c *Client) ListSecrets(ctx context.Context, m Mount, rel string) ([]string, error) {
	if m.KVVersion == 2 {
		return c.List(ctx, m.Path+"metadata/"+rel)
	}
	return c.List(ctx, m.Path+rel)
}

// TokenInfo is the subset of auth/token/lookup-self we care about.
type TokenInfo struct {
	Accessor   string
	ExpireTime time.Time // zero for non-expiring tokens
	ServerTime time.Time
	Namespace  string // namespace the token belongs to ("" = root)
	Renewable  bool
	// EntityID is the identity the token was issued to; the same person
	// logging in again gets the same one. Empty for tokens without an
	// identity (root tokens, tokens created directly).
	EntityID string
}

type lookupData struct {
	Accessor      string  `json:"accessor"`
	ExpireTime    *string `json:"expire_time"`
	TTL           int64   `json:"ttl"`
	NamespacePath *string `json:"namespace_path"`
	Renewable     bool    `json:"renewable"`
	EntityID      string  `json:"entity_id"`
}

func (c *Client) lookupIn(ctx context.Context, ns string) (TokenInfo, *string, error) {
	tok := c.Token()
	resp, err := c.doIn(ctx, ns, http.MethodGet, "auth/token/lookup-self", nil, nil)
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			return TokenInfo{}, nil, fmt.Errorf("token lookup: %w (%w)", ErrTokenInvalid, err)
		}
		return TokenInfo{}, nil, fmt.Errorf("token lookup: %w", err)
	}
	var d lookupData
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return TokenInfo{}, nil, err
	}
	ti := TokenInfo{Accessor: d.Accessor, ServerTime: resp.ServerTime, Namespace: ns, Renewable: d.Renewable, EntityID: d.EntityID}
	if ti.ServerTime.IsZero() {
		ti.ServerTime = time.Now()
	}
	if d.ExpireTime != nil && *d.ExpireTime != "" {
		if t, err := time.Parse(time.RFC3339Nano, *d.ExpireTime); err == nil {
			ti.ExpireTime = t
		}
	}
	if ti.ExpireTime.IsZero() && d.TTL > 0 {
		ti.ExpireTime = ti.ServerTime.Add(time.Duration(d.TTL) * time.Second)
	}
	c.mu.Lock()
	if c.token == tok {
		info := ti
		c.lastInfo = &info
	}
	c.mu.Unlock()
	return ti, d.NamespacePath, nil
}

// LastTokenInfo returns what the last successful lookup of the current
// token said, without contacting the server.
func (c *Client) LastTokenInfo() (TokenInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastInfo == nil {
		return TokenInfo{}, false
	}
	return *c.lastInfo, true
}

// LookupSelf validates the token and returns its expiry. On first use it
// also determines the token's namespace: the server's namespace_path when
// reported, else the root namespace if the token is valid there, else the
// secrets namespace.
func (c *Client) LookupSelf(ctx context.Context) (TokenInfo, error) {
	c.mu.Lock()
	known, ns := c.tokenNSOK, c.tokenNS
	c.mu.Unlock()
	if known {
		ti, _, err := c.lookupIn(ctx, ns)
		return ti, err
	}

	candidates := []string{""}
	if c.Namespace != "" {
		candidates = append(candidates, c.Namespace)
	}
	var firstErr error
	for _, cand := range candidates {
		ti, reported, err := c.lookupIn(ctx, cand)
		if err != nil {
			if firstErr == nil || !errors.Is(err, ErrForbidden) {
				firstErr = err
			}
			if errors.Is(err, ErrForbidden) {
				continue
			}
			return TokenInfo{}, err
		}
		by := "detected"
		if reported != nil {
			ti.Namespace, by = strings.Trim(*reported, "/"), "reported by server"
		}
		c.mu.Lock()
		c.tokenNS, c.tokenNSOK, c.tokenNSBy = ti.Namespace, true, by
		c.mu.Unlock()
		return ti, nil
	}
	return TokenInfo{}, firstErr
}

// TokenNamespace returns the token's namespace, detecting it if needed,
// and how it was determined.
func (c *Client) TokenNamespace(ctx context.Context) (string, string, error) {
	c.mu.Lock()
	if c.tokenNSOK {
		defer c.mu.Unlock()
		return c.tokenNS, c.tokenNSBy, nil
	}
	c.mu.Unlock()
	ti, err := c.LookupSelf(ctx)
	if err != nil {
		return "", "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return ti.Namespace, c.tokenNSBy, nil
}

// KnownTokenNamespace returns the token namespace if already known,
// without contacting the server.
func (c *Client) KnownTokenNamespace() (ns, by string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokenNS, c.tokenNSBy, c.tokenNSOK
}

// HintTokenNamespace records a previously detected token namespace (e.g.
// from the cache header) so detection can be skipped. It never overrides
// a configured or already detected value.
func (c *Client) HintTokenNamespace(ns string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.tokenNSOK {
		c.tokenNS, c.tokenNSOK, c.tokenNSBy = ns, true, "cached"
	}
}

// TokenRead, TokenWrite, TokenDelete and TokenList act in the token's own
// namespace (for its cubbyhole).
func (c *Client) TokenRead(ctx context.Context, path string) (*Response, error) {
	ns, _, err := c.TokenNamespace(ctx)
	if err != nil {
		return nil, err
	}
	return c.doIn(ctx, ns, http.MethodGet, path, nil, nil)
}

// TokenWrite writes in the token's namespace.
func (c *Client) TokenWrite(ctx context.Context, path string, body any) (*Response, error) {
	ns, _, err := c.TokenNamespace(ctx)
	if err != nil {
		return nil, err
	}
	return c.doIn(ctx, ns, http.MethodPost, path, nil, body)
}

// TokenDelete deletes in the token's namespace.
func (c *Client) TokenDelete(ctx context.Context, path string) error {
	ns, _, err := c.TokenNamespace(ctx)
	if err != nil {
		return err
	}
	_, err = c.doIn(ctx, ns, http.MethodDelete, path, nil, nil)
	return err
}

// TokenList lists in the token's namespace.
func (c *Client) TokenList(ctx context.Context, path string) ([]string, error) {
	ns, _, err := c.TokenNamespace(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := c.doIn(ctx, ns, http.MethodGet, path, url.Values{"list": {"true"}}, nil)
	if err != nil {
		return nil, err
	}
	var d struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return nil, err
	}
	return d.Keys, nil
}

// ErrNoNamespaces means the server has no namespaces (they need Vault
// Enterprise or OpenBao).
var ErrNoNamespaces = errors.New("this server has no namespaces (Vault Enterprise or OpenBao)")

// maxNamespaces bounds ListNamespaces on very large servers.
const maxNamespaces = 2000

// ListNamespaces returns the namespaces the token can use, as full paths
// ("" is the root): its own namespace and those below it that its
// policies reach. It uses sys/internal/ui/namespaces, which any token may
// call (like the Vault UI's namespace picker), falling back to listing
// sys/namespaces.
func (c *Client) ListNamespaces(ctx context.Context) ([]string, error) {
	home, _, err := c.TokenNamespace(ctx)
	if err != nil {
		return nil, err
	}
	top, err := c.childNamespaces(ctx, home)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{home: true}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	// Servers answer with direct children (OpenBao) or every descendant
	// (possibly Vault Enterprise); relative paths may be nested either way,
	// so each new namespace is asked for its own children.
	var visit func(parent string, children []string)
	visit = func(parent string, children []string) {
		for _, ch := range children {
			full := strings.Trim(parent+"/"+strings.Trim(ch, "/"), "/")
			mu.Lock()
			if seen[full] || len(seen) >= maxNamespaces {
				mu.Unlock()
				continue
			}
			seen[full] = true
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				kids, err := c.childNamespaces(ctx, full)
				<-sem
				if err == nil {
					visit(full, kids)
				}
			}()
		}
	}
	visit(home, top)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out, nil
}

// childNamespaces lists the namespaces below ns, relative to it.
func (c *Client) childNamespaces(ctx context.Context, ns string) ([]string, error) {
	resp, err := c.doIn(ctx, ns, http.MethodGet, "sys/internal/ui/namespaces", nil, nil)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err != nil && len(resp.Errors) == 0 {
		return nil, nil // an empty list: 404 without an error message
	}
	if err != nil {
		// No UI endpoint: list sys/namespaces (needs a policy allowing it).
		resp, err = c.doIn(ctx, ns, http.MethodGet, "sys/namespaces", url.Values{"list": {"true"}}, nil)
		if errors.Is(err, ErrNotFound) && len(resp.Errors) > 0 {
			return nil, ErrNoNamespaces // "unsupported path"
		}
		if errors.Is(err, ErrNotFound) {
			return nil, nil // no namespaces below ns
		}
		if err != nil {
			return nil, err
		}
	}
	var d struct {
		Keys []string `json:"keys"`
	}
	if len(resp.Data) > 0 && string(resp.Data) != "null" {
		if err := json.Unmarshal(resp.Data, &d); err != nil {
			return nil, fmt.Errorf("listing namespaces: %w", err)
		}
	}
	return d.Keys, nil
}

// Stringify renders secret values as strings; non-strings become JSON.
func Stringify(data map[string]any) map[string]string {
	out := make(map[string]string, len(data))
	for k, v := range data {
		switch t := v.(type) {
		case string:
			out[k] = t
		case nil:
			out[k] = ""
		default:
			b, _ := json.Marshal(t)
			out[k] = string(b)
		}
	}
	return out
}
