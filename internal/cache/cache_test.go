package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/vault"
)

// fakeVault implements lookup-self and a cubbyhole for one valid token.
type fakeVault struct {
	mu     sync.Mutex
	token  string
	expire time.Time
	now    time.Time
	cubby  map[string]json.RawMessage
	entity string
}

func (f *fakeVault) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Date", f.now.UTC().Format(http.TimeFormat))
	if r.Header.Get("X-Vault-Token") != f.token {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/v1/")
	switch {
	case p == "auth/token/lookup-self":
		exp := f.expire.Format(time.RFC3339)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"expire_time": exp, "entity_id": f.entity}})
	case strings.HasPrefix(p, "cubbyhole/"):
		switch r.Method {
		case http.MethodPost:
			var body json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.cubby[p] = body
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			delete(f.cubby, p)
			w.WriteHeader(http.StatusNoContent)
		default:
			d, ok := f.cubby[p]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"errors":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":` + string(d) + `}`))
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func setup(t *testing.T) (*fakeVault, *Store) {
	t.Helper()
	fv := &fakeVault{token: "hvs.test", now: time.Now(), expire: time.Now().Add(8 * time.Hour), cubby: map[string]json.RawMessage{}}
	srv := httptest.NewServer(fv)
	t.Cleanup(srv.Close)
	c, err := vault.New(vault.Config{Addr: srv.URL, Token: fv.token})
	if err != nil {
		t.Fatal(err)
	}
	return fv, &Store{Dir: t.TempDir(), Client: c}
}

var sample = []index.Entry{{Path: "secret/a", Mount: "secret/", KV: 2, Keys: []string{"password"}}}

func TestRoundTrip(t *testing.T) {
	_, s := setup(t)
	ctx := context.Background()
	h, warn, err := s.Save(ctx, sample, 0)
	if err != nil || warn != "" {
		t.Fatalf("save: %v %q", err, warn)
	}
	if !h.Bound() || h.Expires.Sub(h.Created) != MaxAge {
		t.Fatalf("unexpected header %+v", h)
	}
	raw, _ := os.ReadFile(s.File())
	if strings.Contains(string(raw), "password") {
		t.Fatal("key names stored in clear text")
	}
	got, _, err := s.Load(ctx)
	if err != nil || len(got) != 1 || got[0].Keys[0] != "password" {
		t.Fatalf("load: %v %+v", err, got)
	}
}

func TestExpiryCappedByToken(t *testing.T) {
	fv, s := setup(t)
	fv.expire = fv.now.Add(10 * time.Minute)
	h, _, err := s.Save(context.Background(), sample, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d := h.Expires.Sub(h.Created); d > 10*time.Minute+time.Second {
		t.Fatalf("expiry not capped by token: %s", d)
	}
}

func TestExpiredUsesServerClock(t *testing.T) {
	fv, s := setup(t)
	ctx := context.Background()
	if _, _, err := s.Save(ctx, sample, time.Hour); err != nil {
		t.Fatal(err)
	}
	fv.mu.Lock()
	fv.now = fv.now.Add(61 * time.Minute) // local clock untouched
	fv.mu.Unlock()
	if _, _, err := s.Load(ctx); !errors.Is(err, ErrStale) {
		t.Fatalf("want ErrStale, got %v", err)
	}
	if _, err := os.Stat(s.File()); !os.IsNotExist(err) {
		t.Fatal("expired cache not deleted")
	}
	if len(fv.cubby) != 0 {
		t.Fatal("cubbyhole key not deleted")
	}
}

func TestKeyGoneMeansUnreadable(t *testing.T) {
	fv, s := setup(t)
	ctx := context.Background()
	if _, _, err := s.Save(ctx, sample, 0); err != nil {
		t.Fatal(err)
	}
	fv.mu.Lock()
	fv.cubby = map[string]json.RawMessage{} // token expired: Vault wiped cubbyhole
	fv.mu.Unlock()
	if _, _, err := s.Load(ctx); !errors.Is(err, ErrStale) {
		t.Fatalf("want ErrStale, got %v", err)
	}
}

func TestOtherTokenCannotDecrypt(t *testing.T) {
	fv, s := setup(t)
	ctx := context.Background()
	if _, _, err := s.Save(ctx, sample, 0); err != nil {
		t.Fatal(err)
	}
	// Same cubbyhole contents, different token: the token is part of the key.
	fv.token = "hvs.other"
	c, _ := vault.New(vault.Config{Addr: s.Client.Addr, Token: "hvs.other"})
	s2 := &Store{Dir: s.Dir, Client: c}
	if _, _, err := s2.Load(ctx); !errors.Is(err, ErrStale) {
		t.Fatalf("want ErrStale, got %v", err)
	}
}

func TestTamperDetected(t *testing.T) {
	_, s := setup(t)
	ctx := context.Background()
	if _, _, err := s.Save(ctx, sample, 0); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.File())
	// Push the expiry in the clear header one year out.
	raw = []byte(strings.Replace(string(raw), `"expires":"20`, `"expires":"21`, 1))
	_ = os.WriteFile(s.File(), raw, 0o600)
	if _, _, err := s.Load(ctx); !errors.Is(err, ErrStale) {
		t.Fatalf("want ErrStale, got %v", err)
	}
}

func TestAdoptKeepsIndexForSameIdentity(t *testing.T) {
	fv, s := setup(t)
	ctx := context.Background()
	fv.entity = "ent-alice"
	old, _, err := s.Save(ctx, sample, 0)
	if err != nil || old.Owner == "" || strings.Contains(old.Owner, "alice") {
		t.Fatalf("owner %q (should be a hash): %v", old.Owner, err)
	}

	// Alice logs in again: a new token (and cubbyhole), the same entity.
	relogin := func(token, entity string) *Store {
		fv.mu.Lock()
		fv.token, fv.entity, fv.cubby = token, entity, map[string]json.RawMessage{}
		fv.mu.Unlock()
		c, _ := vault.New(vault.Config{Addr: s.Client.Addr, Token: token})
		return &Store{Dir: s.Dir, Client: c}
	}
	s2 := relogin("hvs.alice2", "ent-alice")
	if _, _, err := s2.Load(ctx); !errors.Is(err, ErrStale) {
		t.Fatalf("the old file should not open with the new token: %v", err)
	}
	h, ok, _, err := s2.Adopt(ctx, sample, old, 0)
	if err != nil || !ok || !h.Bound() {
		t.Fatalf("adopt: ok %v, bound %v, %v", ok, h.Bound(), err)
	}
	if got, _, err := s2.Load(ctx); err != nil || len(got) != 1 {
		t.Fatalf("load after adopt: %v %v", got, err)
	}

	// Someone else, or a token with no identity: nothing is saved.
	for _, entity := range []string{"ent-bob", ""} {
		s3 := relogin("hvs.other-"+entity, entity)
		if _, ok, _, err := s3.Adopt(ctx, sample, h, 0); ok || err != nil {
			t.Errorf("entity %q: adopted (%v, %v)", entity, ok, err)
		}
	}
	noOwner := h
	noOwner.Owner = ""
	s4 := relogin("hvs.alice3", "ent-alice")
	if _, ok, _, _ := s4.Adopt(ctx, sample, noOwner, 0); ok {
		t.Error("adopted an index with no recorded owner")
	}
}

func TestRecentPaths(t *testing.T) {
	_, s := setup(t)
	ctx := context.Background()
	h, _, err := s.Save(ctx, sample, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"secret/a", "secret/b#k", "secret/a"} {
		if err := s.AddRecent(p); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Recent(); !reflect.DeepEqual(got, []string{"secret/a", "secret/b#k"}) {
		t.Errorf("newest first, no duplicates: %q", got)
	}

	// Another store (a later run) reads them back; the key and expiry
	// didn't change.
	s2 := &Store{Dir: s.Dir, Client: s.Client}
	entries, h2, err := s2.Load(ctx)
	if err != nil || len(entries) != 1 || !reflect.DeepEqual(s2.Recent(), []string{"secret/a", "secret/b#k"}) {
		t.Fatalf("reload: %v %q", err, s2.Recent())
	}
	if h2.KeyRef != h.KeyRef || !h2.Expires.Equal(h.Expires) || !h2.Created.Equal(h.Created) {
		t.Errorf("header changed: %+v -> %+v", h, h2)
	}

	// At most MaxRecent; a rebuild keeps them.
	for i := 0; i < 15; i++ {
		_ = s2.AddRecent("secret/x" + string(rune('a'+i)))
	}
	if len(s2.Recent()) != MaxRecent {
		t.Errorf("kept %d", len(s2.Recent()))
	}
	if _, _, err := s2.Save(ctx, sample, 0); err != nil {
		t.Fatal(err)
	}
	s3 := &Store{Dir: s.Dir, Client: s.Client}
	if _, _, err := s3.Load(ctx); err != nil || len(s3.Recent()) != MaxRecent || s3.Recent()[0] != "secret/xo" {
		t.Errorf("after rebuild: %v %q", err, s3.Recent())
	}

	// Nothing loaded: nothing recorded.
	empty := &Store{Dir: t.TempDir(), Client: s.Client}
	if err := empty.AddRecent("secret/a"); err != nil || empty.Recent() != nil {
		t.Errorf("without an index: %v %q", err, empty.Recent())
	}
}

func TestOldFormatIsRebuilt(t *testing.T) {
	_, s := setup(t)
	ctx := context.Background()
	if _, _, err := s.Save(ctx, sample, 0); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.File())
	raw = bytes.Replace(raw, []byte(`"version":2`), []byte(`"version":1`), 1)
	if err := os.WriteFile(s.File(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&Store{Dir: s.Dir, Client: s.Client}).Load(ctx); !errors.Is(err, ErrStale) {
		t.Errorf("format 1 file: %v", err)
	}
}
