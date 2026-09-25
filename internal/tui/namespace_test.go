package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/vault"
)

func TestNamespaceSwitcherFiltersAndSwitches(t *testing.T) {
	fb := newFakeBackend(t)
	fb.client = fb.client.InNamespace("team-b")
	child := []index.Entry{{Path: "kv/child/only", Mount: "kv/", KV: 2, Keys: []string{"child_key"}}}
	fb.loadCache = func(context.Context) ([]index.Entry, cache.Header, error) {
		if fb.Client().Namespace == "team-a/child" {
			return child, cache.Header{}, nil
		}
		return nil, cache.Header{}, cache.ErrStale
	}
	m := newTest(t, Options{Backend: fb, Entries: testEntries})

	m = press(t, m, "ctrl+n")
	if m.mode != modeNamespace || m.ns.loading {
		t.Fatalf("mode %v loading %v", m.mode, m.ns.loading)
	}
	// "all namespaces" comes first.
	if !reflect.DeepEqual(m.ns.results, []string{allEntry, "", "team-a", "team-a/child", "team-b"}) {
		t.Errorf("namespaces: %q", m.ns.results)
	}
	if m.ns.cursor != 4 {
		t.Errorf("cursor starts on %d, want the current namespace (4)", m.ns.cursor)
	}
	v := m.View()
	for _, want := range []string{"Namespaces", "team-b  (current)", "/  root"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}

	m = typeText(t, m, "a chi")
	if !reflect.DeepEqual(m.ns.results, []string{"team-a/child"}) {
		t.Errorf("filtered: %q", m.ns.results)
	}
	m = press(t, m, "enter")
	if m.mode != modeList || fb.Client().Namespace != "team-a/child" {
		t.Fatalf("after enter: mode %v namespace %q", m.mode, fb.Client().Namespace)
	}
	if got := rowIDs(m); !reflect.DeepEqual(got, []string{"kv/child/only#child_key"}) {
		t.Errorf("rows after switching: %q", got)
	}
	if !strings.Contains(m.flash, "switched to namespace team-a/child") {
		t.Errorf("flash: %q", m.flash)
	}
	m.flash = ""
	if !strings.Contains(m.statusLine(), "ns team-a/child") {
		t.Errorf("status line does not show the new namespace: %q", m.statusLine())
	}
}

func TestNamespaceSwitcherBuildsWhenNoCache(t *testing.T) {
	fb := newFakeBackend(t)
	built := 0
	fb.build = func(context.Context, func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
		built++
		return testEntries[:1], cache.Header{}, "", nil
	}
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	m = press(t, m, "ctrl+n")
	m = typeText(t, m, "team-b")
	m = press(t, m, "enter")
	if built != 1 || len(m.results) != 2 || fb.Client().Namespace != "team-b" {
		t.Errorf("built %d, rows %q, namespace %q", built, rowIDs(m), fb.Client().Namespace)
	}
}

func TestNamespaceSwitcherKeys(t *testing.T) {
	fb := newFakeBackend(t)
	m := newTest(t, Options{Backend: fb, Entries: testEntries})

	// esc clears the filter, then goes back without switching.
	m = press(t, m, "ctrl+n")
	m = typeText(t, m, "zzz")
	if len(m.ns.results) != 0 || !strings.Contains(m.View(), "no namespace matches") {
		t.Errorf("no-match view:\n%s", m.View())
	}
	m = press(t, m, "esc")
	if m.mode != modeNamespace || m.ns.input.Value() != "" || len(m.ns.results) != 5 {
		t.Errorf("esc should clear the filter first")
	}
	m = press(t, m, "esc")
	if m.mode != modeList || len(fb.switches) != 0 {
		t.Errorf("esc: mode %v, switches %q", m.mode, fb.switches)
	}
	// ^n closes it too, even with a filter typed.
	m = press(t, m, "ctrl+n")
	m = typeText(t, m, "team")
	m = press(t, m, "ctrl+n")
	if m.mode != modeList || len(fb.switches) != 0 {
		t.Errorf("^n: mode %v, switches %q", m.mode, fb.switches)
	}

	// Switch to another one; choosing the current one again changes nothing.
	m = press(t, m, "ctrl+n", "down", "enter") // from the current (root) to team-a
	if m.mode != modeList || len(fb.switches) != 1 || fb.switches[0] != "team-a" {
		t.Errorf("switches: %q", fb.switches)
	}
	m = press(t, m, "ctrl+n", "enter")
	if len(fb.switches) != 1 {
		t.Errorf("re-selecting the current namespace switched again: %q", fb.switches)
	}
}

