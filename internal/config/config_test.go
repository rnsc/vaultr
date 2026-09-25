package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var allEnv = []string{
	"VAULT_ADDR", "VAULT_URL", "VAULT_TOKEN", "VAULT_NAMESPACE", "VAULT_CACERT",
	"VAULT_CLIENT_CERT", "VAULT_CLIENT_KEY", "VAULT_SKIP_VERIFY",
	"VAULTR_CONFIG", "VAULTR_MOUNTS", "VAULTR_WORKERS", "VAULTR_MAX_AGE", "VAULTR_REVEAL_TIMEOUT",
	"VAULTR_PATHS_ONLY", "VAULTR_CLIP_CLEAR", "VAULTR_CACHE_DIR", "XDG_CONFIG_HOME",
}

// isolate clears the environment and points HOME at a temp dir. It
// returns the config file path in use.
func isolate(t *testing.T, env map[string]string, file string) string {
	t.Helper()
	for _, k := range allEnv {
		t.Setenv(k, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("VAULTR_CONFIG", cfg)
	if file != "" {
		if err := os.WriteFile(cfg, []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	return cfg
}

func load(t *testing.T) *Settings {
	t.Helper()
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDefaults(t *testing.T) {
	isolate(t, nil, "")
	s := load(t)
	if s.Found || s.Vault.Addr != "https://127.0.0.1:8200" || s.Vault.Namespace != "" || s.Vault.Token != "" {
		t.Errorf("defaults: %+v", s)
	}
	if s.Workers != 32 || s.MaxAge != MaxAge || s.ClipClear != 45*time.Second || s.RevealTimeout != 30*time.Second || s.PathsOnly || s.Mounts != nil {
		t.Errorf("defaults: %+v", s)
	}
	if !strings.HasSuffix(s.CacheDir, "vaultr") {
		t.Errorf("cache dir %q", s.CacheDir)
	}
	if err := s.RequireToken(); err == nil || !strings.Contains(err.Error(), "vault login") {
		t.Errorf("RequireToken: %v", err)
	}
}

const fullFile = `
address    = "https://file.example.com"
namespace  = "team-a/"
ca_cert    = "/file/ca.pem"
mounts     = ["secret/", "/kv-team", " "]
workers    = 8
max_age    = "30m"
paths_only = true
clip_clear = "0"
cache_dir  = "/file/cache"
`

func TestFileValues(t *testing.T) {
	isolate(t, nil, fullFile)
	s := load(t)
	want := Settings{Workers: 8, MaxAge: 30 * time.Minute, PathsOnly: true, ClipClear: 0, CacheDir: "/file/cache"}
	if !s.Found || s.Vault.Addr != "https://file.example.com" || s.Vault.Namespace != "team-a" || s.Vault.CACert != "/file/ca.pem" {
		t.Errorf("vault settings from file: %+v", s.Vault)
	}
	if s.Workers != want.Workers || s.MaxAge != want.MaxAge || s.PathsOnly != want.PathsOnly || s.ClipClear != 0 || s.CacheDir != want.CacheDir {
		t.Errorf("settings from file: %+v", s)
	}
	if !reflect.DeepEqual(s.Mounts, []string{"secret/", "kv-team/"}) {
		t.Errorf("mounts %q", s.Mounts)
	}
	for _, k := range []string{"address", "namespace", "mounts", "workers", "max_age", "paths_only", "clip_clear", "cache_dir"} {
		if s.Source[k] != "config" {
			t.Errorf("source of %s = %q, want config", k, s.Source[k])
		}
	}
}

func TestEnvOverridesFile(t *testing.T) {
	isolate(t, map[string]string{
		"VAULT_ADDR":        "https://env.example.com",
		"VAULT_NAMESPACE":   "team-b",
		"VAULT_CACERT":      "/env/ca.pem",
		"VAULTR_MOUNTS":     "a,b/",
		"VAULTR_WORKERS":    "4",
		"VAULTR_MAX_AGE":    "10m",
		"VAULTR_PATHS_ONLY": "false",
		"VAULTR_CLIP_CLEAR": "5s",
		"VAULTR_CACHE_DIR":  "/env/cache",
	}, fullFile)
	s := load(t)
	if s.Vault.Addr != "https://env.example.com" || s.Vault.Namespace != "team-b" || s.Vault.CACert != "/env/ca.pem" {
		t.Errorf("vault: %+v", s.Vault)
	}
	if !reflect.DeepEqual(s.Mounts, []string{"a/", "b/"}) || s.Workers != 4 || s.MaxAge != 10*time.Minute || s.PathsOnly || s.ClipClear != 5*time.Second || s.CacheDir != "/env/cache" {
		t.Errorf("settings: %+v", s)
	}
	if s.Source["namespace"] != "env VAULT_NAMESPACE" || s.Source["address"] != "env VAULT_ADDR" {
		t.Errorf("sources: %v", s.Source)
	}
}

func TestNamespaceRootOverride(t *testing.T) {
	isolate(t, map[string]string{"VAULT_NAMESPACE": "/"}, `namespace = "team-a"`)
	if s := load(t); s.Vault.Namespace != "" {
		t.Errorf("VAULT_NAMESPACE=/ should select the root namespace, got %q", s.Vault.Namespace)
	}
}

func TestAddressAlias(t *testing.T) {
	isolate(t, map[string]string{"VAULT_URL": "https://url.example.com"}, `address = "https://file.example.com"`)
	if s := load(t); s.Vault.Addr != "https://url.example.com" || s.Source["address"] != "env VAULT_URL" {
		t.Errorf("VAULT_URL: %q (%s)", s.Vault.Addr, s.Source["address"])
	}
	t.Setenv("VAULT_ADDR", "https://addr.example.com")
	if s := load(t); s.Vault.Addr != "https://addr.example.com" {
		t.Errorf("VAULT_ADDR should win over VAULT_URL, got %q", s.Vault.Addr)
	}
}

func TestToken(t *testing.T) {
	isolate(t, nil, "")
	home := os.Getenv("HOME")
	if err := os.WriteFile(filepath.Join(home, ".vault-token"), []byte("hvs.file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := load(t)
	if s.Vault.Token != "hvs.file" || s.Source["token"] != "~/.vault-token" {
		t.Errorf("token file: %q (%s)", s.Vault.Token, s.Source["token"])
	}
	t.Setenv("VAULT_TOKEN", "hvs.env")
	if s := load(t); s.Vault.Token != "hvs.env" {
		t.Errorf("VAULT_TOKEN should win, got %q", s.Vault.Token)
	}
	if strings.Contains(load(t).Describe(), "hvs.") {
		t.Error("Describe leaks the token")
	}
}

func TestMaxAgeCapped(t *testing.T) {
	isolate(t, nil, `max_age = "12h"`)
	if s := load(t); s.MaxAge != MaxAge {
		t.Errorf("max_age not capped: %s", s.MaxAge)
	}
}

func TestInvalid(t *testing.T) {
	cases := []struct {
		env  map[string]string
		file string
		msg  string
	}{
		{file: `token = "hvs.x"`, msg: "tokens are not read"},
		{file: `adress = "typo"`, msg: "unknown setting(s): adress"},
		{file: `workers = "many"`, msg: "workers"},
		{file: `not toml`, msg: "config.toml"},
		{file: `max_age = "soon"`, msg: "max_age (config)"},
		{file: `workers = -2`, msg: "workers must be at least 1"},
		{env: map[string]string{"VAULTR_WORKERS": "x"}, msg: "VAULTR_WORKERS"},
		{env: map[string]string{"VAULTR_WORKERS": "0"}, msg: "at least 1"},
		{env: map[string]string{"VAULTR_MAX_AGE": "soon"}, msg: "env VAULTR_MAX_AGE"},
		{env: map[string]string{"VAULTR_CLIP_CLEAR": "later"}, msg: "clip_clear"},
		{env: map[string]string{"VAULTR_PATHS_ONLY": "perhaps"}, msg: "VAULTR_PATHS_ONLY"},
		{env: map[string]string{"VAULT_SKIP_VERIFY": "maybe"}, msg: "VAULT_SKIP_VERIFY"},
	}
	for _, c := range cases {
		isolate(t, c.env, c.file)
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("env %v file %q: error %v, want it to mention %q", c.env, c.file, err, c.msg)
		}
	}
}

func TestDefaultPath(t *testing.T) {
	isolate(t, nil, "")
	t.Setenv("VAULTR_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if p := DefaultPath(); p != filepath.Join("/xdg", "vaultr", "config.toml") {
		t.Errorf("XDG path %q", p)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	if p := DefaultPath(); !strings.HasSuffix(p, filepath.Join(".config", "vaultr", "config.toml")) && os.PathSeparator == '/' {
		t.Errorf("home path %q", p)
	}
	t.Setenv("VAULTR_CONFIG", "/explicit.toml")
	if p := DefaultPath(); p != "/explicit.toml" {
		t.Errorf("explicit path %q", p)
	}
}

func TestTemplateIsValidAndInert(t *testing.T) {
	isolate(t, nil, string(Template()))
	s := load(t)
	if !s.Found || s.Vault.Addr != "https://127.0.0.1:8200" || s.Workers != 32 {
		t.Errorf("template should parse and change nothing: %+v", s)
	}
}

func TestDescribe(t *testing.T) {
	isolate(t, map[string]string{"VAULT_NAMESPACE": "team-b", "VAULT_TOKEN": "hvs.secret"}, fullFile)
	out := load(t).Describe()
	for _, want := range []string{"(loaded)", "https://file.example.com", "(config)", "team-b", "(env VAULT_NAMESPACE)", "token", "set", "secret/, kv-team/"} {
		if !strings.Contains(out, want) {
			t.Errorf("Describe missing %q:\n%s", want, out)
		}
	}
}

func TestRevealTimeout(t *testing.T) {
	isolate(t, nil, "reveal_timeout = \"10s\"\n")
	if s := load(t); s.RevealTimeout != 10*time.Second || s.Source["reveal_timeout"] != "config" {
		t.Errorf("from file: %v (%s)", s.RevealTimeout, s.Source["reveal_timeout"])
	}
	isolate(t, map[string]string{"VAULTR_REVEAL_TIMEOUT": "0"}, "reveal_timeout = \"10s\"\n")
	if s := load(t); s.RevealTimeout != 0 {
		t.Errorf("env 0: %v", s.RevealTimeout)
	}
	isolate(t, map[string]string{"VAULTR_REVEAL_TIMEOUT": "soon"}, "")
	if _, err := Load(); err == nil {
		t.Error("bad duration accepted")
	}
}
