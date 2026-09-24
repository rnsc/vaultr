package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Kind is how a setting is edited and validated.
type Kind int

// Setting kinds.
const (
	KindString Kind = iota
	KindList        // comma separated in the editor
	KindInt
	KindBool
	KindDuration
	KindChoice
)

// Field describes one config file setting. Key is dotted for tables
// ("auth.method"); Env names the variable that overrides it, if any.
type Field struct {
	Key     string
	Kind    Kind
	Help    string
	Example string
	Env     string
	Choices []string // KindChoice; "" first means "not set"
	Default string   // KindBool default
}

// Name is the key without its table prefix.
func (f Field) Name() string { return f.Key[strings.LastIndexByte(f.Key, '.')+1:] }

// Fields lists every setting, in file order.
var Fields = []Field{
	{Key: "address", Help: "Vault server.", Example: `"https://vault.example.com"`, Env: "VAULT_ADDR"},
	{Key: "namespace", Help: "Namespace holding your secrets (Vault Enterprise / OpenBao). VAULT_NAMESPACE=/ forces the root namespace.", Example: `"team-a"`, Env: "VAULT_NAMESPACE"},
	{Key: "token_namespace", Help: `Namespace your token was issued in, when you log in elsewhere than the secrets namespace. Detected automatically; "/" is the root namespace.`, Example: `"/"`, Env: "VAULTR_TOKEN_NAMESPACE"},
	{Key: "ca_cert", Help: "CA certificate for the Vault server.", Example: `"/etc/ssl/vault-ca.pem"`, Env: "VAULT_CACERT"},
	{Key: "client_cert", Help: "Client certificate (mutual TLS).", Example: `""`, Env: "VAULT_CLIENT_CERT"},
	{Key: "client_key", Help: "Client certificate key (mutual TLS).", Example: `""`, Env: "VAULT_CLIENT_KEY"},
	{Key: "mounts", Kind: KindList, Help: "KV mounts to index. Empty discovers every KV mount the token sees.", Example: `["secret", "kv-team"]`, Env: "VAULTR_MOUNTS"},
	{Key: "workers", Kind: KindInt, Help: "Concurrent requests while indexing.", Example: "32", Env: "VAULTR_WORKERS"},
	{Key: "max_age", Kind: KindDuration, Help: "Cache lifetime, capped at 2h.", Example: `"2h"`, Env: "VAULTR_MAX_AGE"},
	{Key: "paths_only", Kind: KindBool, Help: "Index paths only, without reading key names.", Example: "false", Env: "VAULTR_PATHS_ONLY", Default: "false"},
	{Key: "clip_clear", Kind: KindDuration, Help: `Clear copied values from the clipboard after this long ("0" disables).`, Example: `"45s"`, Env: "VAULTR_CLIP_CLEAR"},
	{Key: "cache_dir", Help: "Where the encrypted index lives.", Example: `""`, Env: "VAULTR_CACHE_DIR"},

	{Key: "auth.method", Kind: KindChoice, Choices: []string{"", "oidc", "ldap", "userpass", "token"}, Help: "Login method for `vaultr login` and the TUI login screen.", Example: `"oidc"`},
	{Key: "auth.mount", Help: "Auth mount path, when it differs from the method name.", Example: `"oidc"`},
	{Key: "auth.role", Help: "OIDC role; empty uses the mount's default role.", Example: `""`},
	{Key: "auth.username", Help: "Username for ldap / userpass.", Example: `"jdoe"`},
	{Key: "auth.namespace", Help: `Namespace to log in to. Defaults to token_namespace, else the root namespace ("/").`, Example: `"/"`},
	{Key: "auth.callback_port", Kind: KindInt, Help: "Local port for the OIDC browser callback.", Example: "8250"},
	{Key: "auth.save_token", Kind: KindBool, Help: "Save the token to ~/.vault-token after logging in, like `vault login`.", Example: "true", Default: "true"},
}

