package tui

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/rnsc/vaultr/internal/auth"
	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/config"
	"github.com/rnsc/vaultr/internal/index"
)

func TestStartsOnLoginWithoutToken(t *testing.T) {
	fb := newFakeBackend(t)
	fb.settings.Auth = config.AuthSettings{Method: "userpass", Username: "jdoe", SaveToken: true}
	m := newTest(t, Options{Backend: fb, Login: "No Vault token found."})
	view := plain(m.View())
	if m.mode != modeLogin || !strings.Contains(view, "No Vault token found.") {
		t.Fatalf("mode %v view:\n%s", m.mode, view)
	}
	// Defaults from the config are filled in, focus is on the password.
	if !strings.Contains(view, "[userpass]") || !strings.Contains(view, "jdoe") {
		t.Errorf("config defaults not applied:\n%s", view)
	}
	if got := m.login.fields()[m.login.focus]; got != "password" {
		t.Errorf("focus on %q, want password", got)
	}

	m = typeText(t, m, "s3cret")
	if strings.Contains(plain(m.View()), "s3cret") {
		t.Fatal("password echoed")
	}
	m = press(t, m, "enter")
	r := fb.lastLogin(t)
	if r.Method != auth.Userpass || r.Username != "jdoe" || r.Password != "s3cret" || r.Namespace != "" {
		t.Errorf("login request %+v", r)
	}
	// After login: cache is stale in the fake, so it builds and lists.
	if m.mode != modeList || len(m.results) != 7 {
		t.Errorf("after login: mode %v rows %d", m.mode, len(m.results))
	}
}

func TestLoginMethodsAndFields(t *testing.T) {
	m := newTest(t, Options{Entries: testEntries})
	m = press(t, m, "ctrl+l")
	if m.mode != modeLogin || m.login.fields()[0] != "method" {
		t.Fatalf("ctrl+l: mode %v", m.mode)
	}
	// Default method is OIDC; move to the method field and cycle.
	for m.login.focus != 0 {
		m = press(t, m, "up")
	}
	want := map[auth.Method][]string{
		auth.OIDC:     {"method", "role", "namespace", "mount"},
		auth.LDAP:     {"method", "username", "password", "namespace", "mount"},
		auth.Userpass: {"method", "username", "password", "namespace", "mount"},
		auth.Token:    {"method", "token", "namespace", "mount"},
	}
	for i := 0; i < len(auth.Methods); i++ {
		meth := auth.Methods[m.login.method]
		if got := strings.Join(m.login.fields(), ","); got != strings.Join(want[meth], ",") {
			t.Errorf("%s fields %s", meth, got)
		}
		m = press(t, m, "right")
	}
	if auth.Methods[m.login.method] != auth.OIDC {
		t.Error("method cycling does not wrap")
	}
	m = press(t, m, "left")
	if auth.Methods[m.login.method] != auth.Token {
		t.Error("left does not cycle backwards")
	}
	// esc goes back to the list, keeping the index; ctrl+c quits.
	if !quits(m, "ctrl+c") {
		t.Error("ctrl+c should quit from the login screen")
	}
	m = press(t, m, "esc")
	if m.mode != modeList || len(m.results) != 7 {
		t.Errorf("esc: mode %v rows %d", m.mode, len(m.results))
	}
}

func TestLoginTokenMethodAndNamespace(t *testing.T) {
	fb := newFakeBackend(t)
	fb.settings.Auth = config.AuthSettings{Method: "token", Namespace: "team-a", SaveToken: true}
	m := newTest(t, Options{Backend: fb, Login: "x"})
	m = typeText(t, m, "hvs.pasted")
	m = press(t, m, "enter")
	r := fb.lastLogin(t)
	if r.Method != auth.Token || r.Token != "hvs.pasted" || r.Namespace != "team-a" {
		t.Errorf("request %+v", r)
	}
	if m.mode != modeList {
		t.Errorf("after token login: mode %v", m.mode)
	}
}

