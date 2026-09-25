//go:build integration

package integration

// auth.auto_login, and keeping a TUI session going when the token dies:
// the interrupted action runs after the new login, and the same person's
// index is kept instead of crawled again.

import (
	"strings"
	"testing"
)

// autoLoginCLI has a dead token and an OIDC [auth] setup with
// auto_login, completed by the fake browser.
func autoLoginCLI(t *testing.T) *cli {
	t.Helper()
	role, port := oidcRole(t)
	c := newCLI(t, "")
	delete(c.env, "VAULT_TOKEN")                   // the token comes from ~/.vault-token, which logins replace
	c.env["VAULTR_MOUNTS"] = fx.KV2 + "," + fx.KV1 // what the reader policy covers
	c.env["BROWSER"] = fakeBrowser
	writeConfig(t, c, "[auth]\nmethod = \"oidc\"\nmount = \""+oidcMount+"\"\nrole = \""+role+
		"\"\ncallback_port = "+itoa(port)+"\nauto_login = true\n")
	return c
}

func TestTUIAutoLoginAndResume(t *testing.T) {
	t.Parallel()
	c := autoLoginCLI(t)
	tm := startTUI(t, c, "stripe")
	// No token: it logs in by itself, then indexes.
	tm.waitFor(0, "logged in")
	tm.waitFor(0, "indexed ")
	tm.waitFor(0, "prod/payments/stripe")
	first := savedToken(t, c)

	// The token dies mid-session.
	if err := fx.Revoke(ctx(t), first); err != nil {
		t.Fatal(err)
	}
	m := tm.mark()
	tm.send(keyEnter) // open the secret: fails, logs in again, then opens it
	tm.waitFor(m, "logged in")
	tm.waitFor(m, "webhook_secret")
	m = tm.mark()
	tm.send("r")
	tm.waitFor(m, "whsec_fixture") // the value, read with the new token
	tm.send(keyEsc)
	tm.send(keyCtrlC)
	tm.send(keyCtrlC)
	tm.waitExit()

	second := savedToken(t, c)
	if second == first {
		t.Fatal("no new token saved")
	}
	mustBeValid(t, second, "")
	// The index was kept under the new token (the old one's key died with
	// it): the cache opens without a rebuild.
	st := c.ok("status").stdout
	if !strings.Contains(st, "secrets:") || !strings.Contains(st, "bound to token cubbyhole") {
		t.Errorf("status after resuming:\n%s", st)
	}
}

func TestCLIAutoLogin(t *testing.T) {
	t.Parallel()
	c := autoLoginCLI(t)

	// Not a terminal (a script): no login, the usual error.
	if r := c.run("find", "stripe"); r.code == 0 || strings.Contains(r.stderr, "auto_login") || !strings.Contains(r.stderr, "vaultr login") {
		t.Errorf("without a terminal: %+v", r)
	}

	// In a terminal: logs in, then runs the command.
	tm := startTUI(t, c, "find", "stripe")
	tm.waitFor(0, "logging in (auth.auto_login)")
	tm.waitFor(0, fx.KV2+"prod/payments/stripe")
	tm.waitExit()
	mustBeValid(t, savedToken(t, c), "")
}
