// Package config resolves vaultr settings from, in order of precedence,
// environment variables, the config file, and defaults.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/vault"
)

// MaxAge is the hard cap on the cache lifetime.
const MaxAge = cache.MaxAge

// File is the on-disk config format.
type File struct {
	Address   string `toml:"address"`
	Namespace string `toml:"namespace"`
	// TokenNamespace is where the token was issued, when it differs from
	// Namespace and auto-detection is not wanted.
	TokenNamespace *string  `toml:"token_namespace"`
	CACert         string   `toml:"ca_cert"`
	ClientCert     string   `toml:"client_cert"`
	ClientKey      string   `toml:"client_key"`
	Mounts         []string `toml:"mounts"`
	Workers        int      `toml:"workers"`
	MaxAge         string   `toml:"max_age"`
	PathsOnly      bool     `toml:"paths_only"`
	ClipClear      string   `toml:"clip_clear"`
	CacheDir       string   `toml:"cache_dir"`
	Auth           Auth     `toml:"auth"`
}

// Auth holds login defaults for `vaultr login` and the TUI login screen.
type Auth struct {
	Method       string  `toml:"method"`
	Mount        string  `toml:"mount"`
	Role         string  `toml:"role"`
	Username     string  `toml:"username"`
	Namespace    *string `toml:"namespace"`
	CallbackPort int     `toml:"callback_port"`
	SaveToken    *bool   `toml:"save_token"`
	AutoLogin    bool    `toml:"auto_login"`
}

// AuthSettings are the resolved login defaults.
type AuthSettings struct {
	Method       string // "" = ask (the TUI starts on oidc)
	Mount        string // "" = the method name
	Role         string
	Username     string
	Namespace    string // where to log in; "" = root
	CallbackPort int
	SaveToken    bool
	// AutoLogin starts a login by itself when the token is missing or
	// dead, instead of waiting for the user to ask.
	AutoLogin bool
}

// Settings are the resolved values.
type Settings struct {
	Vault     vault.Config
	Mounts    []string // with trailing slash
	Workers   int
	MaxAge    time.Duration
	PathsOnly bool
	ClipClear time.Duration
	CacheDir  string
	Auth      AuthSettings

	Path      string            // config file path consulted
	Found     bool              // whether it exists
	Source    map[string]string // setting name -> where its value came from
	TokenFile string            // ~/.vault-token path consulted
}

// DefaultPath returns the config file location: $VAULTR_CONFIG, else
// $XDG_CONFIG_HOME/vaultr/config.toml, else ~/.config/vaultr/config.toml
// (%AppData%\vaultr\config.toml on Windows).
func DefaultPath() string {
	if p := os.Getenv("VAULTR_CONFIG"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "vaultr", "config.toml")
	}
	if home, err := os.UserHomeDir(); err == nil && os.PathSeparator == '/' {
		return filepath.Join(home, ".config", "vaultr", "config.toml")
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "vaultr", "config.toml")
	}
	return "vaultr.toml"
}

// ReadFile parses a config file. A missing file is not an error.
func ReadFile(path string) (File, bool, error) {
	var f File
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, false, nil
	}
	if err != nil {
		return f, false, err
	}
	md, err := toml.Decode(string(raw), &f)
	if err != nil {
		return f, true, fmt.Errorf("%s: %w", path, err)
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		keys := make([]string, len(extra))
		for i, k := range extra {
			keys[i] = k.String()
			if k.String() == "token" {
				return f, true, fmt.Errorf("%s: tokens are not read from the config file, use VAULT_TOKEN or ~/.vault-token", path)
			}
		}
		return f, true, fmt.Errorf("%s: unknown setting(s): %s", path, strings.Join(keys, ", "))
	}
	return f, true, nil
}

