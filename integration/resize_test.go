//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/creack/pty"
)

// Shrinking the terminal wraps the help line instead of cutting its last
// items off, and long rows keep their key.
func TestTUIResize(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	tm := startTUI(t, c, "k:webhook_secret")
	tm.waitFor(0, "webhook_secret")
	m := tm.mark()
	if err := pty.Setsize(tm.f, &pty.Winsize{Rows: 24, Cols: 40}); err != nil {
		t.Fatal(err)
	}
	tm.waitFor(m, "^c quit") // the last help item, on a wrapped line
	tm.waitFor(m, "⟶ webhook_secret")
	tm.send(keyCtrlC)
	tm.send(keyCtrlC)
	tm.waitExit()
}
