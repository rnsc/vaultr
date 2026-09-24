//go:build integration

package integration

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/search"
	"github.com/rnsc/vaultr/internal/testvault"
	"github.com/rnsc/vaultr/internal/vault"
)

func fixtureMounts(t *testing.T, c *vault.Client) []vault.Mount {
	t.Helper()
	var ms []vault.Mount
	for _, p := range fx.Mounts() {
		m, err := c.MountFor(ctx(t), p)
		if err != nil {
			t.Fatalf("MountFor(%s): %v", p, err)
		}
		ms = append(ms, m)
	}
	return ms
}

func build(t *testing.T, c *vault.Client, opt index.Options) *index.Result {
	t.Helper()
	if opt.Mounts == nil {
		opt.Mounts = fixtureMounts(t, c)
	}
	res, err := index.Build(ctx(t), c, opt)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, e := range res.Errors {
		t.Errorf("crawl error: %v", e)
	}
	return res
}

func asMap(entries []index.Entry) map[string][]string {
	m := map[string][]string{}
	for _, e := range entries {
		m[e.Path] = e.Keys
	}
	return m
}

func diff(t *testing.T, got, want map[string][]string) {
	t.Helper()
	for p, wk := range want {
		gk, ok := got[p]
		if !ok {
			t.Errorf("missing %s", p)
			continue
		}
		if len(gk) != 0 || len(wk) != 0 {
			if !reflect.DeepEqual(gk, wk) {
				t.Errorf("%s: keys %v, want %v", p, gk, wk)
			}
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			t.Errorf("unexpected %s", p)
		}
	}
}

func TestDiscoverMounts(t *testing.T) {
	t.Parallel()
	ms, err := root.KVMounts(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, m := range ms {
		got[m.Path] = m.KVVersion
	}
	for p, v := range map[string]int{fx.KV2: 2, fx.KV1: 1, fx.Empty: 2} {
		if got[p] != v {
			t.Errorf("mount %s: version %d, want %d (all: %v)", p, got[p], v, got)
		}
	}
	if _, ok := got[fx.Transit]; ok {
		t.Error("non-KV transit mount was returned")
	}
	if _, ok := got["cubbyhole/"]; ok {
		t.Error("cubbyhole was returned")
	}
}

func TestDiscoverMountsRestrictedToken(t *testing.T) {
	t.Parallel()
	// sys/internal/ui/mounts only shows mounts the token has access to.
	c := client(t, token(t, testvault.TokenOptions{Policies: []string{fx.Reader}}))
	ms, err := c.KVMounts(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, m := range ms {
		paths = append(paths, m.Path)
	}
	sort.Strings(paths)
	has := func(p string) bool {
		i := sort.SearchStrings(paths, p)
		return i < len(paths) && paths[i] == p
	}
	if !has(fx.KV2) || !has(fx.KV1) {
		t.Errorf("reader should see %s and %s, got %v", fx.KV2, fx.KV1, paths)
	}
	if has(fx.Empty) {
		t.Errorf("reader has no access to %s but it was listed", fx.Empty)
	}
}

func TestMountFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path  string
		mount string
		v     int
	}{
		{fx.KV2 + "prod/db/postgres", fx.KV2, 2},
		{fx.KV2 + "odd/with space/secret name", fx.KV2, 2},
		{fx.KV1 + "legacy/ldap", fx.KV1, 1},
		{strings.TrimSuffix(fx.Empty, "/"), fx.Empty, 2},
	}
	for _, c := range cases {
		m, err := root.MountFor(ctx(t), c.path)
		if err != nil {
			t.Errorf("%s: %v", c.path, err)
			continue
		}
		if m.Path != c.mount || m.KVVersion != c.v {
			t.Errorf("%s: got %+v, want %s v%d", c.path, m, c.mount, c.v)
		}
	}
	if _, err := root.MountFor(ctx(t), fx.Transit+"keys/x"); err == nil || !strings.Contains(err.Error(), "not a KV mount") {
		t.Errorf("transit: want not a KV mount error, got %v", err)
	}
	if _, err := root.MountFor(ctx(t), fx.Prefix+"-nope/x"); err == nil {
		t.Error("nonexistent mount: want error")
	}
}

