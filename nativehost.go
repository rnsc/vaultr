package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rnsc/vaultr/internal/auth"
	"github.com/rnsc/vaultr/internal/browserext"
	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/config"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/nativehost"
	"github.com/rnsc/vaultr/internal/vault"
)

// Browser extension support: the browser starts vaultr as its native
// messaging host and talks to it over stdin and stdout.

// hostIdle ends the host after this long without a request. The
// extension closes the connection sooner (10 minutes); this is a backstop
// so the token doesn't stay in memory if it doesn't.
const hostIdle = 15 * time.Minute

// isNativeHost reports whether a browser started vaultr. Browsers pass
// arguments of their own, which a host manifest can't change:
//
//	Chrome family:  chrome-extension://ID/ [--parent-window=N on Windows]
//	Firefox:        /path/to/host-manifest.json vaultr@rnsc.be
//
// Nobody types these as a search query, so recognizing them is safe, and
// simpler than installing a wrapper script (a .bat on Windows).
func isNativeHost(args []string) bool {
	if len(args) >= 1 && strings.HasPrefix(args[0], "chrome-extension://") {
		return true
	}
	return len(args) >= 2 && args[1] == browserext.GeckoID && strings.HasSuffix(strings.ToLower(args[0]), ".json")
}

// nativeHost serves the extension until the browser closes the
// connection.
func nativeHost(ctx context.Context) error {
	in, out := os.Stdin, os.Stdout
	// stdout carries the protocol: anything else written there would
	// corrupt it, so stray prints go to stderr (the browser's log).
	os.Stdout = os.Stderr
	b := &hostBackend{}
	h := &nativehost.Handler{Backend: b, Version: version, Idle: hostIdle}
	return h.Serve(ctx, in, out)
}

// hostBackend is the app seen by the extension. The browser starts vaultr
// without the user's shell environment, so it works from the config file
// and ~/.vault-token; hello reloads both, so a fix or a `vaultr login` in
// a terminal is picked up without restarting the browser.
type hostBackend struct {
	mu   sync.Mutex
	a    *app
	err  error           // why the config could not be loaded
	subs map[string]*app // per namespace, keeping each one's loaded index
	// A token from a login in this process when it isn't saved to
	// ~/.vault-token (auth.save_token = false): it lasts as long as the
	// process.
	loginToken, loginNS string
}

func (b *hostBackend) Reload() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = nil
	a, err := newApp(false, nil)
	if err != nil {
		b.a, b.err = nil, err
		return
	}
	if b.loginToken != "" && !a.settings.Auth.SaveToken {
		a.client.SetToken(b.loginToken, b.loginNS)
	}
	b.a, b.err = a, nil
}

func (b *hostBackend) app() (*app, error) {
	b.mu.Lock()
	loaded := b.a != nil || b.err != nil
	b.mu.Unlock()
	if !loaded {
		b.Reload() // a request before hello, as in tests
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.a == nil {
		return nil, b.err
	}
	return b.a, nil
}

// sub is the app for namespace ns.
func (b *hostBackend) sub(ns string) (*app, error) {
	a, err := b.app()
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.subs[ns]; ok {
		return s, nil
	}
	if b.subs == nil {
		b.subs = map[string]*app{}
	}
	s := a.inNamespace(ns, a.workers)
	b.subs[ns] = s
	return s, nil
}

func (b *hostBackend) Info() nativehost.Info {
	a, err := b.app()
	if err != nil {
		return nativehost.Info{ConfigPath: config.DefaultPath(), ConfigError: err.Error()}
	}
	s := a.settings
	return nativehost.Info{
		ConfigPath:  s.Path,
		ConfigFound: s.Found,
		Addr:        s.Vault.Addr,
		AddrSource:  s.Source["address"],
		Namespace:   s.Vault.Namespace,
		AuthMethod:  s.Auth.Method,
	}
}

func (b *hostBackend) Status(ctx context.Context) (nativehost.Status, error) {
	a, err := b.app()
	if err != nil {
		return nativehost.Status{}, err
	}
	st := nativehost.Status{AuthMethod: a.settings.Auth.Method}
	if a.client.Token() == "" {
		st.Reason = "none"
		return st, nil
	}
	ti, err := a.client.LookupSelf(ctx)
	if errors.Is(err, vault.ErrTokenInvalid) {
		st.Reason = "expired"
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.LoggedIn = true
	st.TokenNamespace = ti.Namespace
	if !ti.ExpireTime.IsZero() {
		st.TTL = int64(time.Until(ti.ExpireTime).Seconds())
	}
	return st, nil
}

func (b *hostBackend) Namespaces(ctx context.Context) ([]string, error) {
	a, err := b.app()
	if err != nil {
		return nil, err
	}
	return a.Namespaces(ctx)
}

func (b *hostBackend) Index(ctx context.Context, ns string, rebuild bool, onProgress func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
	s, err := b.sub(ns)
	if err != nil {
		return nil, cache.Header{}, "", err
	}
	if s.client.Token() == "" {
		return nil, cache.Header{}, "", nativehost.ErrNotLoggedIn
	}
	if !rebuild {
		entries, h, err := s.store.Load(ctx)
		if err == nil {
			return entries, h, "", nil
		}
		if !errors.Is(err, cache.ErrStale) {
			return nil, h, "", err
		}
	}
	return s.buildIndex(ctx, onProgress)
}

func (b *hostBackend) AllNamespaces(ctx context.Context, rebuild bool, onProgress func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
	a, err := b.app()
	if err != nil {
		return nil, cache.Header{}, "", err
	}
	if a.client.Token() == "" {
		return nil, cache.Header{}, "", nativehost.ErrNotLoggedIn
	}
	return a.AllNamespaces(ctx, rebuild, onProgress)
}

func (b *hostBackend) Value(ctx context.Context, ns, path, key string) (string, error) {
	s, err := b.sub(ns)
	if err != nil {
		return "", err
	}
	m, rel, err := s.mountAndRel(ctx, path)
	if err != nil {
		return "", err
	}
	data, err := s.client.ReadSecret(ctx, m, rel)
	if err != nil {
		return "", err
	}
	v, ok := vault.Stringify(data)[key]
	if !ok {
		return "", fmt.Errorf("%s has no key %q: %w", path, key, vault.ErrNotFound)
	}
	return v, nil
}

func (b *hostBackend) Login(ctx context.Context, method, token string, openURL func(string)) (string, string, error) {
	a, err := b.app()
	if err != nil {
		return "", "", err
	}
	r := LoginRequest(a.settings.Auth)
	switch method {
	case "oidc":
		r.Method = auth.OIDC
		r.OpenURL = func(u string) {
			openURL(u)
			_ = auth.OpenBrowser(u)
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	case "token":
		r.Method, r.Token = auth.Token, token
	default:
		return "", "", fmt.Errorf("%w: %s", nativehost.ErrUseTerminal, method)
	}
	summary, warn, err := a.Login(ctx, r)
	if err != nil {
		return "", "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = nil // their clients have the old token
	if !a.settings.Auth.SaveToken {
		ns, _, _ := a.client.KnownTokenNamespace()
		b.loginToken, b.loginNS = a.client.Token(), ns
	}
	return summary, warn, nil
}

func (b *hostBackend) Recent(ns string) []string {
	s, err := b.sub(ns)
	if err != nil {
		return nil
	}
	return s.store.Recent()
}

func (b *hostBackend) AddRecent(ns, item string) {
	if s, err := b.sub(ns); err == nil {
		_ = s.store.AddRecent(item)
	}
}
