//go:build integration

package integration

// Tests for switching namespaces: listing them, --ns on the command line
// (and its completion), and the TUI switcher (^n).

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestListNamespacesByToken(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	cases := []struct {
		name     string
		tok      string
		want     []string
		wantNot  []string
		wantOnly bool
	}{
		{"root policy", rootChild(t, time.Hour), []string{"", nsx.Parent, nsx.Child}, nil, false},
		{"root login, team policy", rootLoginToken(t, time.Hour), []string{"", nsx.Parent, nsx.Child}, nil, false},
		// Its policy covers the parent's secret/ only: the child is hidden.
		{"parent token", nsToken(t, nsx.Parent), []string{nsx.Parent}, []string{"", nsx.Child}, true},
		{"child token", nsToken(t, nsx.Child), []string{nsx.Child}, []string{"", nsx.Parent}, true},
	}
	for _, c := range cases {
		got, err := nsClient(t, "", c.tok).ListNamespaces(ctx(t))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		for _, w := range c.want {
			if !slices.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", c.name, got, w)
			}
		}
		for _, w := range c.wantNot {
			if slices.Contains(got, w) {
				t.Errorf("%s: %q includes %q", c.name, got, w)
			}
		}
		if c.wantOnly && len(got) != len(c.want) {
			t.Errorf("%s: %q, want exactly %q", c.name, got, c.want)
		}
	}
}

func TestCLINamespaceFlag(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	c := nsCLI(t, rootLoginToken(t, time.Hour))
	c.env["VAULT_NAMESPACE"] = nsx.Parent // the flag wins over the environment

	if r := c.ok("find", "--ns", nsx.Child, "child_key"); r.stdout != "secret/nested/thing\tchild_key\n" {
		t.Errorf("find --ns: %q", r.stdout)
	}
	if r := c.ok("--namespace="+nsx.Child+"/", "get", "secret/nested/thing", "child_key"); r.stdout != "ns-child-value" {
		t.Errorf("--namespace= before the command: %q", r.stdout)
	}
	if r := c.ok("-ns", nsx.Parent, "find", "k:api_key"); r.stdout != "secret/team/app/api\tapi_key\n" {
		t.Errorf("-ns: %q", r.stdout)
	}
	if st := c.ok("status", "--ns", nsx.Child).stdout; !strings.Contains(st, "namespace: "+nsx.Child) {
		t.Errorf("status --ns:\n%s", st)
	}
	if st := c.ok("config", "show").stdout; !strings.Contains(st, nsx.Parent) {
		t.Errorf("config show without --ns:\n%s", st)
	}
	if r := c.run("find", "x", "--ns"); r.code == 0 || !strings.Contains(r.stderr, "needs a namespace") {
		t.Errorf("--ns without a value: %+v", r)
	}
}

func TestCompleteNamespaces(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	c := nsCLI(t, rootLoginToken(t, time.Hour))

	got := completeCLI(t, c, "find", "--ns", "")
	for _, want := range []string{"/", nsx.Parent, nsx.Child} {
		if !has(got, want) {
			t.Errorf("--ns candidates %q lack %q", got, want)
		}
	}
	if got := completeCLI(t, c, "--namespace", nsx.Parent+"/ch"); !slices.Equal(got, []string{nsx.Child}) {
		t.Errorf("prefix: %q", got)
	}
	// Paths complete in the namespace given with --ns.
	if got := completeCLI(t, c, "--ns", nsx.Child, "get", "secret/"); !has(got, "secret/nested/") {
		t.Errorf("paths in --ns namespace: %q", got)
	}
	if got := completeCLI(t, c, "get", "--ns", nsx.Parent, "secret/team/app/db", "ns_"); !slices.Equal(got, []string{"ns_only_key"}) {
		t.Errorf("keys in --ns namespace: %q", got)
	}
}

func TestTUINamespaceSwitch(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	c := nsCLI(t, rootLoginToken(t, time.Hour))
	writeConfig(t, c, `namespace = "`+nsx.Parent+`"`+"\n")
	tm := startTUI(t, c)
	tm.waitFor(0, "secret/team/app/db")

	m := tm.mark()
	tm.send(keyCtrlN)
	tm.waitFor(m, "Namespaces")
	tm.waitFor(m, "(current)")
	tm.send("child")
	tm.waitFor(m, nsx.Child)
	m = tm.mark()
	tm.send(keyEnter)
	tm.waitFor(m, "switched to namespace "+nsx.Child)
	tm.waitFor(m, "secret/nested/thing")

	// The switch lasts for the session, and --ns starts in a namespace.
	tm.send(keyCtrlC)
	tm.waitExit()
	tm = startTUI(t, c, "--ns", nsx.Child)
	tm.waitFor(0, "ns "+nsx.Child)
	tm.waitFor(0, "secret/nested/thing")
	tm.send(keyCtrlC)
	tm.waitExit()
}