func TestIndexMatchesFixture(t *testing.T) {
	t.Parallel()
	res := build(t, root, index.Options{})
	if res.Denied != 0 {
		t.Errorf("root token: %d denied", res.Denied)
	}
	diff(t, asMap(res.Entries), fx.Expected())
	if want := len(fx.Secrets); len(res.Entries) != want {
		t.Errorf("%d entries, want %d", len(res.Entries), want)
	}
	if !sort.SliceIsSorted(res.Entries, func(i, j int) bool { return res.Entries[i].Path < res.Entries[j].Path }) {
		t.Error("entries not sorted")
	}
	for _, e := range res.Entries {
		if !strings.HasPrefix(e.Path, e.Mount) || e.Mount+e.Rel() != e.Path {
			t.Errorf("inconsistent entry %+v", e)
		}
		want := 2
		if e.Mount == fx.KV1 {
			want = 1
		}
		if e.KV != want {
			t.Errorf("%s: KV %d, want %d", e.Path, e.KV, want)
		}
	}
}

func TestIndexLatestVersionOnly(t *testing.T) {
	t.Parallel()
	got := asMap(build(t, root, index.Options{}).Entries)
	if k := got[fx.KV2+"lifecycle/versioned"]; !reflect.DeepEqual(k, []string{"new_key"}) {
		t.Errorf("versioned secret keys %v, want [new_key]", k)
	}
	for _, p := range []string{"lifecycle/deleted", "lifecycle/destroyed"} {
		k, ok := got[fx.KV2+p]
		if !ok {
			t.Errorf("%s: still has metadata, should be listed", p)
		}
		if len(k) != 0 {
			t.Errorf("%s: keys %v, want none", p, k)
		}
	}
}

func TestIndexWorkerCountsAgree(t *testing.T) {
	t.Parallel()
	mounts := fixtureMounts(t, root)
	one := build(t, root, index.Options{Mounts: mounts, Workers: 1})
	many := build(t, root, index.Options{Mounts: mounts, Workers: 128})
	if !reflect.DeepEqual(one.Entries, many.Entries) {
		t.Error("results differ between 1 and 128 workers")
	}
}

func TestIndexProgress(t *testing.T) {
	t.Parallel()
	var last index.Progress
	calls := 0
	res := build(t, root, index.Options{Workers: 1, OnProgress: func(p index.Progress) {
		calls++
		if p.Secrets < last.Secrets || p.Lists < last.Lists {
			t.Errorf("progress went backwards: %+v after %+v", p, last)
		}
		last = p
	}})
	if calls == 0 {
		t.Fatal("no progress reported")
	}
	if last.Secrets != int64(len(res.Entries)) {
		t.Errorf("final progress %d secrets, want %d", last.Secrets, len(res.Entries))
	}
}

func TestIndexReaderPolicy(t *testing.T) {
	t.Parallel()
	c := client(t, token(t, testvault.TokenOptions{Policies: []string{fx.Reader}}))
	// The reader policy does not cover the empty mount.
	res := build(t, c, index.Options{Mounts: fixtureMounts(t, root)[:2]})
	want := map[string][]string{}
	var denied int64 = 1 // listing restricted/
	for _, s := range fx.Secrets {
		switch {
		case strings.HasPrefix(s.Path, "restricted/"):
			continue // not listable, so invisible
		case s.Mount == fx.KV1 || strings.HasPrefix(s.Path, "prod/"):
			want[s.Full()] = testvault.Keys(s)
		default:
			want[s.Full()] = nil // listed, but reading is denied
			denied++
		}
	}
	diff(t, asMap(res.Entries), want)
	if res.Denied != denied {
		t.Errorf("denied %d, want %d", res.Denied, denied)
	}
}

func TestIndexListOnlyPolicy(t *testing.T) {
	t.Parallel()
	c := client(t, token(t, testvault.TokenOptions{Policies: []string{fx.ListOnly}}))
	want := map[string][]string{}
	for _, s := range fx.Secrets {
		want[s.Full()] = nil
	}
	t.Run("reading", func(t *testing.T) {
		res := build(t, c, index.Options{Mounts: fixtureMounts(t, root)[:2]})
		diff(t, asMap(res.Entries), want)
		if res.Denied != int64(len(fx.Secrets)) {
			t.Errorf("denied %d, want %d", res.Denied, len(fx.Secrets))
		}
	})
	t.Run("paths only", func(t *testing.T) {
		res := build(t, c, index.Options{Mounts: fixtureMounts(t, root)[:2], PathsOnly: true})
		diff(t, asMap(res.Entries), want)
		if res.Denied != 0 {
			t.Errorf("paths-only should not read secrets, %d denied", res.Denied)
		}
	})
}

func TestIndexEmptyMount(t *testing.T) {
	t.Parallel()
	m, err := root.MountFor(ctx(t), fx.Empty)
	if err != nil {
		t.Fatal(err)
	}
	res := build(t, root, index.Options{Mounts: []vault.Mount{m}})
	if len(res.Entries) != 0 {
		t.Errorf("empty mount produced %d entries", len(res.Entries))
	}
}

