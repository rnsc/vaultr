//go:build integration

package integration

// Tests for the common enterprise setup: log in at the root namespace,
// then read secrets in a team namespace. Secret calls must go to the team
// namespace while the cache key lives in the token's root cubbyhole.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/testvault"
	"github.com/rnsc/vaultr/internal/vault"
)

// rootLoginToken is issued in the root namespace with a root-namespace
// policy that only grants access to the team namespaces.
func rootLoginToken(t *testing.T, ttl time.Duration) string {
	t.Helper()
	return token(t, testvault.TokenOptions{Policies: []string{nsx.RootPolicy}, TTL: ttl})
}

func TestCrossNSDetection(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	cases := []struct {
		name   string
		ns     string
		tok    string
		wantNS string
	}{
		{"root login, parent secrets", nsx.Parent, rootLoginToken(t, time.Hour), ""},
		{"root login, child secrets", nsx.Child, rootLoginToken(t, time.Hour), ""},
		{"parent login, parent secrets", nsx.Parent, nsToken(t, nsx.Parent), nsx.Parent},
		{"parent login, child secrets", nsx.Child, nsToken(t, nsx.Parent), nsx.Parent},
		{"child login, child secrets", nsx.Child, nsToken(t, nsx.Child), nsx.Child},
	}
	for _, c := range cases {
		cl := nsClient(t, c.ns, c.tok)
		got, by, err := cl.TokenNamespace(ctx(t))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.wantNS {
			t.Errorf("%s: token namespace %q (%s), want %q", c.name, got, by, c.wantNS)
		}
	}
}

func TestCrossNSIndexAndCache(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	tok := rootLoginToken(t, time.Hour)
	c := nsClient(t, nsx.Parent, tok)

	res := build(t, c, index.Options{Mounts: []vault.Mount{{Path: "secret/", KVVersion: 2}}})
	diff(t, asMap(res.Entries), expectedNS(nsx.Parent))
	if ms, err := c.KVMounts(ctx(t)); err != nil || len(ms) != 1 || ms[0].Path != "secret/" {
		t.Errorf("discovery from a root-namespace token: %+v %v", ms, err)
	}

	s := &cache.Store{Dir: t.TempDir(), Client: c}
	h, warn, err := s.Save(ctx(t), res.Entries, 0)
	if err != nil {
		t.Fatal(err)
	}
	if warn != "" || !h.Bound() || h.TokenNamespace != "" {
		t.Fatalf("cache not bound to the root cubbyhole: warn=%q header=%+v", warn, h)
	}
	// The key is in the token's root-namespace cubbyhole...
	rootSide := nsClient(t, "", tok)
	if keys, err := rootSide.List(ctx(t), "cubbyhole/vaultr"); err != nil || len(keys) != 1 || keys[0] != h.KeyRef {
		t.Errorf("root cubbyhole keys %v (%v), want [%s]", keys, err, h.KeyRef)
	}
	// ...and a fresh process (no detection yet) loads it via the header hint.
	fresh := &cache.Store{Dir: s.Dir, Client: nsClient(t, nsx.Parent, tok)}
	got, _, err := fresh.Load(ctx(t))
	if err != nil {
		t.Fatalf("Load in a new client: %v", err)
	}
	diff(t, asMap(got), expectedNS(nsx.Parent))

	// Time bomb still works across namespaces.
	if err := fx.Revoke(ctx(t), tok); err != nil {
		t.Fatal(err)
	}
	again := &cache.Store{Dir: s.Dir, Client: nsClient(t, nsx.Parent, tok)}
	mustStale(t, again, "")
	fileGone(t, again)
}

func TestCrossNSExplicitTokenNamespace(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	tok := rootLoginToken(t, time.Hour)
	root := ""
	c, err := vault.New(vault.Config{Addr: addr, Token: tok, Namespace: nsx.Parent, TokenNamespace: &root})
	if err != nil {
		t.Fatal(err)
	}
	if ns, by, _ := c.TokenNamespace(ctx(t)); ns != "" || by != "configured" {
		t.Errorf("configured token namespace: %q (%s)", ns, by)
	}
	h, warn, err := (&cache.Store{Dir: t.TempDir(), Client: c}).Save(ctx(t), nil, 0)
	if err != nil || warn != "" || !h.Bound() {
		t.Errorf("save with explicit root token namespace: %v %q %+v", err, warn, h)
	}

}

func TestCrossNSInvalidToken(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	c := nsClient(t, nsx.Parent, "hvs.not-valid")
	if _, err := c.LookupSelf(ctx(t)); !errors.Is(err, vault.ErrForbidden) {
		t.Errorf("want ErrForbidden, got %v", err)
	}
}

func TestCLICrossNS(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	tok := rootLoginToken(t, time.Hour)
	c := nsCLI(t, tok)
	writeConfig(t, c, `namespace = "`+nsx.Parent+`"`+"\n")

	if r := c.ok("find", "ns_only_key"); r.stdout != "secret/team/app/db\tns_only_key\n" || strings.Contains(r.stderr, "warning") {
		t.Errorf("find: %+v", r)
	}
	st := c.ok("status").stdout
	for _, want := range []string{"namespace: " + nsx.Parent, "token ns: (root)", "bound to token cubbyhole"} {
		if !strings.Contains(st, want) {
			t.Errorf("status missing %q:\n%s", want, st)
		}
	}
	if r := c.ok("get", "secret/team/app/db", "password"); r.stdout != "ns-parent-pass" {
		t.Errorf("get: %q", r.stdout)
	}
	if st := c.ok("config").stdout; !strings.Contains(st, "token_namespace") || !strings.Contains(st, "(auto)") {
		t.Errorf("config show:\n%s", st)
	}

	// Child namespace via env, same root token.
	c.env["VAULT_NAMESPACE"] = nsx.Child
	if r := c.ok("find", "child_key"); r.stdout != "secret/nested/thing\tchild_key\n" {
		t.Errorf("child namespace: %q", r.stdout)
	}

	// Explicit token namespace via env ("/" = root).
	c.env["VAULTR_TOKEN_NAMESPACE"] = "/"
	c.ok("index")
	if st := c.ok("status").stdout; !strings.Contains(st, "token ns: (root) (configured)") || !strings.Contains(st, "bound") {
		t.Errorf("status with VAULTR_TOKEN_NAMESPACE=/:\n%s", st)
	}

	// Revoking the root-namespace token makes every namespace's cache
	// unreadable.
	if err := fx.Revoke(ctx(t), tok); err != nil {
		t.Fatal(err)
	}
	if r := c.run("find", "child_key"); r.code != 1 || !strings.Contains(r.stderr, "expired or invalid") {
		t.Errorf("after revoke: %+v", r)
	}
}
