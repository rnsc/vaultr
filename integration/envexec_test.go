//go:build integration

package integration

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCLIEnv(t *testing.T) {
	t.Parallel()
	vaultrOnPath(t) // the eval below runs vaultr by name
	c := newCLI(t, rootChild(t, time.Hour))
	pg := fx.KV2 + "prod/db/postgres"

	r := c.ok("env", pg)
	want := "export HOST='db.prod.internal'\nexport PASSWORD='pg-prod-pass'\nexport PORT='5432'\nexport USERNAME='app'\n"
	if r.stdout != want {
		t.Errorf("env:\n%s", r.stdout)
	}

	// The output evaluates in a real shell, odd keys and values included.
	cmd := exec.Command("bash", "-c", `eval "$(vaultr env --prefix APP_ "$1" "$2")" && printf '%s|%s|%s' "$APP_PASSWORD" "$APP_MULTILINE" "$APP_NESTED"`,
		"bash", pg, fx.KV2+"odd/json")
	cmd.Env = shellEnv(c)
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != `pg-prod-pass|line1`+"\n"+`line2|{"a":1}` {
		t.Errorf("eval: %v %q", err, out)
	}

	var got map[string]string
	if err := json.Unmarshal([]byte(c.ok("env", "--format", "json", fx.KV1+"legacy/ldap").stdout), &got); err != nil ||
		got["BIND_PASSWORD"] != "ldap-pass" || got["BIND_DN"] != "cn=svc" {
		t.Errorf("json: %v %v", got, err)
	}
	if r := c.ok("env", "--format", "fish", fx.KV2+"odd/with space/secret name"); r.stdout != "set -gx KEY_WITH_SPACE 'spaced'\n" {
		t.Errorf("fish: %q", r.stdout)
	}

	// Same key in two secrets: the later path wins, with a warning.
	r = c.ok("env", pg, fx.KV2+"staging/db/postgres")
	if !strings.Contains(r.stdout, "PASSWORD='pg-staging-pass'") || !strings.Contains(r.stderr, "PASSWORD from "+fx.KV2+"staging/db/postgres replaces") {
		t.Errorf("collision: %+v", r)
	}
	for _, args := range [][]string{{"env"}, {"env", "--format", "xml", pg}, {"env", fx.KV2 + "nope/missing"}} {
		if r := c.run(args...); r.code == 0 {
			t.Errorf("%q succeeded", args)
		}
	}
}

func TestCLIExec(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	pg := fx.KV2 + "prod/db/postgres"

	r := c.ok("exec", "--prefix", "DB_", pg, "--", "sh", "-c", `printf '%s@%s' "$DB_USERNAME" "$DB_HOST"; echo " $1"`, "sh", "--not-a-vaultr-flag")
	if r.stdout != "app@db.prod.internal --not-a-vaultr-flag\n" {
		t.Errorf("exec: %+v", r)
	}
	// The command's exit status is vaultr's.
	if r := c.run("exec", pg, "--", "sh", "-c", "exit 7"); r.code != 7 {
		t.Errorf("exit status %d, want 7", r.code)
	}
	for _, args := range [][]string{{"exec", pg}, {"exec", pg, "--"}, {"exec", "--", "true"}} {
		if r := c.run(args...); r.code == 0 || !strings.Contains(r.stderr, "exec") {
			t.Errorf("%q: %+v", args, r)
		}
	}
}