// Load resolves all settings. A missing token is not an error here; see
// Settings.RequireToken.
func Load() (*Settings, error) {
	s := &Settings{
		Path:      DefaultPath(),
		Source:    map[string]string{},
		Workers:   32,
		MaxAge:    MaxAge,
		ClipClear: 45 * time.Second,
	}
	f, found, err := ReadFile(s.Path)
	if err != nil {
		return nil, err
	}
	s.Found = found

	pick := func(name string, dst *string, envs []string, file, def string) {
		for _, e := range envs {
			if v := os.Getenv(e); v != "" {
				*dst, s.Source[name] = v, "env "+e
				return
			}
		}
		if file != "" {
			*dst, s.Source[name] = file, "config"
			return
		}
		*dst, s.Source[name] = def, "default"
	}

	v := &s.Vault
	pick("address", &v.Addr, []string{"VAULT_ADDR", "VAULT_URL"}, f.Address, "https://127.0.0.1:8200")
	pick("namespace", &v.Namespace, []string{"VAULT_NAMESPACE"}, f.Namespace, "")
	v.Namespace = strings.Trim(v.Namespace, "/") // "/" means root
	switch e := os.Getenv("VAULTR_TOKEN_NAMESPACE"); {
	case e != "":
		t := strings.Trim(e, "/")
		v.TokenNamespace, s.Source["token_namespace"] = &t, "env VAULTR_TOKEN_NAMESPACE"
	case f.TokenNamespace != nil:
		t := strings.Trim(*f.TokenNamespace, "/")
		v.TokenNamespace, s.Source["token_namespace"] = &t, "config"
	default:
		s.Source["token_namespace"] = "auto-detect"
	}
	pick("ca_cert", &v.CACert, []string{"VAULT_CACERT"}, f.CACert, "")
	pick("client_cert", &v.ClientCert, []string{"VAULT_CLIENT_CERT"}, f.ClientCert, "")
	pick("client_key", &v.ClientKey, []string{"VAULT_CLIENT_KEY"}, f.ClientKey, "")
	if e := os.Getenv("VAULT_SKIP_VERIFY"); e != "" {
		b, err := strconv.ParseBool(e)
		if err != nil {
			return nil, fmt.Errorf("invalid VAULT_SKIP_VERIFY: %w", err)
		}
		v.SkipVerify = b
	}

	// Token: VAULT_TOKEN, then ~/.vault-token.
	if t := os.Getenv("VAULT_TOKEN"); t != "" {
		v.Token, s.Source["token"] = t, "env VAULT_TOKEN"
	} else if home, err := os.UserHomeDir(); err == nil {
		s.TokenFile = filepath.Join(home, ".vault-token")
		if b, err := os.ReadFile(s.TokenFile); err == nil {
			v.Token, s.Source["token"] = strings.TrimSpace(string(b)), "~/.vault-token"
		}
	}
	if v.Token == "" {
		s.Source["token"] = "none"
	}

	// Mounts.
	mounts, src := f.Mounts, "config"
	if e := os.Getenv("VAULTR_MOUNTS"); e != "" {
		mounts, src = strings.Split(e, ","), "env VAULTR_MOUNTS"
	}
	for _, m := range mounts {
		if m = strings.Trim(strings.TrimSpace(m), "/"); m != "" {
			s.Mounts = append(s.Mounts, m+"/")
		}
	}
	if len(s.Mounts) == 0 {
		src = "default (discover)"
	}
	s.Source["mounts"] = src

	// Workers.
	s.Source["workers"] = "default"
	if f.Workers != 0 {
		s.Workers, s.Source["workers"] = f.Workers, "config"
	}
	if e := os.Getenv("VAULTR_WORKERS"); e != "" {
		n, err := strconv.Atoi(e)
		if err != nil {
			return nil, fmt.Errorf("VAULTR_WORKERS: invalid value %q", e)
		}
		s.Workers, s.Source["workers"] = n, "env VAULTR_WORKERS"
	}
	if s.Workers < 1 {
		return nil, fmt.Errorf("workers must be at least 1 (%s)", s.Source["workers"])
	}

	// Durations.
	dur := func(name, env, file string, dst *time.Duration) error {
		raw, src := file, "config"
		if e := os.Getenv(env); e != "" {
			raw, src = e, "env "+env
		}
		if raw == "" {
			s.Source[name] = "default"
			return nil
		}
		d, err := time.ParseDuration(raw)
		if err != nil {
			if raw == "0" {
				d, err = 0, nil
			} else {
				return fmt.Errorf("%s (%s): %w", name, src, err)
			}
		}
		*dst, s.Source[name] = d, src
		return nil
	}
	if err := dur("max_age", "VAULTR_MAX_AGE", f.MaxAge, &s.MaxAge); err != nil {
		return nil, err
	}
	if s.MaxAge <= 0 || s.MaxAge > MaxAge {
		s.MaxAge = MaxAge
	}
	if err := dur("clip_clear", "VAULTR_CLIP_CLEAR", f.ClipClear, &s.ClipClear); err != nil {
		return nil, err
	}

	// Booleans.
	s.PathsOnly, s.Source["paths_only"] = f.PathsOnly, "config"
	if !found {
		s.Source["paths_only"] = "default"
	}
	if e := os.Getenv("VAULTR_PATHS_ONLY"); e != "" {
		b, err := strconv.ParseBool(e)
		if err != nil {
			return nil, fmt.Errorf("VAULTR_PATHS_ONLY: invalid value %q", e)
		}
		s.PathsOnly, s.Source["paths_only"] = b, "env VAULTR_PATHS_ONLY"
	}

	// Cache dir.
	pick("cache_dir", &s.CacheDir, []string{"VAULTR_CACHE_DIR"}, f.CacheDir, "")
	if s.CacheDir == "" {
		d, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		s.CacheDir = filepath.Join(d, "vaultr")
	}

	// Login defaults.
	a := f.Auth
	s.Auth = AuthSettings{Method: a.Method, Mount: a.Mount, Role: a.Role, Username: a.Username, CallbackPort: a.CallbackPort, SaveToken: true, AutoLogin: a.AutoLogin}
	switch {
	case a.Namespace != nil:
		s.Auth.Namespace = strings.Trim(*a.Namespace, "/")
	case v.TokenNamespace != nil:
		s.Auth.Namespace = *v.TokenNamespace
	}
	if a.SaveToken != nil {
		s.Auth.SaveToken = *a.SaveToken
	}
	if err := validateAuth(a); err != nil {
		return nil, fmt.Errorf("%s: %w", s.Path, err)
	}
	return s, nil
}

