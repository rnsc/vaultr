//go:build integration

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/testvault"
)

func lines(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func TestCLIHelpAndVersion(t *testing.T) {
	t.Parallel()
	c := newCLI(t, "")
	delete(c.env, "VAULT_TOKEN") // help must work without Vault
	for _, a := range []string{"help", "-h", "--help"} {
		if r := c.ok(a); !strings.Contains(r.stdout, "Usage:") {
			t.Errorf("%s: no usage in %q", a, r.stdout)
		}
	}
	if r := c.ok("version"); strings.TrimSpace(r.stdout) == "" {
		t.Error("empty version")
	}
}

func TestCLIIndexAndStatus(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	if r := c.ok("status"); !strings.Contains(r.stdout, "cache:    none") {
		t.Errorf("status before index:\n%s", r.stdout)
	}
	r := c.ok("index")
	if !strings.Contains(r.stderr, "indexed ") || r.stdout != "" {
		t.Errorf("index output: stdout %q stderr %q", r.stdout, r.stderr)
	}
	st := c.ok("status").stdout
	for _, want := range []string{"bound to token cubbyhole", "secrets:  " + strconv.Itoa(len(fx.Secrets)), "expires:"} {
		if !strings.Contains(st, want) {
			t.Errorf("status missing %q:\n%s", want, st)
		}
	}
}

func TestCLIFindBuildsIndexOnDemand(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	r := c.ok("find", "stripe", "api")
	if want := fx.KV2 + "prod/payments/stripe\tapi_key\n"; r.stdout != want {
		t.Errorf("stdout %q, want %q", r.stdout, want)
	}
	if strings.Contains(r.stderr, "warning") {
		t.Errorf("unexpected warning: %s", r.stderr)
	}
	if _, err := os.Stat(c.env["VAULTR_CACHE_DIR"]); err != nil {
		t.Error("find did not write a cache")
	}
}

func TestCLIFind(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	c.ok("index")

	t.Run("key filter", func(t *testing.T) {
		got := lines(c.ok("find", "k:client_secret").stdout)
		if len(got) != testvault.BulkTeams*testvault.BulkServices {
			t.Fatalf("%d lines", len(got))
		}
		for _, l := range got {
			if !strings.HasSuffix(l, "\tclient_secret") || !strings.HasPrefix(l, fx.KV2+"teams/") {
				t.Errorf("bad line %q", l)
			}
		}
	})
	t.Run("limit and flags after terms", func(t *testing.T) {
		if got := lines(c.ok("find", "k:client_id", "-n", "3").stdout); len(got) != 3 {
			t.Errorf("%d lines, want 3", len(got))
		}
	})
	t.Run("json", func(t *testing.T) {
		var got []map[string]string
		for _, l := range lines(c.ok("find", "--json", "ldap").stdout) {
			var m map[string]string
			if err := json.Unmarshal([]byte(l), &m); err != nil {
				t.Fatalf("bad JSON %q: %v", l, err)
			}
			got = append(got, m)
		}
		sort.Slice(got, func(i, j int) bool { return got[i]["key"] < got[j]["key"] })
		if len(got) != 2 || got[0]["path"] != fx.KV1+"legacy/ldap" || got[0]["key"] != "bind_dn" || got[1]["key"] != "bind_password" {
			t.Errorf("got %v", got)
		}
		if _, ok := got[0]["value"]; ok {
			t.Error("value present without --values")
		}
	})
	t.Run("values", func(t *testing.T) {
		r := c.ok("find", "--values", "k:bind_password")
		if want := fx.KV1 + "legacy/ldap\tbind_password\tldap-pass\n"; r.stdout != want {
			t.Errorf("stdout %q, want %q", r.stdout, want)
		}
		var m map[string]string
		_ = json.Unmarshal([]byte(c.ok("find", "--json", "--values", "we#ird").stdout), &m)
		if m["value"] != "hash" {
			t.Errorf("json value %v", m)
		}
	})
	t.Run("special characters", func(t *testing.T) {
		r := c.ok("find", "clé")
		if want := fx.KV2 + "odd/ünïcode/naïve\tclé\n"; r.stdout != want {
			t.Errorf("stdout %q, want %q", r.stdout, want)
		}
	})
	t.Run("keyless rows", func(t *testing.T) {
		r := c.ok("find", "lifecycle/deleted")
		if want := fx.KV2 + "lifecycle/deleted\n"; r.stdout != want {
			t.Errorf("stdout %q, want %q", r.stdout, want)
		}
	})
	t.Run("no match exits 1 silently", func(t *testing.T) {
		r := c.run("find", "zzz-no-such-thing")
		if r.code != 1 || r.stdout != "" || r.stderr != "" {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("missing query", func(t *testing.T) {
		if r := c.run("find"); r.code != 1 || !strings.Contains(r.stderr, "missing query") {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("bad flag", func(t *testing.T) {
		if r := c.run("find", "--nope", "x"); r.code == 0 {
			t.Error("unknown flag accepted")
		}
	})
	t.Run("non-tty default command behaves like find", func(t *testing.T) {
		if r := c.ok("stripe", "webhook"); r.stdout != fx.KV2+"prod/payments/stripe\twebhook_secret\n" {
			t.Errorf("stdout %q", r.stdout)
		}
	})
}

func TestCLIGet(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"get", fx.KV2 + "prod/db/postgres", "password"}, "pg-prod-pass"},
		{[]string{"get", fx.KV2 + "prod/db/postgres", "port"}, "5432"},
		{[]string{"get", "/" + fx.KV2 + "prod/db/postgres/", "host"}, "db.prod.internal"},
		{[]string{"get", fx.KV1 + "legacy/nested/deep/thing", "api_token"}, "kv1-token"},
		{[]string{"get", fx.KV2 + "odd/with space/secret name", "key with space"}, "spaced"},
		{[]string{"get", fx.KV2 + "odd/hash#tag?q", "we#ird"}, "hash"},
		{[]string{"get", fx.KV2 + "odd/json", "multiline"}, "line1\nline2"},
		{[]string{"get", fx.KV2 + "odd/json", "nested"}, `{"a":1}`},
		{[]string{"get", fx.KV2 + "prod/payments/stripe"}, "api_key\tsk_live_fixture\nwebhook_secret\twhsec_fixture\n"},
		{[]string{"get", "--json", fx.KV2 + "odd/json", "list"}, "[1,2]\n"},
	}
	for _, tc := range cases {
		if r := c.ok(tc.args...); r.stdout != tc.want {
			t.Errorf("%v: stdout %q, want %q", tc.args, r.stdout, tc.want)
		}
	}
	var all map[string]any
	if err := json.Unmarshal([]byte(c.ok("get", fx.KV2+"odd/json", "--json").stdout), &all); err != nil || all["flag"] != true {
		t.Errorf("get --json: %v %v", all, err)
	}

	errs := []struct {
		args []string
		msg  string
	}{
		{[]string{"get", fx.KV2 + "prod/db/postgres", "nope"}, `no key "nope"`},
		{[]string{"get", fx.KV2 + "does/not/exist"}, "not found"},
		{[]string{"get", fx.Transit + "x"}, "not a KV mount"},
		{[]string{"get"}, "usage"},
		{[]string{"get", "a", "b", "c"}, "usage"},
	}
	for _, tc := range errs {
		r := c.run(tc.args...)
		if r.code == 0 || !strings.Contains(r.stderr, tc.msg) {
			t.Errorf("%v: exit %d stderr %q, want error containing %q", tc.args, r.code, r.stderr, tc.msg)
		}
	}
}

func TestCLIRestrictedToken(t *testing.T) {
	t.Parallel()
	c := newCLI(t, token(t, testvault.TokenOptions{Policies: []string{fx.Reader}, TTL: time.Hour}))
	c.env["VAULTR_MOUNTS"] = fx.KV2 + "," + fx.KV1
	r := c.ok("index")
	if !strings.Contains(r.stderr, "denied by policy") {
		t.Errorf("no denied warning: %q", r.stderr)
	}
	if r := c.run("find", "crown"); r.code != 1 {
		t.Errorf("restricted secret visible: %q", r.stdout)
	}
	// Exact key match first; staging's password is listed but unreadable.
	if r := c.ok("find", "k:password"); r.stdout != fx.KV2+"prod/db/postgres\tpassword\n"+fx.KV1+"legacy/ldap\tbind_password\n" {
		t.Errorf("stdout %q", r.stdout)
	}
	if r := c.run("get", fx.KV2+"staging/db/postgres"); r.code == 0 || !strings.Contains(r.stderr, "permission denied") {
		t.Errorf("get denied secret: %+v", r)
	}
}

func TestCLIDiscoveryWithoutMounts(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	delete(c.env, "VAULTR_MOUNTS")
	if r := c.ok("find", "unique_1_7"); !strings.HasPrefix(r.stdout, fx.KV2+"teams/team-1/svc-07/config\t") {
		t.Errorf("stdout %q", r.stdout)
	}
}

func TestCLIPathsOnly(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	c.env["VAULTR_PATHS_ONLY"] = "1"
	r := c.ok("find", "stripe")
	if r.stdout != fx.KV2+"prod/payments/stripe\n" {
		t.Errorf("stdout %q", r.stdout)
	}
	if r := c.run("find", "k:api_key"); r.code != 1 {
		t.Errorf("key names indexed in paths-only mode: %q", r.stdout)
	}
}

func TestCLIMaxAgeRebuilds(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	c.env["VAULTR_MAX_AGE"] = "2s"
	c.ok("index")
	time.Sleep(3 * time.Second)
	if st := c.ok("status").stdout; !strings.Contains(st, "expired") {
		t.Errorf("status after expiry:\n%s", st)
	}
	// Transparent rebuild.
	if r := c.ok("find", "stripe", "api"); !strings.Contains(r.stdout, "api_key") {
		t.Errorf("find after expiry: %q", r.stdout)
	}
}

func TestCLINewTokenRebuilds(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	c.ok("index")
	c.env["VAULT_TOKEN"] = rootChild(t, time.Hour)
	if st := c.ok("status").stdout; !strings.Contains(st, "needs rebuild") {
		t.Errorf("status with new token:\n%s", st)
	}
	if r := c.ok("find", "ldap"); len(lines(r.stdout)) != 2 {
		t.Errorf("find with new token: %q", r.stdout)
	}
}

func TestCLIPurge(t *testing.T) {
	t.Parallel()
	tok := rootChild(t, time.Hour)
	c := newCLI(t, tok)
	c.ok("index")
	if r := c.ok("purge"); !strings.Contains(r.stderr, "purged") {
		t.Errorf("purge stderr %q", r.stderr)
	}
	if st := c.ok("status").stdout; !strings.Contains(st, "cache:    none") {
		t.Errorf("status after purge:\n%s", st)
	}
	if keys := cubbyKeys(t, client(t, tok)); len(keys) != 0 {
		t.Errorf("purge left cubbyhole keys %v", keys)
	}
}

func TestCLITokenFromFile(t *testing.T) {
	t.Parallel()
	c := newCLI(t, "")
	delete(c.env, "VAULT_TOKEN")
	if err := os.WriteFile(c.env["HOME"]+"/.vault-token", []byte(rootChild(t, time.Hour)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.ok("find", "stripe")
}

func TestCLIAuthErrors(t *testing.T) {
	t.Parallel()
	c := newCLI(t, "")
	delete(c.env, "VAULT_TOKEN")
	if r := c.run("find", "x"); r.code != 1 || !strings.Contains(r.stderr, "no Vault token") {
		t.Errorf("no token: %+v", r)
	}
	c.env["VAULT_TOKEN"] = "hvs.invalid"
	if r := c.run("find", "x"); r.code != 1 || !strings.Contains(r.stderr, "expired or invalid") {
		t.Errorf("bad token: %+v", r)
	}
	c.env["VAULT_TOKEN"] = rootChild(t, time.Hour)
	c.env["VAULT_ADDR"] = "http://127.0.0.1:1"
	if r := c.run("find", "x"); r.code != 1 || r.stderr == "" {
		t.Errorf("unreachable server: %+v", r)
	}
}

func TestCLIBadSettings(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	for k, v := range map[string]string{
		"VAULTR_MAX_AGE":    "soon",
		"VAULTR_WORKERS":    "0",
		"VAULTR_CLIP_CLEAR": "x",
		"VAULT_SKIP_VERIFY": "maybe",
		"VAULTR_MOUNTS":     fx.Prefix + "-missing",
	} {
		old, had := c.env[k]
		c.env[k] = v
		if r := c.run("find", "x"); r.code != 1 || r.stderr == "" {
			t.Errorf("%s=%s accepted: %+v", k, v, r)
		}
		if had {
			c.env[k] = old
		} else {
			delete(c.env, k)
		}
	}
}

// freshMount creates a KV v2 mount for one test, so adding secrets to it
// does not disturb the shared fixture. It returns the mount ("x/").
func freshMount(t *testing.T) string {
	t.Helper()
	m := fx.Prefix + "-fresh-" + itoa(freePort(t))
	if _, err := root.Write(ctx(t), "sys/mounts/"+m, map[string]any{"type": "kv", "options": map[string]string{"version": "2"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Delete(context.Background(), "sys/mounts/"+m) })
	putSecret(t, m+"/", "seed", "seed_key")
	return m + "/"
}

// putSecret writes a secret, retrying while a new KV v2 mount upgrades.
func putSecret(t *testing.T, mount, path, key string) {
	t.Helper()
	var err error
	for i := 0; i < 50; i++ {
		if _, err = root.Write(ctx(t), mount+"data/"+path, map[string]any{"data": map[string]any{key: "v"}}); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(err)
}

func TestCLIFindRefresh(t *testing.T) {
	t.Parallel()
	mount := freshMount(t)
	c := newCLI(t, rootChild(t, time.Hour))
	c.env["VAULTR_MOUNTS"] = mount
	c.ok("index")
	putSecret(t, mount, "fresh/added", "fresh_key") // after the index was built
	if r := c.run("find", "fresh_key"); r.code != 1 {
		t.Fatalf("new secret found without a refresh? %+v", r)
	}
	for _, flag := range []string{"-r", "--refresh"} {
		if r := c.ok("find", flag, "fresh_key"); r.stdout != mount+"fresh/added\tfresh_key\n" {
			t.Errorf("find %s: %q", flag, r.stdout)
		}
	}
	// The refreshed index is kept for later searches.
	if r := c.ok("find", "fresh_key"); !strings.Contains(r.stdout, "fresh_key") {
		t.Errorf("refresh not saved: %q", r.stdout)
	}
}
