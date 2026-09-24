//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/testvault"
	"github.com/rnsc/vaultr/internal/vault"
)

func needNamespaces(t *testing.T) {
	t.Helper()
	if nsx == nil {
		t.Skip("server does not support namespaces (Vault Enterprise or OpenBao needed)")
	}
}

func nsClient(t *testing.T, ns, tok string) *vault.Client {
	t.Helper()
	c, err := vault.New(vault.Config{Addr: addr, Token: tok, Namespace: ns})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// nsToken is a token created inside ns with its reader policy.
func nsToken(t *testing.T, ns string) string {
	t.Helper()
	tok, err := nsx.Token(ctx(t), ns, testvault.TokenOptions{Policies: []string{"reader"}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func expectedNS(ns string) map[string][]string {
	out := map[string][]string{}
	for _, s := range nsx.Secrets[ns] {
		out[s.Full()] = testvault.Keys(s)
	}
	return out
}

func TestNamespaceIndex(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	for _, ns := range []string{nsx.Parent, nsx.Child, "/" + nsx.Parent + "/"} {
		c := nsClient(t, ns, os.Getenv("VAULT_TOKEN"))
		ms, err := c.KVMounts(ctx(t))
		if err != nil {
			t.Fatalf("%s: discovery: %v", ns, err)
		}
		if len(ms) != 1 || ms[0].Path != "secret/" || ms[0].KVVersion != 2 {
			t.Errorf("%s: mounts %+v, want only secret/ (v2)", ns, ms)
		}
		res := build(t, c, index.Options{Mounts: ms})
		diff(t, asMap(res.Entries), expectedNS(strings.Trim(ns, "/")))
	}
}

func TestNamespaceReadAndMountFor(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	c := nsClient(t, nsx.Child, os.Getenv("VAULT_TOKEN"))
	m, err := c.MountFor(ctx(t), "secret/nested/thing")
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.ReadSecret(ctx(t), m, "nested/thing")
	if err != nil || d["child_key"] != "ns-child-value" {
		t.Errorf("read in child namespace: %v %v", d, err)
	}
	// The same path in the root namespace does not exist.
	if _, err := root.MountFor(ctx(t), "secret/nested/thing"); err == nil {
		if _, err := root.ReadSecret(ctx(t), vault.Mount{Path: "secret/", KVVersion: 2}, "nested/thing"); err == nil {
			t.Error("namespaced secret visible from the root namespace")
		}
	}
}

func TestNamespaceCache(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	tok := nsToken(t, nsx.Parent)
	dir := t.TempDir()
	s := &cache.Store{Dir: dir, Client: nsClient(t, nsx.Parent, tok)}
	h, warn, err := s.Save(ctx(t), []index.Entry{{Path: "secret/team/app/db", Mount: "secret/", KV: 2, Keys: []string{"password"}}}, 0)
	if err != nil || warn != "" {
		t.Fatalf("Save: %v %q", err, warn)
	}
	if !h.Bound() || h.Namespace != nsx.Parent {
		t.Errorf("header %+v", h)
	}
	if _, _, err := s.Load(ctx(t)); err != nil {
		t.Fatalf("Load: %v", err)
	}
	other := &cache.Store{Dir: dir, Client: nsClient(t, nsx.Child, tok)}
	if other.File() == s.File() {
		t.Error("namespaces share a cache file")
	}
	rootStore := &cache.Store{Dir: dir, Client: nsClient(t, "", os.Getenv("VAULT_TOKEN"))}
	if rootStore.File() == s.File() {
		t.Error("namespace and root share a cache file")
	}
	// Revoking the namespaced token destroys its cubbyhole there too.
	if _, err := s.Client.Write(ctx(t), "auth/token/revoke-self", nil); err != nil {
		t.Fatal(err)
	}
	mustStale(t, s, "")
	fileGone(t, s)
}

func nsCLI(t *testing.T, tok string) *cli {
	t.Helper()
	c := newCLI(t, tok)
	delete(c.env, "VAULTR_MOUNTS")
	return c
}

func writeConfig(t *testing.T, c *cli, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c.env["VAULTR_CONFIG"] = p
}

func TestCLINamespaceFromEnv(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	c := nsCLI(t, nsToken(t, nsx.Parent))
	c.env["VAULT_NAMESPACE"] = nsx.Parent
	if r := c.ok("find", "ns_only_key"); r.stdout != "secret/team/app/db\tns_only_key\n" {
		t.Errorf("find: %q", r.stdout)
	}
	if r := c.ok("get", "secret/team/app/db", "password"); r.stdout != "ns-parent-pass" {
		t.Errorf("get: %q", r.stdout)
	}
	if st := c.ok("status").stdout; !strings.Contains(st, "namespace: "+nsx.Parent) || !strings.Contains(st, "bound to token cubbyhole") {
		t.Errorf("status:\n%s", st)
	}
	// Root-namespace fixture is not visible from here.
	if r := c.run("find", "stripe"); r.code != 1 {
		t.Errorf("root secrets leaked into namespace search: %q", r.stdout)
	}
}

func TestCLINamespaceFromConfig(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	rootTok := rootChild(t, time.Hour)
	c := nsCLI(t, rootTok)
	writeConfig(t, c, `namespace = "`+nsx.Child+`"`+"\n")

	if r := c.ok("find", "child_key"); r.stdout != "secret/nested/thing\tchild_key\n" {
		t.Errorf("namespace from config: %q", r.stdout)
	}
	if st := c.ok("config").stdout; !strings.Contains(st, nsx.Child) || !strings.Contains(st, "(config)") {
		t.Errorf("config show:\n%s", st)
	}

	// VAULT_NAMESPACE overrides the file.
	c.env["VAULT_NAMESPACE"] = nsx.Parent
	if r := c.ok("find", "k:api_key"); r.stdout != "secret/team/app/api\tapi_key\n" {
		t.Errorf("env override: %q", r.stdout)
	}
	// VAULT_NAMESPACE=/ forces the root namespace.
	c.env["VAULT_NAMESPACE"] = "/"
	c.env["VAULTR_MOUNTS"] = fx.KV2
	if r := c.ok("find", "stripe", "api"); !strings.Contains(r.stdout, fx.KV2+"prod/payments/stripe") {
		t.Errorf("root override: %q", r.stdout)
	}
}