func TestIndexCancelled(t *testing.T) {
	t.Parallel()
	c, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := index.Build(c, root, index.Options{Mounts: fixtureMounts(t, root)})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

func TestSearchOverRealIndex(t *testing.T) {
	t.Parallel()
	ix := search.New(build(t, root, index.Options{}).Entries)
	cases := []struct {
		q     string
		first string // "path#key" of the best hit
		n     int    // expected hit count, -1 to skip
	}{
		{"stripe api", fx.KV2 + "prod/payments/stripe#api_key", 1},
		{"k:bind_password", fx.KV1 + "legacy/ldap#bind_password", 1},
		{"unique_3_24", fx.KV2 + "teams/team-3/svc-24/config#unique_3_24", 1},
		{"k:client_secret", "", testvault.BulkTeams * testvault.BulkServices},
		{"p:team-2 k:client_id", "", testvault.BulkServices},
		{"clé", fx.KV2 + "odd/ünïcode/naïve#clé", 1},
		{"NAÏVE", fx.KV2 + "odd/ünïcode/naïve#clé", 1},
		{"with space", fx.KV2 + "odd/with space/secret name#key with space", 1},
		{"we#ird", fx.KV2 + "odd/hash#tag?q#we#ird", 1},
		{"deep_token", fx.KV2 + "deep/a/b/c/d/e/f/g/leaf#deep_token", 1},
		{"password", "", -1},
		{"nothing-matches-this", "", 0},
	}
	for _, c := range cases {
		rows := ix.Search(c.q, 0)
		if c.n >= 0 && len(rows) != c.n {
			t.Errorf("%q: %d hits, want %d", c.q, len(rows), c.n)
		}
		if c.first != "" && (len(rows) == 0 || rows[0].Entry.Path+"#"+rows[0].Key != c.first) {
			got := "<none>"
			if len(rows) > 0 {
				got = rows[0].Entry.Path + "#" + rows[0].Key
			}
			t.Errorf("%q: best hit %s, want %s", c.q, got, c.first)
		}
	}
	// Exact key name must outrank path-only matches.
	rows := ix.Search("password", 0)
	if len(rows) == 0 || rows[0].Key != "password" {
		t.Errorf("password: want an exact key match first, got %d rows", len(rows))
	}
}

func TestReadSecretValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		full string
		key  string
		want string
	}{
		{fx.KV2 + "odd/json", "nested", `{"a":1}`},
		{fx.KV2 + "odd/json", "list", `[1,2]`},
		{fx.KV2 + "odd/json", "flag", `true`},
		{fx.KV2 + "odd/json", "empty", ``},
		{fx.KV2 + "odd/json", "multiline", "line1\nline2"},
		{fx.KV2 + "prod/db/postgres", "port", `5432`},
		{fx.KV2 + "odd/hash#tag?q", "we#ird", "hash"},
		{fx.KV2 + "odd/with space/secret name", "key with space", "spaced"},
		{fx.KV2 + "odd/ünïcode/naïve", "clé", "unicode-value"},
		{fx.KV1 + "legacy/ldap", "bind_password", "ldap-pass"},
	}
	for _, c := range cases {
		m, err := root.MountFor(ctx(t), c.full)
		if err != nil {
			t.Fatal(err)
		}
		data, err := root.ReadSecret(ctx(t), m, strings.TrimPrefix(c.full, m.Path))
		if err != nil {
			t.Errorf("%s: %v", c.full, err)
			continue
		}
		if got := vault.Stringify(data)[c.key]; got != c.want {
			t.Errorf("%s#%s = %q, want %q", c.full, c.key, got, c.want)
		}
	}
	m, _ := root.MountFor(ctx(t), fx.KV2)
	if _, err := root.ReadSecret(ctx(t), m, "does/not/exist"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("missing secret: want ErrNotFound, got %v", err)
	}
	c := client(t, token(t, testvault.TokenOptions{Policies: []string{fx.Reader}}))
	if _, err := c.ReadSecret(ctx(t), m, "teams/team-0/svc-00/config"); !errors.Is(err, vault.ErrForbidden) {
		t.Errorf("denied secret: want ErrForbidden, got %v", err)
	}
}

func TestInvalidToken(t *testing.T) {
	t.Parallel()
	c := client(t, "hvs.not-a-real-token")
	if _, err := c.LookupSelf(ctx(t)); !errors.Is(err, vault.ErrForbidden) {
		t.Errorf("want ErrForbidden, got %v", err)
	}
}
