//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCLIFindAllNamespaces(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	// Logged in at the root with a policy covering only the team
	// namespaces: the root has nothing to index and is skipped quietly.
	c := nsCLI(t, rootLoginToken(t, time.Hour))

	r := c.ok("find", "--all-ns", "k:ns_only_key")
	if r.stdout != nsx.Parent+"\tsecret/team/app/db\tns_only_key\n" || strings.Contains(r.stderr, "warning") {
		t.Errorf("parent: %+v", r)
	}
	if r := c.ok("find", "--all-ns", "child_key"); r.stdout != nsx.Child+"\tsecret/nested/thing\tchild_key\n" {
		t.Errorf("child: %q", r.stdout)
	}
	// The namespace is searchable, and values are read in their namespace.
	r = c.ok("find", "--all-ns", "--values", "--json", "child", "k:child_key")
	var row struct{ Namespace, Path, Key, Value string }
	if err := json.Unmarshal([]byte(r.stdout), &row); err != nil || row.Namespace != nsx.Child || row.Value != "ns-child-value" {
		t.Errorf("json: %q %v", r.stdout, err)
	}
	// Each namespace got its own cache, usable without --all-ns.
	if st := c.ok("--ns", nsx.Child, "status").stdout; !strings.Contains(st, "secrets:  1") {
		t.Errorf("child cache:\n%s", st)
	}
	// A root token sees the root namespace's fixture too.
	c2 := nsCLI(t, rootChild(t, time.Hour))
	c2.env["VAULTR_MOUNTS"] = ""
	if r := c2.ok("find", "--all-ns", "k:webhook_secret"); !strings.HasPrefix(r.stdout, "/\t"+fx.KV2+"prod/payments/stripe") {
		t.Errorf("root namespace: %q", r.stdout)
	}
}

func TestTUIAllNamespaces(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	c := nsCLI(t, rootLoginToken(t, time.Hour))
	writeConfig(t, c, `namespace = "`+nsx.Parent+`"`+"\n")
	tm := startTUI(t, c)
	tm.waitFor(0, "secret/team/app/db")

	m := tm.mark()
	tm.send(keyCtrlN)
	tm.waitFor(m, "search every namespace")
	tm.send("all")
	tm.send(keyEnter)
	tm.waitFor(m, "across all namespaces")
	m = tm.mark()
	tm.send("child_key")
	tm.waitFor(m, nsx.Child+" · secret/nested/thing")
	m = tm.mark()
	tm.send(keyEnter)
	tm.waitFor(m, "child_key")
	tm.send("r")
	tm.waitFor(m, "ns-child-value")
	tm.send(keyEsc)
	tm.send(keyCtrlC)
	tm.send(keyCtrlC)
	tm.waitExit()
}
