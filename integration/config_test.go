//go:build integration

package integration

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestCLIAddressFromConfig(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	delete(c.env, "VAULT_ADDR")
	writeConfig(t, c, `address = "`+addr+`"`+"\n")
	if r := c.ok("find", "stripe", "api"); !strings.Contains(r.stdout, "api_key") {
		t.Errorf("address from config: %q", r.stdout)
	}
	// VAULT_ADDR wins over the file.
	c.env["VAULT_ADDR"] = "http://127.0.0.1:1"
	if r := c.run("get", fx.KV1+"legacy/ldap"); r.code == 0 {
		t.Error("VAULT_ADDR did not override the config file")
	}
}

func TestCLIVaultURLAlias(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	delete(c.env, "VAULT_ADDR")
	c.env["VAULT_URL"] = addr
	if r := c.ok("get", fx.KV1+"legacy/ldap", "bind_dn"); r.stdout != "cn=svc" {
		t.Errorf("VAULT_URL: %q", r.stdout)
	}
}

func TestCLISettingsFromConfig(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	delete(c.env, "VAULTR_MOUNTS")
	writeConfig(t, c, `
mounts     = ["`+fx.KV1+`"]
paths_only = true
workers    = 2
max_age    = "10m"
`)
	r := c.ok("find", "legacy")
	if r.stdout != fx.KV1+"legacy/ldap\n"+fx.KV1+"legacy/nested/deep/thing\n" {
		t.Errorf("mounts/paths_only from config: %q", r.stdout)
	}
	st := c.ok("status").stdout
	if !strings.Contains(st, "(in 10m") && !strings.Contains(st, "(in 9m") {
		t.Errorf("max_age from config not applied:\n%s", st)
	}
}

func TestCLIBadConfig(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	for body, msg := range map[string]string{
		`token = "hvs.x"`:   "tokens are not read",
		`namespce = "typo"`: "unknown setting",
		`workers = [`:       "config.toml",
	} {
		writeConfig(t, c, body)
		if r := c.run("find", "x"); r.code != 1 || !strings.Contains(r.stderr, msg) {
			t.Errorf("%q: %+v", body, r)
		}
	}
}

func TestCLIConfigInit(t *testing.T) {
	t.Parallel()
	c := newCLI(t, "")
	delete(c.env, "VAULT_TOKEN")
	p := t.TempDir() + "/sub/config.toml"
	c.env["VAULTR_CONFIG"] = p
	c.ok("config", "init")
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("config init: %v %v", err, st)
	}
	if r := c.ok("config", "path"); strings.TrimSpace(r.stdout) != p {
		t.Errorf("config path: %q", r.stdout)
	}
	if r := c.ok("config"); !strings.Contains(r.stdout, "(loaded)") || !strings.Contains(r.stdout, "token") {
		t.Errorf("config show:\n%s", r.stdout)
	}
	if r := c.run("config", "init"); r.code == 0 {
		t.Error("init overwrote the file")
	}
}
