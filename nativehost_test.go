package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rnsc/vaultr/internal/browserext"
	"github.com/rnsc/vaultr/internal/nativehost"
)

func TestIsNativeHost(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"chrome-extension://" + browserext.ChromeID + "/"}, true},
		{[]string{"chrome-extension://" + browserext.ChromeID + "/", "--parent-window=1234"}, true},
		{[]string{"/home/u/.mozilla/native-messaging-hosts/be.rnsc.vaultr.json", "vaultr@rnsc.be"}, true},
		{[]string{`C:\Users\u\AppData\Local\vaultr\NativeMessagingHosts\firefox\be.rnsc.vaultr.JSON`, "vaultr@rnsc.be"}, true},
		{nil, false},
		{[]string{"find", "chrome-extension://x"}, false},
		{[]string{"vaultr@rnsc.be"}, false},
		{[]string{"config.json", "other@example.com"}, false},
		{[]string{"prod", "vaultr@rnsc.be"}, false},
	} {
		if got := isNativeHost(tc.args); got != tc.want {
			t.Errorf("%q: %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestHostBackendConfig(t *testing.T) {
	// As the browser starts it: no VAULT_ADDR, no config file.
	setEnv(t, map[string]string{"VAULT_ADDR": "", "VAULT_TOKEN": ""})
	b := &hostBackend{}
	info := b.Info()
	if info.AddrSource != "default" || info.ConfigFound || info.ConfigError != "" {
		t.Errorf("no config: %+v", info)
	}
	p := os.Getenv("VAULTR_CONFIG")
	if err := os.WriteFile(p, []byte("address = \"https://vault.example\"\nnamespace = \"team-a\"\n[auth]\nmethod = \"ldap\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.Reload()
	info = b.Info()
	if info.AddrSource != "config" || info.Addr != "https://vault.example" || info.Namespace != "team-a" || info.AuthMethod != "ldap" {
		t.Errorf("with config: %+v", info)
	}
	st, err := b.Status(context.Background())
	if err != nil || st.LoggedIn || st.Reason != "none" || st.AuthMethod != "ldap" {
		t.Errorf("no token: %+v, %v", st, err)
	}
	if _, _, _, err := b.Index(context.Background(), "team-a", false, nil); !errors.Is(err, nativehost.ErrNotLoggedIn) {
		t.Errorf("index without a token: %v", err)
	}
	if _, _, err := b.Login(context.Background(), "ldap", "", nil); !errors.Is(err, nativehost.ErrUseTerminal) {
		t.Errorf("ldap login: %v", err)
	}
	if err := os.WriteFile(p, []byte("adress = \"typo\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.Reload()
	if info := b.Info(); !strings.Contains(info.ConfigError, "adress") {
		t.Errorf("bad config: %+v", info)
	}
}

func installEnv(t *testing.T) string {
	t.Helper()
	setEnv(t, map[string]string{"VAULT_TOKEN": ""})
	home := os.Getenv("HOME")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	// Never the real clipboard.
	orig := copyPath
	copyPath = func(string) error { return errors.New("no clipboard in tests") }
	t.Cleanup(func() { copyPath = orig })
	exe := filepath.Join(home, "bin", "vaultr")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestExtensionCommand(t *testing.T) {
	exe := installEnv(t)
	var out bytes.Buffer
	if err := extensionCmd([]string{"install", "--browser", "chromium", "--path", exe}, &out); err != nil {
		t.Fatal(err)
	}
	env, _ := browserext.CurrentEnv(runtime.GOOS)
	hm, err := os.ReadFile(env.HostManifestPath(browserext.Chromium))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hm), filepath.ToSlash(exe)) && !strings.Contains(string(hm), strings.ReplaceAll(exe, `\`, `\\`)) {
		t.Errorf("host manifest doesn't point to %s: %s", exe, hm)
	}
	s := out.String()
	for _, want := range []string{"Load unpacked", env.ExtensionDir(browserext.Chromium), "It starts: " + exe,
		"VAULT_ADDR set in this shell, but the browser starts vaultr without your shell's environment"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
	if _, err := os.Stat(filepath.Join(env.ExtensionDir(browserext.Chromium), "manifest.json")); err != nil {
		t.Error(err)
	}
	out.Reset()
	if err := extensionCmd([]string{"uninstall", "--browser", "chromium"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env.HostManifestPath(browserext.Chromium)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("host manifest left after uninstall: %v", err)
	}

	for _, args := range [][]string{nil, {"install"}, {"install", "--browser", "safari"}, {"frobnicate"}, {"install", "--browser", "chrome", "extra"}} {
		if err := extensionCmd(args, &out); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
}

func TestExtensionExport(t *testing.T) {
	installEnv(t)
	dir := filepath.Join(t.TempDir(), "firefox")
	if err := extensionCmd([]string{"export", "--browser", "firefox", "--version", "v2.3.4", dir}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	m, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(m), `"version": "2.3.4"`) || strings.Contains(string(m), `"key"`) || !strings.Contains(string(m), browserext.GeckoID) {
		t.Errorf("exported manifest:\n%s", m)
	}
}

func TestConfigWarnings(t *testing.T) {
	setEnv(t, map[string]string{"VAULT_ADDR": "", "VAULT_TOKEN": ""})
	if w := strings.Join(configWarnings(), "\n"); !strings.Contains(w, "no Vault address configured") {
		t.Errorf("no address: %q", w)
	}
	setEnv(t, map[string]string{"VAULT_NAMESPACE": "team-a", "VAULTR_MOUNTS": "secret", "VAULTR_CACHE_DIR": ""})
	w := strings.Join(configWarnings(), "\n")
	for _, want := range []string{"VAULTR_MOUNTS, VAULT_ADDR, VAULT_NAMESPACE set in this shell", "the browser doesn't see VAULT_TOKEN", "VAULTR_CONFIG is set"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings lack %q:\n%s", want, w)
		}
	}
	if os.Getenv("VAULTR_CONFIG") == "" {
		t.Error("configWarnings unset VAULTR_CONFIG")
	}
}

// Reload reports a change only when the server, namespace or token does,
// so reopening the popup keeps what is loaded.
func TestHostBackendReloadChanges(t *testing.T) {
	setEnv(t, map[string]string{"VAULT_ADDR": "", "VAULT_TOKEN": ""})
	home := os.Getenv("HOME")
	cfg := os.Getenv("VAULTR_CONFIG")
	write := func(p, body string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(cfg, "address = \"https://vault.example\"\n")
	write(filepath.Join(home, ".vault-token"), "hvs.one")
	b := &hostBackend{}
	if !b.Reload() {
		t.Error("first load: no change reported")
	}
	if b.Reload() {
		t.Error("nothing changed, but Reload says it did")
	}
	write(cfg, "address = \"https://vault.example\"\n[auth]\nmethod = \"oidc\"\n")
	if b.Reload() || b.Info().AuthMethod != "oidc" {
		t.Errorf("an [auth] change: reported as a change, or not picked up (%q)", b.Info().AuthMethod)
	}
	write(filepath.Join(home, ".vault-token"), "hvs.two")
	if !b.Reload() {
		t.Error("new token: no change reported")
	}
	write(cfg, "address = \"https://other.example\"\n")
	if !b.Reload() {
		t.Error("new address: no change reported")
	}
	write(cfg, "namespace = \"team-a\"\naddress = \"https://other.example\"\n")
	if !b.Reload() {
		t.Error("new namespace: no change reported")
	}
}