func TestLoginFailureStaysAndClearsPassword(t *testing.T) {
	fb := newFakeBackend(t)
	fb.settings.Auth = config.AuthSettings{Method: "ldap", Username: "jdoe", SaveToken: true}
	fb.login = func(context.Context, auth.Request) (string, string, error) {
		return "", "", errBoom
	}
	m := newTest(t, Options{Backend: fb, Login: "x"})
	m = typeText(t, m, "wrong")
	m = press(t, m, "enter")
	if m.mode != modeLogin || !strings.Contains(plain(m.View()), "boom") {
		t.Errorf("mode %v view:\n%s", m.mode, plain(m.View()))
	}
	if m.login.inputs["password"].Value() != "" {
		t.Error("password kept after a failed login")
	}
}

func TestLoginOIDCShowsURLAndCanCancel(t *testing.T) {
	opened := make(chan string, 2)
	old := openBrowser
	openBrowser = func(u string) error { opened <- u; return nil }
	t.Cleanup(func() { openBrowser = old })

	fb := newFakeBackend(t)
	release := make(chan struct{})
	fb.login = func(ctx context.Context, r auth.Request) (string, string, error) {
		r.OpenURL("https://idp.example.com/authorize?x=1")
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-release:
			return "logged in", "VAULT_TOKEN is set", nil
		}
	}
	m := newTest(t, Options{Backend: fb, Login: "x"})
	cmd := m.submitLogin()
	for _, msg := range runCmd(cmd) {
		next, c := m.Update(msg)
		m = next.(model)
		cmd = c
	}
	if <-opened != "https://idp.example.com/authorize?x=1" {
		t.Error("browser not opened with the auth URL")
	}
	if !m.login.busy || !strings.Contains(plain(m.View()), "idp.example.com/authorize") {
		t.Errorf("URL not shown:\n%s", plain(m.View()))
	}
	// esc cancels the pending login and stays on the screen.
	m = press(t, m, "esc")
	m = settle(t, m, cmd)
	if m.mode != modeLogin || m.login.busy || m.login.err != nil {
		t.Errorf("after cancel: mode %v busy %v err %v", m.mode, m.login.busy, m.login.err)
	}

	// A successful login surfaces the warning.
	close(release)
	m = press(t, m, "enter")
	if m.mode != modeList || !m.flashErr || !strings.Contains(m.flash, "VAULT_TOKEN") {
		t.Errorf("mode %v flash %q", m.mode, m.flash)
	}
}

func TestConfigEditorCreatesFile(t *testing.T) {
	fb := newFakeBackend(t)
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	m = press(t, m, "ctrl+e")
	if m.mode != modeConfig || !strings.Contains(plain(m.View()), "(new file)") {
		t.Fatalf("mode %v view:\n%s", m.mode, plain(m.View()))
	}

	// address is first: type into it.
	m = typeText(t, m, "https://vault.example.com")
	m = press(t, m, "down") // namespace
	m = typeText(t, m, "team-a")
	// Jump to auth.method and choose ldap.
	for config.Fields[m.cfg.cursor].Key != "auth.method" {
		m = press(t, m, "down")
	}
	m = press(t, m, "right", "right") // "" -> oidc -> ldap
	for config.Fields[m.cfg.cursor].Key != "auth.save_token" {
		m = press(t, m, "down")
	}
	m = press(t, m, " ") // true -> false

	m = press(t, m, "ctrl+s")
	if m.mode != modeList || m.cfg.err != nil || !strings.Contains(m.flash, "saved "+fb.settings.Path) {
		t.Fatalf("save: mode %v err %v flash %q", m.mode, m.cfg.err, m.flash)
	}
	if fb.reloads != 1 {
		t.Errorf("backend reloaded %d times", fb.reloads)
	}
	st, err := os.Stat(fb.settings.Path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", err, st)
	}
	f, found, err := config.ReadFile(fb.settings.Path)
	if err != nil || !found {
		t.Fatal(err)
	}
	if f.Address != "https://vault.example.com" || f.Namespace != "team-a" || f.Auth.Method != "ldap" ||
		f.Auth.SaveToken == nil || *f.Auth.SaveToken {
		t.Errorf("written config %+v", f)
	}

	// Reopening shows the saved values, no longer a new file.
	m = press(t, m, "ctrl+e")
	view := plain(m.View())
	if strings.Contains(view, "(new file)") || !strings.Contains(view, "https://vault.example.com") || !strings.Contains(view, "[ldap]") {
		t.Errorf("reopened editor:\n%s", view)
	}
}