// FieldByKey finds a field.
func FieldByKey(key string) (Field, bool) {
	for _, f := range Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

func optStr(p *string) string {
	if p == nil {
		return ""
	}
	if *p == "" {
		return "/"
	}
	return *p
}

func intStr(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// Values returns the file's settings as editor strings, keyed by
// Field.Key. Unset settings are "" (booleans get their default).
func (f File) Values() map[string]string {
	save := "true"
	if f.Auth.SaveToken != nil && !*f.Auth.SaveToken {
		save = "false"
	}
	return map[string]string{
		"address":            f.Address,
		"namespace":          f.Namespace,
		"token_namespace":    optStr(f.TokenNamespace),
		"ca_cert":            f.CACert,
		"client_cert":        f.ClientCert,
		"client_key":         f.ClientKey,
		"mounts":             strings.Join(f.Mounts, ", "),
		"workers":            intStr(f.Workers),
		"max_age":            f.MaxAge,
		"paths_only":         strconv.FormatBool(f.PathsOnly),
		"clip_clear":         f.ClipClear,
		"cache_dir":          f.CacheDir,
		"auth.method":        f.Auth.Method,
		"auth.mount":         f.Auth.Mount,
		"auth.role":          f.Auth.Role,
		"auth.username":      f.Auth.Username,
		"auth.namespace":     optStr(f.Auth.Namespace),
		"auth.callback_port": intStr(f.Auth.CallbackPort),
		"auth.save_token":    save,
	}
}

// FromValues builds and validates a File from editor strings.
func FromValues(v map[string]string) (File, error) {
	get := func(k string) string { return strings.TrimSpace(v[k]) }
	var errs []error
	num := func(k string) int {
		s := get(k)
		if s == "" {
			return 0
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %q is not a number", k, s))
		}
		return n
	}
	boolean := func(k string) bool {
		s := get(k)
		if s == "" {
			f, _ := FieldByKey(k)
			s = f.Default
		}
		b, err := strconv.ParseBool(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %q is not true or false", k, s))
		}
		return b
	}
	opt := func(k string) *string {
		s := get(k)
		if s == "" {
			return nil
		}
		t := strings.Trim(s, "/")
		return &t
	}
	f := File{
		Address:        get("address"),
		Namespace:      strings.Trim(get("namespace"), "/"),
		TokenNamespace: opt("token_namespace"),
		CACert:         get("ca_cert"),
		ClientCert:     get("client_cert"),
		ClientKey:      get("client_key"),
		Workers:        num("workers"),
		MaxAge:         get("max_age"),
		PathsOnly:      boolean("paths_only"),
		ClipClear:      get("clip_clear"),
		CacheDir:       get("cache_dir"),
		Auth: Auth{
			Method:       strings.ToLower(get("auth.method")),
			Mount:        strings.Trim(get("auth.mount"), "/"),
			Role:         get("auth.role"),
			Username:     get("auth.username"),
			Namespace:    opt("auth.namespace"),
			CallbackPort: num("auth.callback_port"),
		},
	}
	for _, m := range strings.Split(get("mounts"), ",") {
		if m = strings.Trim(strings.TrimSpace(m), "/"); m != "" {
			f.Mounts = append(f.Mounts, m)
		}
	}
	if !boolean("auth.save_token") {
		no := false
		f.Auth.SaveToken = &no
	}
	if len(errs) > 0 {
		return f, errors.Join(errs...)
	}
	return f, Validate(f)
}

// Validate checks values the TOML types don't.
func Validate(f File) error {
	var errs []error
	for k, d := range map[string]string{"max_age": f.MaxAge, "clip_clear": f.ClipClear} {
		if d != "" && d != "0" {
			if _, err := time.ParseDuration(d); err != nil {
				errs = append(errs, fmt.Errorf("%s: %q is not a duration (e.g. 30m, 2h)", k, d))
			}
		}
	}
	if f.Workers < 0 {
		errs = append(errs, errors.New("workers must be at least 1"))
	}
	if err := validateAuth(f.Auth); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func validateAuth(a Auth) error {
	switch strings.ToLower(a.Method) {
	case "", "oidc", "ldap", "userpass", "token":
	default:
		return fmt.Errorf("auth.method: %q is not one of oidc, ldap, userpass, token", a.Method)
	}
	if a.CallbackPort < 0 || a.CallbackPort > 65535 {
		return fmt.Errorf("auth.callback_port: %d is not a valid port", a.CallbackPort)
	}
	return nil
}

// Render produces a commented config file. Unset settings are written as
// commented examples, so the file documents every option.
func Render(f File) []byte {
	vals := f.Values()
	var b bytes.Buffer
	b.WriteString("# vaultr configuration. Environment variables take precedence over\n")
	b.WriteString("# these values. The Vault token is never stored here: use VAULT_TOKEN,\n")
	b.WriteString("# ~/.vault-token, or `vaultr login`.\n")
	table := ""
	for _, fd := range Fields {
		if t := fd.Key[:max(0, strings.LastIndexByte(fd.Key, '.'))]; t != table {
			table = t
			fmt.Fprintf(&b, "\n[%s]\n", table)
		}
		b.WriteString("\n")
		for _, line := range wrap(fd.Help, 72) {
			b.WriteString("# " + line + "\n")
		}
		if fd.Env != "" {
			fmt.Fprintf(&b, "# Overridden by %s.\n", fd.Env)
		}
		v := vals[fd.Key]
		if v == "" || (fd.Kind == KindBool && v == fd.Default) {
			fmt.Fprintf(&b, "# %s = %s\n", fd.Name(), fd.Example)
			continue
		}
		fmt.Fprintf(&b, "%s = %s\n", fd.Name(), tomlValue(fd, f))
	}
	return b.Bytes()
}

// tomlValue renders the field's value from f as TOML.
func tomlValue(fd Field, f File) string {
	var v any
	switch fd.Key {
	case "mounts":
		v = f.Mounts
	case "workers":
		v = f.Workers
	case "paths_only":
		v = f.PathsOnly
	case "auth.callback_port":
		v = f.Auth.CallbackPort
	case "auth.save_token":
		v = f.Auth.SaveToken == nil || *f.Auth.SaveToken
	default:
		v = f.Values()[fd.Key]
	}
	var b bytes.Buffer
	_ = toml.NewEncoder(&b).Encode(map[string]any{"v": v})
	return strings.TrimSpace(strings.TrimPrefix(b.String(), "v = "))
}

func wrap(s string, width int) []string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(s) {
		if line != "" && len(line)+1+len(w) > width {
			lines = append(lines, line)
			line = w
			continue
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	return append(lines, line)
}

// Template is the commented file `vaultr config init` writes.
func Template() []byte { return Render(File{}) }

// WriteFile validates f and writes it to path (mode 0600, parent
// directories created with 0700).
func WriteFile(path string, f File) error {
	if err := Validate(f); err != nil {
		return err
	}
	data := Render(f)
	// Refuse to write anything this package cannot read back identically.
	var back File
	if _, err := toml.Decode(string(data), &back); err != nil {
		return fmt.Errorf("internal error rendering config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
