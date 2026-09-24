package main

import (
	"context"
	"flag"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/cache"
)

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
	a, err := newApp()
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
	a, err := newApp()
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
	if a, _ := newApp(); a.maxAge != cache.MaxAge {
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
		if _, err := newApp(); err == nil {
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
