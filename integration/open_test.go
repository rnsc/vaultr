//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/testvault"
)

func TestCLIOpen(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	want := addr + "/ui/vault/secrets/" + strings.TrimSuffix(fx.KV2, "/") + "/show/odd/with%20space/secret%20name"
	if r := c.ok("open", "--print", fx.KV2+"odd/with space/secret name"); r.stdout != want+"\n" {
		t.Errorf("--print: %q, want %q", r.stdout, want)
	}

	// Without --print it hands the address to the browser ($BROWSER here).
	dir := t.TempDir()
	out := filepath.Join(dir, "url")
	browser := filepath.Join(dir, "browser")
	if err := os.WriteFile(browser, []byte("#!/bin/sh\nprintf '%s' \"$1\" > "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c.env["BROWSER"] = browser
	c.ok("open", fx.KV1+"legacy/ldap")
	deadline := time.Now().Add(5 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		if got, _ = os.ReadFile(out); len(got) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if string(got) != addr+"/ui/vault/secrets/"+strings.TrimSuffix(fx.KV1, "/")+"/show/legacy/ldap" {
		t.Errorf("browser got %q", got)
	}
	if r := c.run("open", fx.KV2+"nope/missing", "extra"); r.code == 0 {
		t.Error("two paths accepted")
	}
}

// loginPageMount mounts an OIDC auth method backed by the fake IdP and
// lists it on the web UI's login page, as a site using OIDC in the UI
// would. It is removed at cleanup.
func loginPageMount(t *testing.T, name string) string {
	t.Helper()
	mount := fx.Prefix + "-" + name
	if err := testvault.EnableOIDC(ctx(t), admin, "", mount, idp.URL, idp.ClientID, idp.ClientSecret); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testvault.DisableAuth(context.Background(), admin, "", mount) })
	if err := testvault.ShowOnLoginPage(ctx(t), admin, "", mount); err != nil {
		t.Fatal(err)
	}
	return mount
}

// loginPageMounts is what the web UI's login page asks for before anyone
// logs in: auth mount path -> type.
func loginPageMounts(t *testing.T, ns string) map[string]string {
	t.Helper()
	resp, err := client(t, "").Unauthenticated(ctx(t), ns, "GET", "sys/internal/ui/mounts", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Auth map[string]struct {
			Type string `json:"type"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for p, m := range d.Auth {
		out[p] = m.Type
	}
	return out
}

func TestCLIOpenWithLoginMethod(t *testing.T) {
	t.Parallel()
	sso := loginPageMount(t, "sso-cli")
	path := fx.KV2 + "odd/with space/secret name"
	c := newCLI(t, rootChild(t, time.Hour))
	plain := strings.TrimSuffix(c.ok("open", "--print", path).stdout, "\n")
	route := strings.TrimPrefix(plain, addr+"/ui")

	for _, tc := range []struct {
		name, config, with string
	}{
		{"custom mount", "[auth]\nmethod = \"oidc\"\nmount = \"" + sso + "\"\nrole = \"dev\"\n", sso + "/"},
		{"default mount", "[auth]\nmethod = \"oidc\"\n", "oidc"},
		{"ldap", "[auth]\nmethod = \"ldap\"\n", "ldap"},
	} {
		writeConfig(t, c, tc.config)
		got := strings.TrimSuffix(c.ok("open", "--print", path).stdout, "\n")
		u, err := url.Parse(got)
		if err != nil || !strings.HasPrefix(got, addr+"/ui/vault/auth?") {
			t.Errorf("%s: %q is not the login page (%v)", tc.name, got, err)
			continue
		}
		q := u.Query()
		if q.Get("with") != tc.with || q.Get("redirect_to") != route || q.Has("namespace") {
			t.Errorf("%s: %q: with %q, redirect_to %q, want %q and %q", tc.name, got, q.Get("with"), q.Get("redirect_to"), tc.with, route)
		}
	}
	// The UI only takes a mount path for with= when that mount is on its
	// login page, which is what it reads before anyone logs in.
	if typ := loginPageMounts(t, "")[sso+"/"]; typ != "oidc" {
		t.Errorf("%s/ is not on the login page as oidc (%q)", sso, typ)
	}

	// token: the secret's own page, as without a config.
	writeConfig(t, c, "[auth]\nmethod = \"token\"\n")
	if got := c.ok("open", "--print", path).stdout; got != plain+"\n" {
		t.Errorf("token: %q, want %q", got, plain)
	}
}

func TestCLIOpenWithLoginMethodAcrossNamespaces(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	// Log in at the root, the secret is in a namespace.
	c := nsCLI(t, rootChild(t, time.Hour))
	c.env["VAULT_NAMESPACE"] = nsx.Child
	writeConfig(t, c, "[auth]\nmethod = \"oidc\"\nmount = \"sso\"\nnamespace = \"\"\n")
	s := nsx.Secrets[nsx.Child][0]
	got := strings.TrimSuffix(c.ok("open", "--print", s.Full()).stdout, "\n")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	want := "/vault/secrets/" + strings.TrimSuffix(s.Mount, "/") + "/show/" + s.Path + "?namespace=" + url.QueryEscape(nsx.Child)
	if q.Get("with") != "sso/" || q.Has("namespace") || q.Get("redirect_to") != want {
		t.Errorf("%q: with %q, namespace %q, redirect_to %q (want %q)", got, q.Get("with"), q.Get("namespace"), q.Get("redirect_to"), want)
	}
	// Logging in to a namespace puts it on the login page.
	writeConfig(t, c, "[auth]\nmethod = \"oidc\"\nmount = \"sso\"\nnamespace = \""+nsx.Parent+"\"\n")
	got = strings.TrimSuffix(c.ok("open", "--print", s.Full()).stdout, "\n")
	if u, _ := url.Parse(got); u.Query().Get("namespace") != nsx.Parent || u.Query().Get("redirect_to") != want {
		t.Errorf("auth namespace: %q", got)
	}
}
