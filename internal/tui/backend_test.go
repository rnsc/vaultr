package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/rnsc/vaultr/internal/auth"
	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/config"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/vault"
)

func TestMain(m *testing.M) {
	inputCursorMode = cursor.CursorStatic
	os.Exit(m.Run())
}

// fakeBackend implements Backend with overridable behaviour.
type fakeBackend struct {
	client   *vault.Client
	settings *config.Settings

	build     func(context.Context, func(index.Progress)) ([]index.Entry, cache.Header, string, error)
	loadCache func(context.Context) ([]index.Entry, cache.Header, error)
	login     func(context.Context, auth.Request) (string, string, error)
	reload    func() error
	nsList    func(context.Context) ([]string, error)
	adopt     func(context.Context, []index.Entry, cache.Header) (cache.Header, bool, string, error)

	mu       sync.Mutex
	switches []string
	logins   []auth.Request
	reloads  int
}

func newFakeBackend(t *testing.T) *fakeBackend {
	t.Helper()
	fb := &fakeBackend{
		client: fakeVault(t, testSecrets),
		settings: &config.Settings{
			ClipClear: 45 * time.Second,
			Path:      filepath.Join(t.TempDir(), "vaultr", "config.toml"),
			Auth:      config.AuthSettings{SaveToken: true},
			Source:    map[string]string{},
		},
	}
	fb.build = func(context.Context, func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
		return testEntries, cache.Header{Expires: time.Now().Add(time.Hour), KeyRef: "k"}, "", nil
	}
	fb.loadCache = func(context.Context) ([]index.Entry, cache.Header, error) {
		return nil, cache.Header{}, cache.ErrStale
	}
	fb.login = func(_ context.Context, r auth.Request) (string, string, error) {
		fb.client.SetToken("hvs.new", r.Namespace)
		return "logged in", "", nil
	}
	fb.reload = func() error { return nil }
	fb.adopt = func(context.Context, []index.Entry, cache.Header) (cache.Header, bool, string, error) {
		return cache.Header{}, false, "", nil
	}
	fb.nsList = func(context.Context) ([]string, error) {
		return []string{"", "team-a", "team-a/child", "team-b"}, nil
	}
	return fb
}

func (f *fakeBackend) Client() *vault.Client      { return f.client }
func (f *fakeBackend) Settings() *config.Settings { return f.settings }
func (f *fakeBackend) LoadCache(ctx context.Context) ([]index.Entry, cache.Header, error) {
	return f.loadCache(ctx)
}
func (f *fakeBackend) Build(ctx context.Context, p func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
	return f.build(ctx, p)
}
func (f *fakeBackend) Login(ctx context.Context, r auth.Request) (string, string, error) {
	f.mu.Lock()
	f.logins = append(f.logins, r)
	f.mu.Unlock()
	return f.login(ctx, r)
}
func (f *fakeBackend) Adopt(ctx context.Context, e []index.Entry, h cache.Header) (cache.Header, bool, string, error) {
	return f.adopt(ctx, e, h)
}
func (f *fakeBackend) Namespaces(ctx context.Context) ([]string, error) { return f.nsList(ctx) }
func (f *fakeBackend) SwitchNamespace(ns string) {
	f.mu.Lock()
	f.switches = append(f.switches, ns)
	f.mu.Unlock()
	f.client = f.client.InNamespace(ns)
}

func (f *fakeBackend) Reload() error {
	f.mu.Lock()
	f.reloads++
	f.mu.Unlock()
	return f.reload()
}

func (f *fakeBackend) lastLogin(t *testing.T) auth.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.logins) == 0 {
		t.Fatal("no login attempted")
	}
	return f.logins[len(f.logins)-1]
}

// settle runs cmd and feeds every resulting message back through update.
func settle(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	for _, msg := range runCmd(cmd) {
		m = update(t, m, msg)
	}
	return m
}

var errBoom = errors.New("boom")
