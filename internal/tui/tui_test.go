package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/vault"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

// fakeVault serves a few KV v2 secrets.
func fakeVault(t *testing.T, secrets map[string]string) *vault.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := secrets[strings.TrimPrefix(r.URL.Path, "/v1/")]
		switch {
		case !ok:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[]}`))
		case body == "403":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
		default:
			_, _ = w.Write([]byte(`{"data":{"data":` + body + `}}`))
		}
	}))
	t.Cleanup(srv.Close)
	c, err := vault.New(vault.Config{Addr: srv.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var testEntries = []index.Entry{
	{Path: "secret/prod/payments/stripe", Mount: "secret/", KV: 2, Keys: []string{"api_key", "webhook_secret"}},
	{Path: "secret/prod/db", Mount: "secret/", KV: 2, Keys: []string{"password", "username"}},
	{Path: "secret/denied", Mount: "secret/", KV: 2, Keys: []string{"k"}},
	{Path: "secret/stale", Mount: "secret/", KV: 2, Keys: []string{"removed_key"}},
	{Path: "secret/keyless", Mount: "secret/", KV: 2},
}

var testSecrets = map[string]string{
	"secret/data/prod/payments/stripe": `{"api_key":"sk_live_123","webhook_secret":"wh_456"}`,
	"secret/data/prod/db":              `{"password":"hunter2","username":"app"}`,
	"secret/data/denied":               "403",
	"secret/data/stale":                `{"other":"x"}`,
}

// fakeClipboard captures clipboard writes.
type fakeClipboard struct {
	mu  sync.Mutex
	val string
}

func useFakeClipboard(t *testing.T) *fakeClipboard {
	t.Helper()
	fc := &fakeClipboard{}
	w, r := writeClipboard, readClipboard
	writeClipboard = func(s string) error { fc.mu.Lock(); fc.val = s; fc.mu.Unlock(); return nil }
	readClipboard = func() (string, error) { fc.mu.Lock(); defer fc.mu.Unlock(); return fc.val, nil }
	t.Cleanup(func() { writeClipboard, readClipboard = w, r })
	return fc
}

func (fc *fakeClipboard) get() string { fc.mu.Lock(); defer fc.mu.Unlock(); return fc.val }

func newTest(t *testing.T, opt Options) model {
	t.Helper()
	if opt.Backend == nil {
		opt.Backend = newFakeBackend(t)
	}
	m := newModel(opt)
	return update(t, m, tea.WindowSizeMsg{Width: 100, Height: 20})
}

// update feeds msg and then runs the resulting commands to completion.
// Commands that don't finish quickly (timers, ticks) are dropped.
func update(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	queue := []tea.Msg{msg}
	for i := 0; len(queue) > 0; i++ {
		if i > 200 {
			t.Fatal("update loop did not settle")
		}
		msg, queue = queue[0], queue[1:]
		if _, ok := msg.(spinner.TickMsg); ok {
			continue
		}
		next, cmd := m.Update(msg)
		m = next.(model)
		queue = append(queue, runCmd(cmd)...)
	}
	return m
}

func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		if b, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range b {
				out = append(out, runCmd(c)...)
			}
			return out
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}

func keyMsg(k string) tea.KeyMsg {
	special := map[string]tea.KeyType{
		"enter": tea.KeyEnter, "esc": tea.KeyEsc, "up": tea.KeyUp, "down": tea.KeyDown,
		"pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp, "ctrl+y": tea.KeyCtrlY, "ctrl+o": tea.KeyCtrlO,
		"ctrl+r": tea.KeyCtrlR, "ctrl+c": tea.KeyCtrlC, "backspace": tea.KeyBackspace, "ctrl+n": tea.KeyCtrlN,
	}
	if kt, ok := special[k]; ok {
		return tea.KeyMsg{Type: kt}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func press(t *testing.T, m model, keys ...string) model {
	t.Helper()
	for _, k := range keys {
		m = update(t, m, keyMsg(k))
	}
	return m
}

func typeText(t *testing.T, m model, s string) model {
	t.Helper()
	for _, r := range s {
		m = update(t, m, keyMsg(string(r)))
	}
	return m
}

func rowIDs(m model) []string {
	var out []string
	for _, r := range m.results {
		out = append(out, r.Entry.Path+"#"+r.Key)
	}
	return out
}

func TestFilterAndNavigate(t *testing.T) {
	m := newTest(t, Options{Entries: testEntries})
	if len(m.results) != 7 {
		t.Fatalf("initial rows %d, want 7", len(m.results))
	}
	m = typeText(t, m, "stripe")
	if got := rowIDs(m); len(got) != 2 || got[0] != "secret/prod/payments/stripe#api_key" {
		t.Fatalf("rows %v", got)
	}
	view := plain(m.View())
	if !strings.Contains(view, "2/7") || !strings.Contains(view, "webhook_secret") || strings.Contains(view, "prod/db") {
		t.Errorf("view:\n%s", view)
	}

	m = press(t, m, "down", "down", "down")
	if m.cursor != 1 {
		t.Errorf("cursor %d, want clamped to 1", m.cursor)
	}
	m = press(t, m, "up", "up")
	if m.cursor != 0 {
		t.Errorf("cursor %d, want 0", m.cursor)
	}

	m = press(t, m, "backspace", "backspace", "backspace", "backspace", "backspace", "backspace")
	m = typeText(t, m, "k:password")
	if got := rowIDs(m); len(got) != 1 || got[0] != "secret/prod/db#password" {
		t.Errorf("k: filter rows %v", got)
	}
	m = typeText(t, m, " zzz")
	if len(m.results) != 0 || !strings.Contains(plain(m.View()), "0/7") {
		t.Error("no-match state wrong")
	}
	m = press(t, m, "enter", "ctrl+y") // no rows: must not panic
	if m.mode != modeList {
		t.Error("enter with no rows changed mode")
	}
}

func TestScrolling(t *testing.T) {
	var many []index.Entry
	for i := 0; i < 100; i++ {
		many = append(many, index.Entry{Path: "secret/s" + string(rune('a'+i%26)) + strings.Repeat("x", i), Mount: "secret/", KV: 2})
	}
	m := newTest(t, Options{Entries: many})
	h := m.listHeight()
	m = press(t, m, "pgdown", "pgdown")
	if m.cursor != 2*h || m.offset != m.cursor-h+1 {
		t.Errorf("after pgdown: cursor %d offset %d (height %d)", m.cursor, m.offset, h)
	}
	if !strings.Contains(plain(m.View()), m.results[m.cursor].Entry.Path) {
		t.Error("selected row not visible")
	}
	m = press(t, m, "pgup", "pgup", "pgup")
	if m.cursor != 0 || m.offset != 0 {
		t.Errorf("after pgup: cursor %d offset %d", m.cursor, m.offset)
	}
	if lines := strings.Count(m.View(), "\n") + 1; lines != 20 {
		t.Errorf("view has %d lines, want 20", lines)
	}
}

func TestDetailView(t *testing.T) {
	fc := useFakeClipboard(t)
	m := newTest(t, Options{Entries: testEntries})
	m = typeText(t, m, "stripe webhook")
	m = press(t, m, "enter")
	if m.mode != modeDetail || m.detail.loading {
		t.Fatalf("mode %v loading %v", m.mode, m.detail.loading)
	}
	view := plain(m.View())
	if strings.Contains(view, "sk_live_123") || strings.Contains(view, "wh_456") {
		t.Fatal("values shown before reveal")
	}
	if !strings.Contains(view, "••••••••") || !strings.Contains(view, "secret/prod/payments/stripe") {
		t.Errorf("detail view:\n%s", view)
	}
	if m.detail.keys[m.detail.cursor] != "webhook_secret" {
		t.Errorf("selected key %q, want the row's key", m.detail.keys[m.detail.cursor])
	}

	m = press(t, m, "r")
	if view := plain(m.View()); !strings.Contains(view, "sk_live_123") || !strings.Contains(view, "wh_456") {
		t.Errorf("reveal:\n%s", view)
	}
	m = press(t, m, "r")
	if strings.Contains(plain(m.View()), "wh_456") {
		t.Error("hide did not mask values")
	}

	m = press(t, m, "enter")
	if fc.get() != "wh_456" || !strings.Contains(m.flash, "copied webhook_secret") {
		t.Errorf("copy selected: clipboard %q flash %q", fc.get(), m.flash)
	}
	m = press(t, m, "up", "up", "c")
	if fc.get() != "sk_live_123" {
		t.Errorf("copy after moving up: %q", fc.get())
	}
	m = press(t, m, "y")
	if fc.get() != "secret/prod/payments/stripe" {
		t.Errorf("copy path: %q", fc.get())
	}

	m = press(t, m, "esc")
	if m.mode != modeList || m.input.Value() != "stripe webhook" || m.detail.values != nil {
		t.Errorf("back to list: mode %v query %q", m.mode, m.input.Value())
	}
}

func TestDetailErrors(t *testing.T) {
	m := newTest(t, Options{Entries: testEntries})
	m = typeText(t, m, "denied")
	m = press(t, m, "enter")
	if !strings.Contains(plain(m.View()), "permission denied") {
		t.Errorf("403 not shown:\n%s", plain(m.View()))
	}
	m = press(t, m, "c") // nothing to copy: no panic
	m = press(t, m, "q")
	if m.mode != modeList {
		t.Error("q did not go back")
	}
}

func TestStaleSecretResponseIgnored(t *testing.T) {
	m := newTest(t, Options{Entries: testEntries})
	m = typeText(t, m, "stripe")
	m = press(t, m, "enter", "esc")
	m = update(t, m, secretMsg{path: "secret/prod/payments/stripe", values: map[string]string{"x": "y"}})
	if m.mode != modeList {
		t.Error("late secret response reopened detail view")
	}
}

func TestQuickCopy(t *testing.T) {
	fc := useFakeClipboard(t)
	m := newTest(t, Options{Entries: testEntries})

	m = typeText(t, m, "k:password")
	m = press(t, m, "ctrl+y")
	if fc.get() != "hunter2" || m.mode != modeList {
		t.Errorf("ctrl+y: clipboard %q mode %v", fc.get(), m.mode)
	}
	m = press(t, m, "ctrl+o")
	if fc.get() != "secret/prod/db" {
		t.Errorf("ctrl+o: %q", fc.get())
	}

	m.input.SetValue("keyless")
	m.refresh()
	m = press(t, m, "ctrl+y")
	if !m.flashErr || !strings.Contains(m.flash, "no key") {
		t.Errorf("keyless row: flash %q", m.flash)
	}

	m.input.SetValue("removed_key")
	m.refresh()
	m = press(t, m, "ctrl+y")
	if !m.flashErr || !strings.Contains(m.flash, "no longer exists") {
		t.Errorf("removed key: flash %q", m.flash)
	}
}

func TestClipboardAutoClear(t *testing.T) {
	fc := useFakeClipboard(t)
	m := newTest(t, Options{Entries: testEntries})
	_ = writeClipboard("secret-value")
	m = update(t, m, clipClearMsg{value: "secret-value"})
	if fc.get() != "" {
		t.Error("clipboard not cleared")
	}
	_ = writeClipboard("something the user copied later")
	_ = update(t, m, clipClearMsg{value: "secret-value"})
	if fc.get() != "something the user copied later" {
		t.Error("cleared a clipboard value vaultr did not put there")
	}
}

func TestInitialBuild(t *testing.T) {
	release := make(chan struct{})
	var calls int
	build := func(ctx context.Context, p func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
		calls++
		p(index.Progress{Secrets: 7, Lists: 3, Denied: 2})
		<-release
		return testEntries, cache.Header{Expires: time.Now().Add(time.Hour), KeyRef: "x"}, "", nil
	}
	fb := newFakeBackend(t)
	fb.build = build
	m := newTest(t, Options{Backend: fb})
	if m.mode != modeBuilding {
		t.Fatal("not building without entries")
	}
	// Deliver the progress message only.
	msgs := runCmd(m.initCmd)
	for _, msg := range msgs {
		if _, ok := msg.(progressMsg); ok {
			next, _ := m.Update(msg)
			m = next.(model)
		}
	}
	view := plain(m.View())
	if !strings.Contains(view, "7 secrets") || !strings.Contains(view, "2 denied") {
		t.Errorf("building view:\n%s", view)
	}
	close(release)
	m = update(t, m, <-m.progressCh)
	if m.mode != modeList || len(m.results) != 7 || calls != 1 {
		t.Fatalf("after build: mode %v rows %d calls %d", m.mode, len(m.results), calls)
	}
	view = plain(m.View())
	if !strings.Contains(view, "indexed 5 secrets") {
		t.Errorf("flash missing:\n%s", view)
	}
	m.flash = ""
	if view := plain(m.View()); !strings.Contains(view, "cache expires in") || strings.Contains(view, "unbound") {
		t.Errorf("status line:\n%s", view)
	}
}

func TestInitialBuildFailureShowsBanner(t *testing.T) {
	fb := newFakeBackend(t)
	fb.build = func(context.Context, func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
		return nil, cache.Header{}, "", errors.New("dial tcp: connection refused")
	}
	m := newTest(t, Options{Backend: fb})
	m = settle(t, m, m.initCmd)
	view := plain(m.View())
	if m.mode != modeList || !strings.Contains(view, "Indexing failed: dial tcp: connection refused") || !strings.Contains(view, "^e to edit the config") {
		t.Errorf("mode %v, view:\n%s", m.mode, view)
	}
	// The user can go fix the config instead of being thrown out.
	m = press(t, m, "ctrl+e")
	if m.mode != modeConfig {
		t.Errorf("ctrl+e did not open the config editor")
	}
}

func TestInvalidTokenOpensLogin(t *testing.T) {
	fb := newFakeBackend(t)
	fb.build = func(context.Context, func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
		return nil, cache.Header{}, "", fmt.Errorf("%w; run `vaultr login`", vault.ErrTokenInvalid)
	}
	m := newTest(t, Options{Backend: fb})
	m = settle(t, m, m.initCmd)
	if m.mode != modeLogin || !strings.Contains(plain(m.View()), "expired or revoked") {
		t.Errorf("mode %v, view:\n%s", m.mode, plain(m.View()))
	}
}

func TestRebuild(t *testing.T) {
	fail := false
	build := func(context.Context, func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
		if fail {
			return nil, cache.Header{}, "", errors.New("vault unreachable")
		}
		return testEntries[:1], cache.Header{Expires: time.Now().Add(time.Hour)}, "cubbyhole unavailable", nil
	}
	fb := newFakeBackend(t)
	fb.build = build
	m := newTest(t, Options{Entries: testEntries, Backend: fb})
	m = press(t, m, "ctrl+r")
	if m.mode != modeList || len(m.results) != 2 {
		t.Fatalf("after rebuild: mode %v rows %d", m.mode, len(m.results))
	}
	if !m.flashErr || !strings.Contains(m.flash, "cubbyhole unavailable") {
		t.Errorf("warning not surfaced: %q", m.flash)
	}
	m.flash = ""
	if !strings.Contains(plain(m.View()), "(unbound)") {
		t.Error("unbound cache not flagged")
	}

	fail = true
	m = press(t, m, "ctrl+r")
	if m.mode != modeList || len(m.results) != 2 || !strings.Contains(m.flash, "vault unreachable") {
		t.Errorf("failed rebuild: mode %v rows %d flash %q", m.mode, len(m.results), m.flash)
	}
}

func TestCancelBuild(t *testing.T) {
	cancelled := make(chan struct{})
	fb := newFakeBackend(t)
	fb.build = func(ctx context.Context, _ func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
		<-ctx.Done()
		close(cancelled)
		return nil, cache.Header{}, "", ctx.Err()
	}
	m := newTest(t, Options{Backend: fb})
	next, cmd := m.Update(keyMsg("esc"))
	m = next.(model)
	if msgs := runCmd(cmd); len(msgs) != 0 {
		t.Errorf("esc during indexing must not quit, got %v", msgs)
	}
	if m.mode != modeList || !strings.Contains(plain(m.View()), "Indexing cancelled") {
		t.Errorf("mode %v view:\n%s", m.mode, plain(m.View()))
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("build context not cancelled")
	}

	// ctrl+c during indexing quits.
	fb.build = func(ctx context.Context, _ func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
		<-ctx.Done()
		return nil, cache.Header{}, "", ctx.Err()
	}
	m = newTest(t, Options{Backend: fb})
	if !quits(m, "ctrl+c") {
		t.Error("ctrl+c during indexing should quit")
	}
}

// quits reports whether key k makes the model quit.
func quits(m model, k string) bool {
	_, cmd := m.Update(keyMsg(k))
	for _, msg := range runCmd(cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			return true
		}
	}
	return false
}

func TestSearchKeys(t *testing.T) {
	m := newTest(t, Options{Entries: testEntries})
	m = typeText(t, m, "stripe")

	// esc clears the search and never quits.
	if quits(m, "esc") {
		t.Fatal("esc quit with a search typed")
	}
	m = press(t, m, "esc")
	if m.input.Value() != "" || len(m.results) != 7 {
		t.Errorf("esc: query %q, %d rows", m.input.Value(), len(m.results))
	}
	if quits(m, "esc") {
		t.Error("esc quit on an empty search")
	}

	// ctrl+c clears a search first, then quits.
	m = typeText(t, m, "db")
	if quits(m, "ctrl+c") {
		t.Fatal("ctrl+c quit with a search typed")
	}
	m = press(t, m, "ctrl+c")
	if m.input.Value() != "" {
		t.Errorf("ctrl+c did not clear: %q", m.input.Value())
	}
	if !quits(m, "ctrl+c") {
		t.Error("ctrl+c on an empty search should quit")
	}

	// In the secret view, esc goes back and ctrl+c quits.
	m = typeText(t, m, "stripe")
	m = press(t, m, "enter")
	if m.mode != modeDetail {
		t.Fatal("enter did not open the secret")
	}
	if !quits(m, "ctrl+c") {
		t.Error("ctrl+c in the secret view should quit")
	}
	m = press(t, m, "esc")
	if m.mode != modeList || m.input.Value() != "stripe" {
		t.Errorf("esc from the secret view: mode %v query %q", m.mode, m.input.Value())
	}
}

func TestTinyTerminal(t *testing.T) {
	m := newModel(Options{Entries: testEntries, Backend: newFakeBackend(t)})
	if m.View() != "" {
		t.Error("view before size should be empty")
	}
	for _, size := range [][2]int{{1, 1}, {10, 2}, {5, 40}} {
		m = update(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		_ = m.View()
		m = press(t, m, "down", "pgdown", "enter")
		_ = m.View()
		m = press(t, m, "esc")
	}
}

func TestHighlight(t *testing.T) {
	if got := highlightTerms("Prod k:PASS p: path:db x"); strings.Join(got, ",") != "prod,pass,db,x" {
		t.Errorf("highlightTerms: %v", got)
	}
	for _, s := range []string{"secret/prod/db", "PRODprod", "naïve/ünï", ""} {
		if got := plain(highlight(s, []string{"prod", "ï"}, sKey)); got != s {
			t.Errorf("highlight changed text: %q -> %q", s, got)
		}
	}
}

func TestNamespaceInStatusLine(t *testing.T) {
	fb := newFakeBackend(t)
	fb.client.Namespace = "team-a"
	m := newTest(t, Options{Entries: testEntries, Backend: fb})
	if !strings.Contains(plain(m.View()), "ns team-a · 7/7") {
		t.Errorf("namespace not shown:\n%s", plain(m.View()))
	}
}
