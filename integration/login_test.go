//go:build integration

package integration

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/testvault"
	"github.com/rnsc/vaultr/internal/vault"
)

// loginCLI is a CLI environment with no token at all.
func loginCLI(t *testing.T) *cli {
	t.Helper()
	c := newCLI(t, "")
	delete(c.env, "VAULT_TOKEN")
	c.env["VAULTR_MOUNTS"] = fx.KV2 + "," + fx.KV1 // what the reader policy covers
	return c
}

func tokenFile(c *cli) string { return filepath.Join(c.env["HOME"], ".vault-token") }

// savedToken returns the token vaultr saved, checking the file mode.
func savedToken(t *testing.T, c *cli) string {
	t.Helper()
	p := tokenFile(c)
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("no saved token: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("%s mode %v, want 0600", p, st.Mode().Perm())
	}
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

// mustBeValid checks that tok is a live token issued in namespace ns.
func mustBeValid(t *testing.T, tok, ns string) {
	t.Helper()
	cl, _ := vault.New(vault.Config{Addr: addr, Token: tok, Namespace: ns, TokenNamespace: &ns})
	if _, err := cl.LookupSelf(ctx(t)); err != nil {
		t.Fatalf("saved token does not work: %v", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// oidcRole creates a role for this test on a free callback port.
func oidcRole(t *testing.T) (role string, port int) {
	t.Helper()
	port = freePort(t)
	role = "r" + itoa(port)
	redirect := "http://localhost:" + itoa(port) + "/oidc/callback"
	if err := testvault.OIDCRole(context.Background(), admin, "", oidcMount, role, idp.ClientID, []string{redirect}, []string{fx.Reader}); err != nil {
		t.Fatal(err)
	}
	return role, port
}

// A fake browser: follow the authorization URL through the provider's
// redirect to vaultr's local callback.
const fakeBrowser = "curl -fsS -L -o /dev/null"

func TestCLILoginUserpass(t *testing.T) {
	t.Parallel()
	c := loginCLI(t)
	if r := c.run("find", "stripe"); r.code != 1 || !strings.Contains(r.stderr, "vaultr login") {
		t.Errorf("no token: %+v", r)
	}

	r := c.runIn("wrong\n", "login", "-method", "userpass", "-mount", userpassMount, "-username", "alice")
	if r.code != 1 || !strings.Contains(r.stderr, "login failed") {
		t.Errorf("wrong password: %+v", r)
	}
	if _, err := os.Stat(tokenFile(c)); !os.IsNotExist(err) {
		t.Error("token file written after a failed login")
	}

	r = c.runIn("alice-pass\n", "login", "-method", "userpass", "-mount", userpassMount, "-username", "alice")
	if r.code != 0 || !strings.Contains(r.stderr, "Logged in") || !strings.Contains(r.stderr, "saved to ~/.vault-token") || r.stdout != "" {
		t.Fatalf("login: %+v", r)
	}
	mustBeValid(t, savedToken(t, c), "")

	// Later commands pick the token up from ~/.vault-token.
	if r := c.ok("find", "k:password"); !strings.Contains(r.stdout, fx.KV2+"prod/db/postgres\tpassword") {
		t.Errorf("find after login: %q", r.stdout)
	}
}

func TestCLILoginFromConfig(t *testing.T) {
	t.Parallel()
	c := loginCLI(t)
	writeConfig(t, c, "[auth]\nmethod = \"userpass\"\nmount = \""+userpassMount+"\"\nusername = \"alice\"\n")
	// Only the password is asked for.
	if r := c.runIn("alice-pass\n", "login"); r.code != 0 {
		t.Fatalf("login from config: %+v", r)
	}
	savedToken(t, c)

	// Username not configured: prompted too (both lines on stdin).
	c2 := loginCLI(t)
	writeConfig(t, c2, "[auth]\nmethod = \"userpass\"\nmount = \""+userpassMount+"\"\n")
	if r := c2.runIn("alice\nalice-pass\n", "login"); r.code != 0 {
		t.Fatalf("prompted username: %+v", r)
	}
}

func TestCLILoginNoSaveAndToken(t *testing.T) {
	t.Parallel()
	c := loginCLI(t)
	r := c.runIn("alice-pass\n", "login", "-method", "userpass", "-mount", userpassMount, "-username", "alice", "-no-save")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "hv") && !strings.HasPrefix(r.stdout, "s.") {
		t.Fatalf("no-save should print the token: %+v", r)
	}
	if _, err := os.Stat(tokenFile(c)); !os.IsNotExist(err) {
		t.Error("-no-save wrote ~/.vault-token")
	}
	printed := strings.TrimSpace(r.stdout)

	// The token method validates and saves a pasted token.
	if r := c.runIn(printed+"\n", "login", "-method", "token"); r.code != 0 {
		t.Fatalf("token login: %+v", r)
	}
	if savedToken(t, c) != printed {
		t.Error("token method saved a different token")
	}
	if r := c.runIn("hvs.bogus\n", "login", "-method", "token"); r.code != 1 || !strings.Contains(r.stderr, "expired or invalid") {
		t.Errorf("bogus token: %+v", r)
	}
}

func TestCLILoginWarnsAboutVAULT_TOKEN(t *testing.T) {
	t.Parallel()
	c := loginCLI(t)
	c.env["VAULT_TOKEN"] = "hvs.stale"
	r := c.runIn("alice-pass\n", "login", "-method", "userpass", "-mount", userpassMount, "-username", "alice")
	if r.code != 0 || !strings.Contains(r.stderr, "VAULT_TOKEN is set") {
		t.Errorf("%+v", r)
	}
}

func TestCLILoginBadInput(t *testing.T) {
	t.Parallel()
	c := loginCLI(t)
	for _, args := range [][]string{
		{"login", "-method", "kerberos"},
		{"login", "extra-arg"},
	} {
		if r := c.run(args...); r.code == 0 {
			t.Errorf("%v accepted", args)
		}
	}
	// Empty stdin for the password.
	if r := c.run("login", "-method", "userpass", "-mount", userpassMount, "-username", "alice"); r.code == 0 {
		t.Error("login without a password succeeded")
	}
}

func TestCLILoginOIDC(t *testing.T) {
	t.Parallel()
	role, port := oidcRole(t)
	c := loginCLI(t)
	c.env["BROWSER"] = fakeBrowser
	writeConfig(t, c, "[auth]\nmethod = \"oidc\"\nmount = \""+oidcMount+"\"\nrole = \""+role+"\"\ncallback_port = "+itoa(port)+"\n")
	r := c.run("login")
	if r.code != 0 || !strings.Contains(r.stderr, "Complete the login in your browser") || !strings.Contains(r.stderr, "Logged in") {
		t.Fatalf("oidc login: %+v", r)
	}
	mustBeValid(t, savedToken(t, c), "")
	if r := c.ok("find", "k:bind_password"); !strings.Contains(r.stdout, "legacy/ldap") {
		t.Errorf("find after oidc login: %q", r.stdout)
	}
}

func TestCLILoginOIDCErrors(t *testing.T) {
	t.Parallel()
	_, port := oidcRole(t)
	c := loginCLI(t)
	c.env["BROWSER"] = fakeBrowser
	if r := c.run("login", "-method", "oidc", "-mount", oidcMount, "-role", "no-such-role", "-callback-port", itoa(port)); r.code != 1 || r.stderr == "" {
		t.Errorf("unknown role: %+v", r)
	}
	// The provider refuses: the error reaches the user.
	idp.DenyRedirect("localhost:" + itoa(port))
	if r := c.run("login", "-method", "oidc", "-mount", oidcMount, "-role", "r"+itoa(port), "-callback-port", itoa(port)); r.code != 1 || !strings.Contains(r.stderr, "the user declined") {
		t.Errorf("denied at the provider: %+v", r)
	}
	if r := c.run("login", "-method", "oidc", "-mount", fx.Prefix+"-nope"); r.code != 1 {
		t.Errorf("missing mount: %+v", r)
	}
}

func TestCLILoginIntoNamespace(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	c := loginCLI(t)
	delete(c.env, "VAULTR_MOUNTS")
	writeConfig(t, c, "namespace = \""+nsx.Parent+"\"\n[auth]\nmethod = \"userpass\"\nusername = \"bob\"\nnamespace = \""+nsx.Parent+"\"\n")
	if r := c.runIn("bob-pass\n", "login"); r.code != 0 || !strings.Contains(r.stderr, "namespace "+nsx.Parent) {
		t.Fatalf("namespace login: %+v", r)
	}
	mustBeValid(t, savedToken(t, c), nsx.Parent)
	if r := c.ok("find", "ns_only_key"); r.stdout != "secret/team/app/db\tns_only_key\n" {
		t.Errorf("find: %q", r.stdout)
	}
	if st := c.ok("status").stdout; !strings.Contains(st, "token ns: "+nsx.Parent) || !strings.Contains(st, "bound to token cubbyhole") {
		t.Errorf("status:\n%s", st)
	}
}

func TestTUILoginUserpass(t *testing.T) {
	t.Parallel()
	c := loginCLI(t)
	writeConfig(t, c, "[auth]\nmethod = \"userpass\"\nmount = \""+userpassMount+"\"\nusername = \"alice\"\n")
	tm := startTUI(t, c)
	tm.waitFor(0, "No Vault token found.")
	tm.waitFor(0, "[userpass]")

	m := tm.mark()
	tm.send("nope" + keyEnter)
	tm.waitFor(m, "login failed")

	m = tm.mark()
	tm.send("alice-pass" + keyEnter)
	tm.waitFor(m, "logged in")
	tm.waitFor(m, "indexed ")

	m = tm.mark()
	tm.send("stripe api")
	tm.waitFor(m, "api_key")
	tm.send(keyCtrlC) // clears the search
	tm.send(keyCtrlC) // quits
	tm.waitExit()
	savedToken(t, c)
}

func TestTUILoginOIDCAfterExpiredToken(t *testing.T) {
	t.Parallel()
	role, port := oidcRole(t)
	c := newCLI(t, "hvs.expired-or-bogus")
	c.env["VAULTR_MOUNTS"] = fx.KV2 + "," + fx.KV1 // what the reader policy covers
	c.env["BROWSER"] = fakeBrowser
	writeConfig(t, c, "[auth]\nmethod = \"oidc\"\nmount = \""+oidcMount+"\"\nrole = \""+role+"\"\ncallback_port = "+itoa(port)+"\n")
	tm := startTUI(t, c)
	tm.waitFor(0, "expired or revoked")
	tm.waitFor(0, "[oidc]")
	m := tm.mark()
	tm.send(keyEnter)
	tm.waitFor(m, "logged in")
	tm.waitFor(m, "VAULT_TOKEN is set") // the env token is stale; we warn about it
	tm.waitFor(m, "indexed ")
	tm.send(keyCtrlC)
	tm.waitExit()
}

func TestTUIConfigEditorCreatesFile(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	p := filepath.Join(t.TempDir(), "cfg", "config.toml")
	c.env["VAULTR_CONFIG"] = p
	tm := startTUI(t, c)
	tm.waitFor(0, "indexed ")

	m := tm.mark()
	tm.send("\x05") // ctrl+e
	tm.waitFor(m, "(new file)")
	// Go down to paths_only (the 10th setting) and toggle it on.
	for i := 0; i < 9; i++ {
		tm.send("\x1b[B") // down
	}
	tm.send(" ")
	m = tm.mark()
	tm.send("\x13") // ctrl+s
	tm.waitFor(m, "saved "+p)
	tm.send(keyCtrlC)
	tm.waitExit()

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if !strings.Contains(string(b), "\npaths_only = true\n") {
		t.Errorf("paths_only not saved:\n%s", b)
	}
	// The CLI reads the new setting: paths only, so no key names.
	c.ok("index")
	if r := c.run("find", "k:api_key"); r.code != 1 {
		t.Errorf("key names still indexed after paths_only: %q", r.stdout)
	}
}

func TestTUIEscClearsSearch(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	c.ok("index")
	tm := startTUI(t, c, "ldap")
	tm.waitFor(0, "bind_password")
	m := tm.mark()
	tm.send(keyEsc)
	tm.waitFor(m, "search paths and keys") // placeholder is back
	tm.send(keyEsc)                        // esc on an empty search does nothing
	tm.send("stripe")
	tm.send(keyCtrlC) // clears, does not quit
	m = tm.mark()
	tm.send("ldap")
	tm.waitFor(m, "bind_password")
	tm.send(keyCtrlC)
	tm.send(keyCtrlC)
	tm.waitExit()
}
