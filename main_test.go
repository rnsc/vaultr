package main

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/cache"
)

func TestNamespaceFlag(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, c := range []struct {
		in   []string
		ns   *string
		rest []string
	}{
		{[]string{"find", "x"}, nil, []string{"find", "x"}},
		{[]string{"--ns", "team-a", "find", "x"}, str("team-a"), []string{"find", "x"}},
		{[]string{"find", "--namespace=team-a/child/", "x"}, str("team-a/child"), []string{"find", "x"}},
		{[]string{"-ns", "/", "get", "p"}, str(""), []string{"get", "p"}},
		{[]string{"-namespace=/"}, str(""), []string{}},
		{[]string{"--ns", "a", "--ns", "b"}, str("b"), []string{}},
		{[]string{"find", "--", "--ns", "x"}, nil, []string{"find", "--", "--ns", "x"}},
		{[]string{"--nsx", "y"}, nil, []string{"--nsx", "y"}},
	} {
		ns, rest, err := namespaceFlag(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if (ns == nil) != (c.ns == nil) || (ns != nil && *ns != *c.ns) || !reflect.DeepEqual(rest, c.rest) {
			t.Errorf("%q: got %v %q", c.in, ns, rest)
		}
	}
	if _, _, err := namespaceFlag([]string{"find", "--ns"}); err == nil || !strings.Contains(err.Error(), "needs a namespace") {
		t.Errorf("missing value: %v", err)
	}
}

func TestParseInterspersed(t *testing.T) {
	cases := []struct {
		args  []string
		pos   []string
		json  bool
		limit int
	}{
		{[]string{"a", "b"}, []string{"a", "b"}, false, 0},
		{[]string{"--json", "a"}, []string{"a"}, true, 0},
		{[]string{"a", "-n", "3", "b", "--json"}, []string{"a", "b"}, true, 3},
		{[]string{"a", "--", "-n"}, []string{"a", "-n"}, false, 0},
		{nil, nil, false, 0},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		j := fs.Bool("json", false, "")
		n := fs.Int("n", 0, "")
		pos, err := parseInterspersed(fs, c.args)
		if err != nil {
			t.Errorf("%v: %v", c.args, err)
			continue
		}
		if !reflect.DeepEqual(pos, c.pos) || *j != c.json || *n != c.limit {
			t.Errorf("%v: pos %v json %v n %d", c.args, pos, *j, *n)
		}
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(new(strings.Builder))
	if _, err := parseInterspersed(fs, []string{"a", "--nope"}); err == nil {
		t.Error("unknown flag accepted")
	}
}

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	base := map[string]string{
		"VAULT_ADDR": "http://127.0.0.1:1", "VAULT_TOKEN": "t", "HOME": t.TempDir(),
		"VAULTR_CACHE_DIR": t.TempDir(), "VAULTR_MAX_AGE": "", "VAULTR_WORKERS": "",
		"VAULTR_CLIP_CLEAR": "", "VAULTR_PATHS_ONLY": "", "VAULTR_MOUNTS": "", "VAULT_SKIP_VERIFY": "",
		"VAULT_URL": "", "VAULT_NAMESPACE": "", "XDG_CONFIG_HOME": "",
		"VAULTR_CONFIG": filepath.Join(t.TempDir(), "config.toml"),
	}
	for k, v := range kv {
		base[k] = v
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
}

func TestNewAppDefaults(t *testing.T) {
	setEnv(t, nil)
	a, err := newApp(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.maxAge != cache.MaxAge || a.workers != 32 || a.clipClear != 45*time.Second || a.pathsOnly || a.mounts != nil {
		t.Errorf("defaults: %+v", a)
	}
}

func TestNewAppSettings(t *testing.T) {
	setEnv(t, map[string]string{
		"VAULTR_MAX_AGE":    "30m",
		"VAULTR_WORKERS":    "4",
		"VAULTR_CLIP_CLEAR": "0",
		"VAULTR_PATHS_ONLY": "true",
		"VAULTR_MOUNTS":     " secret/ , /kv-team ,,",
	})
	a, err := newApp(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.maxAge != 30*time.Minute || a.workers != 4 || a.clipClear != 0 || !a.pathsOnly {
		t.Errorf("settings: %+v", a)
	}
	if !reflect.DeepEqual(a.mounts, []string{"secret/", "kv-team/"}) {
		t.Errorf("mounts %q", a.mounts)
	}

	setEnv(t, map[string]string{"VAULTR_MAX_AGE": "12h"})
	if a, _ := newApp(true, nil); a.maxAge != cache.MaxAge {
		t.Errorf("max age not capped: %s", a.maxAge)
	}
}

func TestNewAppInvalid(t *testing.T) {
	for k, v := range map[string]string{
		"VAULTR_MAX_AGE":    "soon",
		"VAULTR_WORKERS":    "-1",
		"VAULTR_CLIP_CLEAR": "later",
		"VAULT_SKIP_VERIFY": "maybe",
		"VAULT_TOKEN":       "",
	} {
		setEnv(t, map[string]string{k: v})
		if _, err := newApp(true, nil); err == nil {
			t.Errorf("%s=%q accepted", k, v)
		}
	}
}

func TestRunWithoutVault(t *testing.T) {
	setEnv(t, map[string]string{"VAULT_TOKEN": ""})
	for _, args := range [][]string{{"help"}, {"--help"}, {"version"}} {
		if err := run(context.Background(), args); err != nil {
			t.Errorf("%v: %v", args, err)
		}
	}
	setEnv(t, nil)
	if err := run(context.Background(), []string{"--bogus"}); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("unknown flag: %v", err)
	}
	if err := run(context.Background(), []string{"get"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("get without args: %v", err)
	}
}

func TestConfigCommand(t *testing.T) {
	setEnv(t, map[string]string{"VAULT_TOKEN": ""})
	p := os.Getenv("VAULTR_CONFIG")
	if err := run(context.Background(), []string{"config", "init"}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(p); err != nil || !strings.Contains(string(b), "namespace") {
		t.Fatalf("template not written: %v", err)
	}
	if err := run(context.Background(), []string{"config", "init"}); err == nil {
		t.Error("init overwrote an existing file")
	}
	// Works without a token.
	for _, sub := range [][]string{{"config"}, {"config", "show"}, {"config", "path"}} {
		if err := run(context.Background(), sub); err != nil {
			t.Errorf("%v: %v", sub, err)
		}
	}
	if err := run(context.Background(), []string{"config", "bogus"}); err == nil {
		t.Error("unknown subcommand accepted")
	}
	_ = os.WriteFile(p, []byte("namespace = \"team-a\"\n"), 0o600)
	setEnv(t, map[string]string{"VAULTR_CONFIG": p})
	a, err := newApp(true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.client.Namespace != "team-a" {
		t.Errorf("namespace from config not applied: %q", a.client.Namespace)
	}
}