func TestConfigEditorValidationAndCancel(t *testing.T) {
	fb := newFakeBackend(t)
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	m = press(t, m, "ctrl+e")
	for config.Fields[m.cfg.cursor].Key != "max_age" {
		m = press(t, m, "down")
	}
	m = typeText(t, m, "soon")
	m = press(t, m, "ctrl+s")
	if m.mode != modeConfig || m.cfg.err == nil || !strings.Contains(plain(m.View()), "not a duration") {
		t.Errorf("invalid value accepted: mode %v err %v", m.mode, m.cfg.err)
	}
	if _, err := os.Stat(fb.settings.Path); !os.IsNotExist(err) {
		t.Error("invalid config was written")
	}
	// esc discards without writing; ctrl+c quits.
	if !quits(m, "ctrl+c") {
		t.Error("ctrl+c should quit from the editor")
	}
	m = press(t, m, "esc")
	if m.mode != modeList {
		t.Error("esc did not leave the editor")
	}
	if _, err := os.Stat(fb.settings.Path); !os.IsNotExist(err) {
		t.Error("cancelled edit was written")
	}
}

func TestConfigEditorShowsEnvOverride(t *testing.T) {
	t.Setenv("VAULT_NAMESPACE", "from-env")
	m := newTest(t, Options{Entries: testEntries})
	m = press(t, m, "ctrl+e")
	if !strings.Contains(plain(m.View()), "(overridden by VAULT_NAMESPACE)") {
		t.Errorf("env override not flagged:\n%s", plain(m.View()))
	}
}

func TestConfigEditorReloadErrorShown(t *testing.T) {
	fb := newFakeBackend(t)
	fb.reload = func() error { return errBoom }
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	m = press(t, m, "ctrl+e", "ctrl+s")
	if m.mode != modeConfig || !strings.Contains(plain(m.View()), "boom") {
		t.Errorf("reload error not shown: mode %v", m.mode)
	}
}

func TestReindexAfterSaveUsesCache(t *testing.T) {
	fb := newFakeBackend(t)
	one := []index.Entry{{Path: "kv/only", Mount: "kv/", KV: 2, Keys: []string{"k"}}}
	fb.loadCache = func(context.Context) ([]index.Entry, cache.Header, error) {
		return one, cache.Header{}, nil
	}
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	m = press(t, m, "ctrl+e", "ctrl+s")
	if m.mode != modeList || len(m.results) != 1 {
		t.Errorf("after save: mode %v rows %d", m.mode, len(m.results))
	}
}

func TestNoMatchOffersRefresh(t *testing.T) {
	fb := newFakeBackend(t)
	fresh := append([]index.Entry{{Path: "secret/just/added", Mount: "secret/", KV: 2, Keys: []string{"new_key"}}}, testEntries...)
	builds := 0
	fb.build = func(context.Context, func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
		builds++
		return fresh, cache.Header{}, "", nil
	}
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	m = typeText(t, m, "new_key")
	if len(m.results) != 0 || !strings.Contains(plain(m.View()), "Press enter or ^r to refresh") {
		t.Fatalf("no-match hint missing:\n%s", plain(m.View()))
	}
	m = press(t, m, "enter")
	if builds != 1 || len(m.results) != 1 || m.results[0].Key != "new_key" || m.input.Value() != "new_key" {
		t.Errorf("refresh on enter: builds %d rows %d query %q", builds, len(m.results), m.input.Value())
	}
	// With matches, enter opens the secret instead of refreshing.
	m = press(t, m, "enter")
	if builds != 1 || m.mode != modeDetail {
		t.Errorf("enter with a match: builds %d mode %v", builds, m.mode)
	}
}

func TestConfigEditorLongPath(t *testing.T) {
	fb := newFakeBackend(t)
	fb.settings.Path = "/var/folders/xy/" + strings.Repeat("very-long-directory-name/", 8) + "config.toml"
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	m = press(t, m, "ctrl+e")
	first := strings.SplitN(plain(m.View()), "\n", 2)[0]
	if !strings.Contains(first, "(new file)") || !strings.HasSuffix(strings.TrimSpace(first), "config.toml") || lipgloss.Width(first) > 100 {
		t.Errorf("title line %q", first)
	}
}
