//go:build integration

package integration

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/testvault"
	"github.com/rnsc/vaultr/internal/vault"
)

func store(t *testing.T, tok string) *cache.Store {
	t.Helper()
	return &cache.Store{Dir: t.TempDir(), Client: client(t, tok)}
}

func entries(t *testing.T) []index.Entry {
	t.Helper()
	return build(t, root, index.Options{}).Entries
}

// cubbyKeys lists vaultr's data keys in the token's cubbyhole.
func cubbyKeys(t *testing.T, c *vault.Client) []string {
	t.Helper()
	keys, err := c.List(ctx(t), "cubbyhole/vaultr")
	if errors.Is(err, vault.ErrNotFound) {
		return nil
	}
	if err != nil {
		t.Fatalf("listing cubbyhole: %v", err)
	}
	return keys
}

func mustStale(t *testing.T, s *cache.Store, reason string) {
	t.Helper()
	_, _, err := s.Load(ctx(t))
	if !errors.Is(err, cache.ErrStale) {
		t.Fatalf("Load: want ErrStale, got %v", err)
	}
	if reason != "" && !strings.Contains(err.Error(), reason) {
		t.Errorf("Load: error %q should mention %q", err, reason)
	}
}

func fileGone(t *testing.T, s *cache.Store) {
	t.Helper()
	if _, err := os.Stat(s.File()); !os.IsNotExist(err) {
		t.Errorf("cache file still exists (%v)", err)
	}
}

