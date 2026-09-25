//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"
)

// Recent rows are kept in the encrypted index, so a later session shows
// them first.
func TestTUIRecentAcrossSessions(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	tm := startTUI(t, c, "k:api_token")
	tm.waitFor(0, "legacy/nested/deep/thing")
	m := tm.mark()
	tm.send(keyEnter)
	tm.waitFor(m, "api_token")
	tm.send(keyEsc)
	tm.send(keyCtrlC) // clear the search
	tm.send(keyCtrlC) // quit
	tm.waitExit()

	tm = startTUI(t, c)
	tm.waitFor(0, "api_token  recent")
	tm.send(keyCtrlC)
	tm.waitExit()
	if st := c.ok("status").stdout; !strings.Contains(st, "bound to token cubbyhole") {
		t.Errorf("status:\n%s", st)
	}
}
