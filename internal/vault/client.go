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
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrNotFound is returned for 404 responses.
var ErrNotFound = errors.New("not found")

// ErrForbidden is returned for 403 responses.
var ErrForbidden = errors.New("permission denied")

// Client talks to a single Vault server with a single token.
type Client struct {
	Addr      string
	Namespace string
	token     string
	http      *http.Client
}

// Config holds connection settings.
type Config struct {
	Addr       string
	Token      string
	Namespace  string
	CACert     string
	ClientCert string
	ClientKey  string
	SkipVerify bool
}

// ConfigFromEnv reads the standard VAULT_* environment variables, falling
// back to ~/.vault-token for the token.
func ConfigFromEnv() (Config, error) {
	c := Config{
		Addr:       os.Getenv("VAULT_ADDR"),
		Token:      os.Getenv("VAULT_TOKEN"),
		Namespace:  os.Getenv("VAULT_NAMESPACE"),
		CACert:     os.Getenv("VAULT_CACERT"),
		ClientCert: os.Getenv("VAULT_CLIENT_CERT"),
		ClientKey:  os.Getenv("VAULT_CLIENT_KEY"),
	}
	if c.Addr == "" {
		c.Addr = "https://127.0.0.1:8200"
	}
	if v := os.Getenv("VAULT_SKIP_VERIFY"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, fmt.Errorf("invalid VAULT_SKIP_VERIFY: %w", err)
		}
		c.SkipVerify = b
	}
	if c.Token == "" {
		if home, err := os.UserHomeDir(); err == nil {
			if b, err := os.ReadFile(filepath.Join(home, ".vault-token")); err == nil {
				c.Token = strings.TrimSpace(string(b))
			}
		}
	}
	if c.Token == "" {
		return c, errors.New("no Vault token: set VAULT_TOKEN or run `vault login`")
	}
	return c, nil
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
	return &Client{
		Addr:      strings.TrimRight(cfg.Addr, "/"),
		Namespace: strings.Trim(cfg.Namespace, "/"),
		token:     cfg.Token,
		http:      &http.Client{Transport: tr, Timeout: 30 * time.Second},
	}, nil
}

// Token returns the token in use.
func (c *Client) Token() string { return c.token }

// Response is the generic Vault response envelope.
type Response struct {
	Data     json.RawMessage `json:"data"`
	Errors   []string        `json:"errors"`
	Warnings []string        `json:"warnings"`
	// ServerTime is the Date header of the response, used as a clock that
	// cannot be tampered with locally.
	ServerTime time.Time `json:"-"`
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (*Response, error) {
	u := c.Addr + "/v1/" + strings.TrimLeft(path, "/")
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
	req.Header.Set("X-Vault-Token", c.token)
	req.Header.Set("X-Vault-Request", "true")
	if c.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", c.Namespace)
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
}

// LookupSelf validates the token and returns its expiry.
func (c *Client) LookupSelf(ctx context.Context) (TokenInfo, error) {
	resp, err := c.Read(ctx, "auth/token/lookup-self")
	if err != nil {
		return TokenInfo{}, fmt.Errorf("token lookup: %w", err)
	}
	var d struct {
		Accessor   string  `json:"accessor"`
		ExpireTime *string `json:"expire_time"`
		TTL        int64   `json:"ttl"`
	}
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return TokenInfo{}, err
	}
	ti := TokenInfo{Accessor: d.Accessor, ServerTime: resp.ServerTime}
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
	return ti, nil
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