// RequireToken fails when no token was found.
func (s *Settings) RequireToken() error {
	if s.Vault.Token == "" {
		return errors.New("no Vault token: run `vaultr login` (or `vault login`, or set VAULT_TOKEN)")
	}
	return nil
}

// Describe renders the effective settings and where each came from.
func (s *Settings) Describe() string {
	var b strings.Builder
	state := "not found"
	if s.Found {
		state = "loaded"
	}
	fmt.Fprintf(&b, "config file: %s (%s)\n\n", s.Path, state)
	ns := s.Vault.Namespace
	if ns == "" {
		ns = "(root)"
	}
	mounts := strings.Join(s.Mounts, ", ")
	if mounts == "" {
		mounts = "(discover)"
	}
	token := "not set"
	if s.Vault.Token != "" {
		token = "set"
	}
	rows := [][2]string{
		{"address", s.Vault.Addr},
		{"namespace", ns},
		{"token_namespace", tokenNS(s)},
		{"token", token},
		{"ca_cert", s.Vault.CACert},
		{"client_cert", s.Vault.ClientCert},
		{"client_key", s.Vault.ClientKey},
		{"mounts", mounts},
		{"workers", strconv.Itoa(s.Workers)},
		{"max_age", s.MaxAge.String()},
		{"paths_only", strconv.FormatBool(s.PathsOnly)},
		{"clip_clear", s.ClipClear.String()},
		{"cache_dir", s.CacheDir},
	}
	for _, r := range rows {
		src := s.Source[r[0]]
		if r[1] == "" && src == "default" {
			continue
		}
		fmt.Fprintf(&b, "%-12s %-40s %s\n", r[0], r[1], "("+src+")")
	}
	return b.String()
}

func tokenNS(s *Settings) string {
	switch {
	case s.Vault.TokenNamespace == nil:
		return "(auto)"
	case *s.Vault.TokenNamespace == "":
		return "(root)"
	}
	return *s.Vault.TokenNamespace
}
