package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/vault"
)

// versionedVault serves secret/app with versions 1..3, version 2 deleted.
func versionedVault(t *testing.T, fb *fakeBackend) {
	t.Helper()
	day := time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339)
	hour := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/secret/metadata/app":
			_, _ = w.Write([]byte(`{"data":{"current_version":3,"versions":{
				"1":{"created_time":"` + day + `","deletion_time":"","destroyed":false},
				"2":{"created_time":"` + day + `","deletion_time":"` + hour + `","destroyed":false},
				"3":{"created_time":"` + hour + `","deletion_time":"","destroyed":false}}}}`))
		case "/v1/secret/data/app":
			v := r.URL.Query().Get("version")
			if v == "" {
				v = "3"
			}
			if v == "2" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"data":{"data":null}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"data":{"password":"pass-v` + v + `"}}}`))
		case "/v1/kv/legacy":
			_, _ = w.Write([]byte(`{"data":{"password":"kv1"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	fb.client, _ = vault.New(vault.Config{Addr: srv.URL, Token: "t"})
}

var versionedEntries = []index.Entry{
	{Path: "secret/app", Mount: "secret/", KV: 2, Keys: []string{"password"}},
	{Path: "kv/legacy", Mount: "kv/", KV: 1, Keys: []string{"password"}},
}

func TestSecretViewVersions(t *testing.T) {
	clip := useFakeClipboard(t)
	fb := newFakeBackend(t)
	versionedVault(t, fb)
	m := newTest(t, Options{Backend: fb, Entries: versionedEntries})
	m = typeText(t, m, "secret/app")
	m = press(t, m, "enter")
	view := func() string { return plain(m.View()) }
	if m.detail.values["password"] != "pass-v3" || !strings.Contains(view(), "version 3 of 3 (current) · written 2h ago") {
		t.Fatalf("current:\n%s", view())
	}
	if !strings.Contains(view(), "[ ] versions") {
		t.Errorf("help lacks the version keys:\n%s", view())
	}

	m = press(t, m, "[") // version 2: deleted
	if m.detail.version != 2 || m.detail.err == nil || !strings.Contains(view(), "version 2 of 3 (deleted 2h ago)") {
		t.Errorf("deleted version:\n%s", view())
	}
	m = press(t, m, "[") // version 1: older, readable
	if m.detail.values["password"] != "pass-v1" || !strings.Contains(view(), "version 1 of 3 (older) · written on ") {
		t.Errorf("version 1:\n%s", view())
	}
	m = press(t, m, "c") // copies the version shown
	if clip.get() != "pass-v1" {
		t.Errorf("copied %q", clip.get())
	}
	m = press(t, m, "[")
	if !strings.Contains(m.flash, "oldest") {
		t.Errorf("flash %q", m.flash)
	}
	m = press(t, m, "]", "]") // back to 2, then the current one
	if m.detail.version != 0 || m.detail.values["password"] != "pass-v3" {
		t.Errorf("back to current: version %d values %v", m.detail.version, m.detail.values)
	}
	m = press(t, m, "]")
	if !strings.Contains(m.flash, "current version") {
		t.Errorf("flash %q", m.flash)
	}

	// KV v1: no versions, no version line.
	m = press(t, m, "esc", "ctrl+c")
	m = typeText(t, m, "kv/legacy")
	m = press(t, m, "enter")
	m.flash = "" // the "current version" message from above
	if m.detail.meta != nil || strings.Contains(view(), "version") || m.detail.values["password"] != "kv1" {
		t.Errorf("kv1:\n%s", view())
	}
	m = press(t, m, "[")
	if m.detail.loading || m.detail.version != 0 {
		t.Error("[ did something on a KV v1 secret")
	}
}

func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "just now",
		5 * time.Minute:  "5m ago",
		30 * time.Hour:   "30h ago",
	} {
		if got := ago(time.Now().Add(-d)); got != want {
			t.Errorf("%s: %q, want %q", d, got, want)
		}
	}
	if got := ago(time.Now().Add(-100 * time.Hour)); !strings.HasPrefix(got, "on 20") {
		t.Errorf("days ago: %q", got)
	}
}

func TestOpenInVaultUI(t *testing.T) {
	var opened string
	orig := openBrowser
	openBrowser = func(u string) error { opened = u; return nil }
	t.Cleanup(func() { openBrowser = orig })
	fb := newFakeBackend(t)
	versionedVault(t, fb)
	fb.client = fb.client.InNamespace("team-a")
	m := newTest(t, Options{Backend: fb, Entries: versionedEntries})
	m = typeText(t, m, "secret/app")
	m = press(t, m, "enter", "o")
	if !strings.HasSuffix(opened, "/ui/vault/secrets/secret/show/app?namespace=team-a") || !strings.Contains(m.flash, "opened in the browser") {
		t.Errorf("opened %q, flash %q", opened, m.flash)
	}
}