func TestNamespaceSwitcherErrors(t *testing.T) {
	fb := newFakeBackend(t)
	fb.nsList = func(context.Context) ([]string, error) { return nil, vault.ErrNoNamespaces }
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	m = press(t, m, "ctrl+n")
	if !strings.Contains(m.View(), "no namespaces") {
		t.Errorf("error view:\n%s", m.View())
	}
	m = press(t, m, "enter")
	if m.mode != modeNamespace || len(fb.switches) != 0 {
		t.Errorf("enter with nothing listed: mode %v switches %q", m.mode, fb.switches)
	}

	// An expired token goes to the login screen.
	fb.nsList = func(context.Context) ([]string, error) { return nil, vault.ErrTokenInvalid }
	m = press(t, m, "esc", "ctrl+n")
	if m.mode != modeLogin {
		t.Errorf("mode %v, want login", m.mode)
	}
}

func TestAllNamespacesMode(t *testing.T) {
	fb := newFakeBackend(t)
	var rebuilds []bool
	orig := fb.allNS
	fb.allNS = func(ctx context.Context, rebuild bool) ([]index.Entry, cache.Header, string, error) {
		rebuilds = append(rebuilds, rebuild)
		return orig(ctx, rebuild)
	}
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	m = press(t, m, "ctrl+n")
	m = typeText(t, m, "all")
	m = press(t, m, "enter")
	if !m.allNS || m.mode != modeList || len(m.results) != 2 || !reflect.DeepEqual(rebuilds, []bool{false}) {
		t.Fatalf("allNS %v mode %v rows %d rebuilds %v", m.allNS, m.mode, len(m.results), rebuilds)
	}
	v := m.View()
	for _, want := range []string{"team-a · secret/app/db", "team-b · secret/app/db", "all namespaces"} {
		if !strings.Contains(plain(v), want) {
			t.Errorf("view lacks %q:\n%s", want, plain(v))
		}
	}
	// The namespace narrows the search.
	m = typeText(t, m, "team-b")
	if len(m.results) != 1 || m.results[0].Entry.Namespace != "team-b" {
		t.Errorf("team-b: %d rows", len(m.results))
	}
	// ^r rebuilds every namespace.
	m = press(t, m, "ctrl+r")
	if !reflect.DeepEqual(rebuilds, []bool{false, true}) {
		t.Errorf("rebuilds %v", rebuilds)
	}
	// The switcher marks "all namespaces" as current; picking a namespace
	// leaves the mode, even the one the client was already in.
	m = press(t, m, "ctrl+n")
	if !strings.Contains(plain(m.View()), "all namespaces  search every namespace below at once  (current)") {
		t.Errorf("switcher:\n%s", plain(m.View()))
	}
	m = press(t, m, "down", "enter") // the root: the client's namespace
	if m.allNS || len(fb.switches) != 1 || fb.switches[0] != "" {
		t.Errorf("allNS %v switches %q", m.allNS, fb.switches)
	}
}

func TestAllNamespacesReadsInTheEntrysNamespace(t *testing.T) {
	var gotNS []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotNS = append(gotNS, r.Header.Get("X-Vault-Namespace"))
		_, _ = w.Write([]byte(`{"data":{"data":{"password":"from-` + r.Header.Get("X-Vault-Namespace") + `"}}}`))
	}))
	t.Cleanup(srv.Close)
	fb := newFakeBackend(t)
	fb.client, _ = vault.New(vault.Config{Addr: srv.URL, Token: "t"})
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	m = press(t, m, "ctrl+n")
	m = typeText(t, m, "all")
	m = press(t, m, "enter")
	m = typeText(t, m, "team-b")
	m = press(t, m, "enter")
	if m.mode != modeDetail || m.detail.values["password"] != "from-team-b" {
		t.Errorf("mode %v values %v (namespaces asked: %q)", m.mode, m.detail.values, gotNS)
	}
	if !strings.Contains(plain(m.View()), "team-b · secret/app/db") {
		t.Errorf("detail title:\n%s", plain(m.View()))
	}
}
