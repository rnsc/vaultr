//go:build integration

package integration

import "testing"

// Starting the TUI with a token that has expired (not a missing one):
// auto_login must log in without a key press.
func TestTUIAutoLoginWithExpiredToken(t *testing.T) {
	t.Parallel()
	c := autoLoginCLI(t)
	c.env["VAULT_TOKEN"] = "hvs.expired-or-bogus"
	tm := startTUI(t, c)
	tm.waitFor(0, "logged in")
	tm.waitFor(0, "indexed ")
	tm.send(keyCtrlC)
	tm.waitExit()
}