func TestCacheRoundTrip(t *testing.T) {
	t.Parallel()
	s := store(t, rootChild(t, time.Hour))
	want := entries(t)
	h, warn, err := s.Save(ctx(t), want, 0)
	if err != nil || warn != "" {
		t.Fatalf("Save: %v (warning %q)", err, warn)
	}
	if !h.Bound() {
		t.Fatal("cache not bound to cubbyhole")
	}
	if got := cubbyKeys(t, s.Client); len(got) != 1 || got[0] != h.KeyRef {
		t.Errorf("cubbyhole keys %v, want [%s]", got, h.KeyRef)
	}

	st, err := os.Stat(s.File())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("cache mode %v, want 0600", st.Mode().Perm())
	}
	raw, _ := os.ReadFile(s.File())
	for _, leak := range []string{"stripe", "api_key", "client_secret", "legacy", fx.Prefix, s.Client.Token()} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("cache file contains %q in clear text", leak)
		}
	}

	got, h2, err := s.Load(ctx(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if h2.KeyRef != h.KeyRef || !h2.Expires.Equal(h.Expires) {
		t.Errorf("header changed: %+v vs %+v", h2, h)
	}
	diff(t, asMap(got), asMap(want))
}

func TestCacheExpiryCappedAtTwoHours(t *testing.T) {
	t.Parallel()
	s := store(t, rootChild(t, 24*time.Hour))
	for _, age := range []time.Duration{0, 5 * time.Hour, -time.Minute} {
		h, _, err := s.Save(ctx(t), nil, age)
		if err != nil {
			t.Fatal(err)
		}
		if d := h.Expires.Sub(h.Created); d != cache.MaxAge {
			t.Errorf("maxAge %s: lifetime %s, want %s", age, d, cache.MaxAge)
		}
	}
	h, _, _ := s.Save(ctx(t), nil, 30*time.Minute)
	if d := h.Expires.Sub(h.Created); d != 30*time.Minute {
		t.Errorf("lifetime %s, want 30m", d)
	}
}

func TestCacheExpiryCappedByToken(t *testing.T) {
	t.Parallel()
	s := store(t, rootChild(t, 10*time.Minute))
	h, _, err := s.Save(ctx(t), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d := h.Expires.Sub(h.Created); d > 10*time.Minute+2*time.Second || d < 9*time.Minute {
		t.Errorf("lifetime %s, want about 10m (token TTL)", d)
	}
}

func TestCacheRebuildRotatesKey(t *testing.T) {
	t.Parallel()
	s := store(t, rootChild(t, time.Hour))
	h1, _, err := s.Save(ctx(t), entries(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	h2, _, err := s.Save(ctx(t), entries(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	if h1.KeyRef == h2.KeyRef || string(h1.Salt) == string(h2.Salt) {
		t.Error("key or salt reused across rebuilds")
	}
	if got := cubbyKeys(t, s.Client); len(got) != 1 || got[0] != h2.KeyRef {
		t.Errorf("cubbyhole keys %v, want only [%s]", got, h2.KeyRef)
	}
	if _, _, err := s.Load(ctx(t)); err != nil {
		t.Errorf("Load after rebuild: %v", err)
	}
}

func TestCacheMaxAgeTimeBomb(t *testing.T) {
	t.Parallel()
	s := store(t, rootChild(t, time.Hour))
	h, _, err := s.Save(ctx(t), entries(t), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Load(ctx(t)); err != nil {
		t.Fatalf("Load before expiry: %v", err)
	}
	time.Sleep(time.Until(h.Expires) + 1500*time.Millisecond)
	mustStale(t, s, "expired")
	fileGone(t, s)
	if got := cubbyKeys(t, s.Client); len(got) != 0 {
		t.Errorf("expired data key not deleted from cubbyhole: %v", got)
	}
}

func TestCacheDiesWithToken(t *testing.T) {
	t.Parallel()
	tok := rootChild(t, 3*time.Second)
	s := store(t, tok)
	h, _, err := s.Save(ctx(t), entries(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	stolen, _ := os.ReadFile(s.File())
	time.Sleep(time.Until(h.Expires) + 2*time.Second)

	mustStale(t, s, "")
	fileGone(t, s)

	// A copy of the file plus the old token is useless: Vault destroyed
	// the cubbyhole along with the token.
	if err := os.WriteFile(s.File(), stolen, 0o600); err != nil {
		t.Fatal(err)
	}
	mustStale(t, s, "")
}

func TestCacheDiesWithRevocation(t *testing.T) {
	t.Parallel()
	tok := rootChild(t, time.Hour)
	s := store(t, tok)
	if _, _, err := s.Save(ctx(t), entries(t), 0); err != nil {
		t.Fatal(err)
	}
	stolen, _ := os.ReadFile(s.File())
	if err := fx.Revoke(ctx(t), tok); err != nil {
		t.Fatal(err)
	}
	mustStale(t, s, "")
	fileGone(t, s)
	_ = os.WriteFile(s.File(), stolen, 0o600)
	mustStale(t, s, "")
}

func TestCacheOtherTokenCannotRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := &cache.Store{Dir: dir, Client: client(t, rootChild(t, time.Hour))}
	b := &cache.Store{Dir: dir, Client: client(t, rootChild(t, time.Hour))}
	if a.File() != b.File() {
		t.Fatal("same server should map to the same file")
	}
	if _, _, err := a.Save(ctx(t), entries(t), 0); err != nil {
		t.Fatal(err)
	}
	mustStale(t, b, "")
	fileGone(t, b)
	// B rebuilding works and A can no longer read B's cache.
	if _, _, err := b.Save(ctx(t), entries(t), 0); err != nil {
		t.Fatal(err)
	}
	mustStale(t, a, "")
}

func TestCacheTamperedHeader(t *testing.T) {
	t.Parallel()
	s := store(t, rootChild(t, time.Hour))
	if _, _, err := s.Save(ctx(t), entries(t), 0); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.File())
	// Try to extend the lifetime by a century.
	forged := strings.Replace(string(raw), `"expires":"20`, `"expires":"21`, 1)
	if forged == string(raw) {
		t.Fatal("expiry field not found in header")
	}
	_ = os.WriteFile(s.File(), []byte(forged), 0o600)
	mustStale(t, s, "decrypted")
	fileGone(t, s)
}

func TestCacheCorruptFile(t *testing.T) {
	t.Parallel()
	s := store(t, rootChild(t, time.Hour))
	for name, data := range map[string][]byte{
		"garbage":   []byte("not a cache"),
		"empty":     {},
		"truncated": []byte("VLTRC1\n\x00\x00\x00\x02{}"),
	} {
		_ = os.MkdirAll(s.Dir, 0o700)
		_ = os.WriteFile(s.File(), data, 0o600)
		_, _, err := s.Load(ctx(t))
		if !errors.Is(err, cache.ErrStale) {
			t.Errorf("%s: want ErrStale, got %v", name, err)
		}
		fileGone(t, s)
	}
}

func TestCacheWithoutCubbyhole(t *testing.T) {
	t.Parallel()
	tok := token(t, testvault.TokenOptions{Policies: []string{fx.NoCubby}, NoDefaultPolicy: true, TTL: time.Hour})
	s := store(t, tok)
	h, warn, err := s.Save(ctx(t), entries(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	if h.Bound() || !strings.Contains(warn, "cubbyhole") {
		t.Errorf("want unbound cache with a warning, got bound=%v warning=%q", h.Bound(), warn)
	}
	if _, _, err := s.Load(ctx(t)); err != nil {
		t.Errorf("Load: %v", err)
	}
	// Still bound to the token itself.
	other := &cache.Store{Dir: s.Dir, Client: client(t, rootChild(t, time.Hour))}
	mustStale(t, other, "decrypted")
}

func TestCachePurge(t *testing.T) {
	t.Parallel()
	s := store(t, rootChild(t, time.Hour))
	if _, _, err := s.Save(ctx(t), entries(t), 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge(ctx(t)); err != nil {
		t.Fatal(err)
	}
	fileGone(t, s)
	if got := cubbyKeys(t, s.Client); len(got) != 0 {
		t.Errorf("purge left cubbyhole keys %v", got)
	}
	if err := s.Purge(ctx(t)); err != nil {
		t.Errorf("purging nothing: %v", err)
	}
	mustStale(t, s, "no cache")
}
