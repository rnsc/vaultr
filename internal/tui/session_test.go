package tui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/auth"
	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/config"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/vault"
)

// tokenVault serves testSecrets to one live token and answers "permission
// denied" to any other, like Vault after a token expires.
type tokenVault struct {
	mu   sync.Mutex
	live string
}

func (v *tokenVault) setLive(tok string) { v.mu.Lock(); v.live = tok; v.mu.Unlock() }

func newTokenVault(t *testing.T, fb *fakeBackend, live string) *tokenVault {
	t.Helper()
	tv := &tokenVault{live: live}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tv.mu.Lock()
		ok := r.Header.Get("X-Vault-Token") == tv.live
		tv.mu.Unlock()
		p := strings.TrimPrefix(r.URL.Path, "/v1/")
		body, found := testSecrets[p]
		switch {
		case !ok || body == "403":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
		case p == "auth/token/lookup-self":
			_, _ = w.Write([]byte(`{"data":{"ttl":3600,"entity_id":"e1"}}`))
		case !found:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[]}`))
		default:
			_, _ = w.Write([]byte(`{"data":{"data":` + body + `}}`))
		}
	}))
	t.Cleanup(srv.Close)
	c, err := vault.New(vault.Config{Addr: srv.URL, Token: "hvs.old"})
	if err != nil {
		t.Fatal(err)
	}
	c.HintTokenNamespace("")
	fb.client = c
	fb.login = func(_ context.Context, r auth.Request) (string, string, error) {
		tv.setLive("hvs.new")
		fb.client.SetToken("hvs.new", r.Namespace)
		return "logged in", "", nil
	}
	return tv
}

// adoptCalls records Adopt calls and answers ok.
func adoptCalls(fb *fakeBackend, ok bool) *int {
	n := 0
	fb.adopt = func(_ context.Context, e []index.Entry, prev cache.Header) (cache.Header, bool, string, error) {
		n++
		if !ok {
			return cache.Header{}, false, "", nil
		}
		return cache.Header{Expires: time.Now().Add(time.Hour), KeyRef: "k"}, true, "", nil
	}
	return &n
}

func TestResumeOpenAfterTokenExpired(t *testing.T) {
	fb := newFakeBackend(t)
	newTokenVault(t, fb, "hvs.dead") // the session's token is no longer valid
	adopts := adoptCalls(fb, true)
	built := 0
	fb.build = func(context.Context, func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
		built++
		return testEntries, cache.Header{}, "", nil
	}
	m := newTest(t, Options{Backend: fb, Entries: testEntries, Header: cache.Header{Expires: time.Now().Add(time.Hour)}})
	m = typeText(t, m, "prod db")
	m = press(t, m, "enter")
	if m.mode != modeLogin || !strings.Contains(m.View(), "continue where you were") {
		t.Fatalf("mode %v, view:\n%s", m.mode, m.View())
	}
	if m.pending == nil || m.pending.row.Entry.Path != "secret/prod/db" {
		t.Fatalf("pending: %+v", m.pending)
	}

	m = press(t, m, "enter") // log in
	if m.mode != modeDetail || m.detail.values["password"] != "hunter2" {
		t.Fatalf("after login: mode %v detail %+v", m.mode, m.detail)
	}
	if m.input.Value() != "prod db" {
		t.Errorf("search lost: %q", m.input.Value())
	}
	if *adopts != 1 || built != 0 || m.header.KeyRef != "k" {
		t.Errorf("adopt %d, builds %d, header %+v", *adopts, built, m.header)
	}
	if !strings.Contains(m.flash, "logged in") {
		t.Errorf("flash: %q", m.flash)
	}
}

func TestResumeCopyAfterTokenExpired(t *testing.T) {
	clip := useFakeClipboard(t)
	fb := newFakeBackend(t)
	newTokenVault(t, fb, "hvs.dead")
	adoptCalls(fb, false) // someone else logged in: nothing saved
	m := newTest(t, Options{Backend: fb, Entries: testEntries, Header: cache.Header{Expires: time.Now().Add(time.Hour)}})
	m = typeText(t, m, "k:password")
	m = press(t, m, "ctrl+y")
	if m.mode != modeLogin {
		t.Fatalf("mode %v", m.mode)
	}
	m = press(t, m, "enter")
	if clip.get() != "hunter2" || !strings.Contains(m.flash, "copied password") {
		t.Errorf("clipboard %q, flash %q", clip.get(), m.flash)
	}
	// The index could not be kept for this login. It stays in memory for
	// now (no rebuild over the copy's message); ^r rebuilds it.
	m.flash = ""
	if m.mode != modeList || m.header.KeyRef != "" || !strings.Contains(m.statusLine(), "^r rebuilds it") {
		t.Errorf("mode %v, status %q", m.mode, m.statusLine())
	}
}

func TestPolicyDenialIsNotALogin(t *testing.T) {
	fb := newFakeBackend(t)
	newTokenVault(t, fb, "hvs.old") // valid token; secret/denied is forbidden by policy
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	m = typeText(t, m, "denied")
	m = press(t, m, "enter")
	if m.mode != modeDetail || !errors.Is(m.detail.err, vault.ErrForbidden) || errors.Is(m.detail.err, vault.ErrTokenInvalid) {
		t.Errorf("mode %v, err %v", m.mode, m.detail.err)
	}
}

func TestEscOnLoginDropsPendingAction(t *testing.T) {
	fb := newFakeBackend(t)
	newTokenVault(t, fb, "hvs.dead")
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	m = typeText(t, m, "prod db")
	m = press(t, m, "enter", "esc")
	if m.mode != modeList || m.pending != nil {
		t.Fatalf("mode %v pending %+v", m.mode, m.pending)
	}
	// A later, unrelated login doesn't replay it.
	m = press(t, m, "ctrl+l", "enter")
	if m.mode == modeDetail {
		t.Error("dropped action ran after a later login")
	}
}

func TestAutoLoginOIDC(t *testing.T) {
	fb := newFakeBackend(t)
	fb.settings.Auth = config.AuthSettings{Method: "oidc", AutoLogin: true, SaveToken: true}
	m := newTest(t, Options{Backend: fb, Login: "No Vault token found."})
	m = settle(t, m, m.Init()) // no key pressed
	if r := fb.lastLogin(t); r.Method != auth.OIDC {
		t.Fatalf("login request %+v", r)
	}
	if m.mode != modeList || len(m.results) == 0 {
		t.Errorf("after auto-login: mode %v rows %d", m.mode, len(m.results))
	}

	// ^l is the user's choice of method: no automatic start.
	m = press(t, m, "ctrl+l")
	if n := len(fb.logins); n != 1 || m.mode != modeLogin {
		t.Errorf("^l started a login by itself (%d logins)", n)
	}
}

func TestAutoLoginNotRetriedAfterFailure(t *testing.T) {
	fb := newFakeBackend(t)
	fb.settings.Auth = config.AuthSettings{Method: "oidc", AutoLogin: true}
	fb.login = func(context.Context, auth.Request) (string, string, error) { return "", "", errBoom }
	m := newTest(t, Options{Backend: fb, Login: "No Vault token found."})
	m = settle(t, m, m.Init())
	if len(fb.logins) != 1 || m.mode != modeLogin || !strings.Contains(m.View(), "boom") {
		t.Fatalf("logins %d mode %v", len(fb.logins), m.mode)
	}
	m = press(t, m, "esc")
	m = update(t, m, builtMsg{err: vault.ErrTokenInvalid}) // the token dies again
	if len(fb.logins) != 1 || m.mode != modeLogin {
		t.Errorf("retried after a failure: %d logins", len(fb.logins))
	}
}

func TestAutoLoginWaitsForPassword(t *testing.T) {
	fb := newFakeBackend(t)
	fb.settings.Auth = config.AuthSettings{Method: "ldap", Username: "jdoe", AutoLogin: true}
	m := newTest(t, Options{Backend: fb, Login: "No Vault token found."})
	m = settle(t, m, m.Init())
	if len(fb.logins) != 0 || m.login.fields()[m.login.focus] != "password" {
		t.Errorf("logins %d, focus %q", len(fb.logins), m.login.fields()[m.login.focus])
	}
}

func TestTokenCountdown(t *testing.T) {
	m := newTest(t, Options{Entries: testEntries})
	for _, c := range []struct {
		left      time.Duration
		renewable bool
		want      string
		warn      bool
	}{
		{90 * time.Minute, false, "token 1h29m", false},
		{3 * time.Hour, true, "token 2h59m", false},
		{5 * time.Minute, false, "token expires in 4m · ^l to log in again", true},
		{5 * time.Minute, true, "token expires in 4m", true},
		{-time.Minute, false, "token expired · ^l to log in", true},
	} {
		m.token = vault.TokenInfo{ExpireTime: time.Now().Add(c.left), Renewable: c.renewable}
		m.tokenOK = true
		got, warn := m.tokenStatus()
		if got != c.want || warn != c.warn {
			t.Errorf("%s left: %q %v, want %q %v", c.left, got, warn, c.want, c.warn)
		}
		if !strings.Contains(m.statusLine(), c.want) {
			t.Errorf("status line %q lacks %q", m.statusLine(), c.want)
		}
	}
	m.token = vault.TokenInfo{} // never expires
	if got, _ := m.tokenStatus(); got != "" {
		t.Errorf("non-expiring token: %q", got)
	}
}

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second:              "<1m",
		12*time.Minute + time.Second:  "12m",
		2 * time.Hour:                 "2h",
		time.Hour + 5*time.Minute + 9: "1h5m",
	} {
		if got := shortDuration(d); got != want {
			t.Errorf("%s: %q, want %q", d, got, want)
		}
	}
}

func TestReloginWithoutPendingAction(t *testing.T) {
	for _, same := range []bool{true, false} {
		fb := newFakeBackend(t)
		newTokenVault(t, fb, "hvs.old")
		adopts := adoptCalls(fb, same)
		built := 0
		fb.build = func(context.Context, func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
			built++
			return testEntries, cache.Header{Expires: time.Now().Add(time.Hour)}, "", nil
		}
		m := newTest(t, Options{Backend: fb, Entries: testEntries, Header: cache.Header{Expires: time.Now().Add(time.Hour)}})
		m = press(t, m, "ctrl+l", "enter")
		wantBuilt := 1 // someone else: rebuilt for them
		if same {
			wantBuilt = 0 // the same person: kept
		}
		if *adopts != 1 || built != wantBuilt || m.mode != modeList {
			t.Errorf("same identity %v: adopts %d, builds %d, mode %v", same, *adopts, built, m.mode)
		}
	}
}

func TestResumeRefreshAfterTokenExpired(t *testing.T) {
	fb := newFakeBackend(t)
	newTokenVault(t, fb, "hvs.old")
	adopts := adoptCalls(fb, true)
	builds := 0
	fb.build = func(ctx context.Context, _ func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
		builds++
		if fb.Client().Token() != "hvs.new" {
			return nil, cache.Header{}, "", vault.ErrTokenInvalid
		}
		return testEntries[:2], cache.Header{Expires: time.Now().Add(time.Hour)}, "", nil
	}
	m := newTest(t, Options{Backend: fb, Entries: testEntries, Header: cache.Header{Expires: time.Now().Add(time.Hour)}})
	m = press(t, m, "ctrl+r")
	if m.mode != modeLogin || m.pending == nil || m.pending.kind != pendingBuild {
		t.Fatalf("mode %v pending %+v", m.mode, m.pending)
	}
	m = press(t, m, "enter")
	if builds != 2 || *adopts != 0 || m.mode != modeList || len(m.entries) != 2 {
		t.Errorf("builds %d, adopts %d, mode %v, entries %d", builds, *adopts, m.mode, len(m.entries))
	}
}
