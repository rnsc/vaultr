//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCLIVersions(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	versioned := fx.KV2 + "lifecycle/versioned" // v1 {old_key: v1}, v2 {new_key: v2}

	r := c.ok("versions", versioned)
	lines := lines(r.stdout)
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "2\t") || !strings.HasSuffix(lines[1], "\tcurrent") || !strings.HasPrefix(lines[2], "1\t") {
		t.Errorf("versions:\n%s", r.stdout)
	}
	if r := c.ok("get", "--version", "1", versioned, "old_key"); r.stdout != "v1" {
		t.Errorf("get --version 1: %q", r.stdout)
	}
	if r := c.ok("get", versioned, "new_key"); r.stdout != "v2" {
		t.Errorf("get current: %q", r.stdout)
	}

	var vs []struct {
		Version   int
		Deleted   *time.Time
		Destroyed bool
		Current   bool
	}
	if err := json.Unmarshal([]byte(c.ok("versions", "--json", fx.KV2+"lifecycle/deleted").stdout), &vs); err != nil ||
		len(vs) != 1 || vs[0].Deleted == nil || !vs[0].Current {
		t.Errorf("deleted, json: %+v %v", vs, err)
	}
	if r := c.ok("versions", fx.KV2+"lifecycle/destroyed"); !strings.Contains(r.stdout, "destroyed") {
		t.Errorf("destroyed: %q", r.stdout)
	}
	if r := c.run("get", "--version", "1", fx.KV2+"lifecycle/destroyed"); r.code == 0 || !strings.Contains(r.stderr, "no readable version 1") {
		t.Errorf("destroyed version: %+v", r)
	}
	if r := c.run("versions", fx.KV1+"legacy/ldap"); r.code == 0 || !strings.Contains(r.stderr, "KV v1") {
		t.Errorf("kv1: %+v", r)
	}
}

func TestTUIVersions(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	tm := startTUI(t, c, "new_key")
	tm.waitFor(0, "lifecycle/versioned")
	m := tm.mark()
	tm.send(keyEnter)
	tm.waitFor(m, "version 2 of 2 (current)")
	m = tm.mark()
	tm.send("[")
	tm.waitFor(m, "version 1 of 2 (older)")
	tm.waitFor(m, "old_key")
	m = tm.mark()
	tm.send("r")
	tm.waitFor(m, "v1")
	tm.send(keyEsc)
	tm.send(keyCtrlC)
	tm.send(keyCtrlC)
	tm.waitExit()
}
