// Package testvault provisions a known secret tree, policies and tokens in
// a real Vault (normally a dev server) for integration tests and demos.
//
// Everything is created under mounts named "<prefix>-..." so several runs
// can share one server and Teardown removes all of it.
package testvault

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Admin is a deliberately separate, minimal HTTP client so fixtures do not
// depend on the vaultr client under test (a path-encoding bug there would
// otherwise be mirrored on both the write and the read side).
type Admin struct {
	Addr      string
	Token     string
	Namespace string
	HTTP      *http.Client
}

// In returns a copy of a scoped to namespace ns.
func (a *Admin) In(ns string) *Admin {
	c := *a
	c.Namespace = ns
	return &c
}

func (a *Admin) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	return a.doQuery(ctx, method, path, "", body)
}

func (a *Admin) doQuery(ctx context.Context, method, path, query string, body any) (json.RawMessage, error) {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.Addr, "/")+"/v1/"+strings.Join(segs, "/")+query, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", a.Token)
	if a.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", a.Namespace)
	}
	hc := a.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(raw))
	}
	return raw, nil
}

func (a *Admin) write(ctx context.Context, path string, body any) (json.RawMessage, error) {
	return a.do(ctx, http.MethodPost, path, body)
}

func (a *Admin) delete(ctx context.Context, path string) error {
	_, err := a.do(ctx, http.MethodDelete, path, nil)
	return err
}

// Secret is one fixture secret.
type Secret struct {
	Mount string // mount path with trailing slash
	Path  string // relative to the mount
	Data  map[string]any
	// Deleted marks secrets whose latest KV v2 version is deleted or
	// destroyed: they are listed but have no readable keys.
	Deleted bool
}

// Full returns the logical path, e.g. "p-kv2/prod/db".
func (s Secret) Full() string { return s.Mount + s.Path }

// Fixture describes what Provision created.
type Fixture struct {
	Prefix  string
	KV2     string // "<prefix>-kv2/"
	KV1     string // "<prefix>-kv1/"
	Empty   string // empty KV v2 mount
	Transit string // non-KV mount, must be ignored

	// Policy names.
	Reader, ListOnly, NoCubby string

	Secrets []Secret
	admin   *Admin
}

// Mounts returns the KV mounts of the fixture.
func (f *Fixture) Mounts() []string { return []string{f.KV2, f.KV1, f.Empty} }

// Expected returns full path -> sorted key names for every fixture secret.
func (f *Fixture) Expected() map[string][]string {
	out := map[string][]string{}
	for _, s := range f.Secrets {
		out[s.Full()] = Keys(s)
	}
	return out
}

// Secret looks up a fixture secret by full path.
func (f *Fixture) Secret(full string) (Secret, bool) {
	for _, s := range f.Secrets {
		if s.Full() == full {
			return s, true
		}
	}
	return Secret{}, false
}

// Keys returns the sorted key names readable for s.
func Keys(s Secret) []string {
	if s.Deleted {
		return nil
	}
	var ks []string
	for k := range s.Data {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// BulkTeams and BulkServices size the generated part of the tree.
const (
	BulkTeams    = 4
	BulkServices = 25
)

func tree(kv2, kv1 string) []Secret {
	s := []Secret{
		{Mount: kv2, Path: "prod/payments/stripe", Data: map[string]any{"api_key": "sk_live_fixture", "webhook_secret": "whsec_fixture"}},
		{Mount: kv2, Path: "prod/db/postgres", Data: map[string]any{"username": "app", "password": "pg-prod-pass", "host": "db.prod.internal", "port": 5432}},
		// A secret with the same name as a folder.
		{Mount: kv2, Path: "prod/db", Data: map[string]any{"note": "folder and secret share this name"}},
		{Mount: kv2, Path: "staging/db/postgres", Data: map[string]any{"username": "app", "password": "pg-staging-pass"}},
		{Mount: kv2, Path: "deep/a/b/c/d/e/f/g/leaf", Data: map[string]any{"deep_token": "bottom"}},
		{Mount: kv2, Path: "odd/with space/secret name", Data: map[string]any{"key with space": "spaced"}},
		{Mount: kv2, Path: "odd/hash#tag?q", Data: map[string]any{"we#ird": "hash"}},
		{Mount: kv2, Path: "odd/ünïcode/naïve", Data: map[string]any{"clé": "unicode-value"}},
		{Mount: kv2, Path: "odd/json", Data: map[string]any{
			"nested": map[string]any{"a": 1}, "list": []any{1, 2}, "flag": true, "empty": "", "multiline": "line1\nline2",
		}},
		{Mount: kv2, Path: "restricted/hidden/crown-jewels", Data: map[string]any{"top_secret": "nope"}},
		{Mount: kv2, Path: "lifecycle/deleted", Data: map[string]any{"gone_key": "x"}, Deleted: true},
		{Mount: kv2, Path: "lifecycle/destroyed", Data: map[string]any{"destroyed_key": "x"}, Deleted: true},
		{Mount: kv2, Path: "lifecycle/versioned", Data: map[string]any{"new_key": "v2"}},
		{Mount: kv1, Path: "legacy/ldap", Data: map[string]any{"bind_dn": "cn=svc", "bind_password": "ldap-pass"}},
		{Mount: kv1, Path: "legacy/nested/deep/thing", Data: map[string]any{"api_token": "kv1-token"}},
	}
	for t := 0; t < BulkTeams; t++ {
		for i := 0; i < BulkServices; i++ {
			s = append(s, Secret{Mount: kv2, Path: fmt.Sprintf("teams/team-%d/svc-%02d/config", t, i), Data: map[string]any{
				"client_id":                       fmt.Sprintf("id-%d-%d", t, i),
				"client_secret":                   fmt.Sprintf("secret-%d-%d", t, i),
				fmt.Sprintf("unique_%d_%d", t, i): "u",
			}})
		}
	}
	return s
}

// Provision creates mounts, secrets and policies. admin must use a root (or
// equivalently privileged) token.
func Provision(ctx context.Context, admin *Admin, prefix string) (*Fixture, error) {
	f := &Fixture{
		Prefix:   prefix,
		KV2:      prefix + "-kv2/",
		KV1:      prefix + "-kv1/",
		Empty:    prefix + "-empty/",
		Transit:  prefix + "-transit/",
		Reader:   prefix + "-reader",
		ListOnly: prefix + "-listonly",
		NoCubby:  prefix + "-nocubby",
		admin:    admin,
	}
	mounts := []struct {
		path string
		body map[string]any
	}{
		{f.KV2, map[string]any{"type": "kv", "options": map[string]string{"version": "2"}}},
		{f.KV1, map[string]any{"type": "kv", "options": map[string]string{"version": "1"}}},
		{f.Empty, map[string]any{"type": "kv", "options": map[string]string{"version": "2"}}},
		{f.Transit, map[string]any{"type": "transit"}},
	}
	for _, m := range mounts {
		if _, err := admin.write(ctx, "sys/mounts/"+strings.TrimSuffix(m.path, "/"), m.body); err != nil {
			return nil, fmt.Errorf("mount %s: %w", m.path, err)
		}
	}
	f.Secrets = tree(f.KV2, f.KV1)
	for _, s := range f.Secrets {
		if err := f.write(ctx, s); err != nil {
			return nil, err
		}
	}
	if err := f.policies(ctx); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *Fixture) write(ctx context.Context, s Secret) error {
	kv2 := s.Mount != f.KV1
	put := func(data map[string]any) error {
		p, body := s.Mount+s.Path, any(data)
		if kv2 {
			p, body = s.Mount+"data/"+s.Path, map[string]any{"data": data}
		}
		// A fresh KV v2 mount rejects writes until its upgrade finishes.
		var err error
		for i := 0; i < 50; i++ {
			if _, err = f.admin.write(ctx, p, body); err == nil {
				return nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		return fmt.Errorf("write %s: %w", p, err)
	}
	switch s.Path {
	case "lifecycle/versioned":
		// v1 had another key; latest version is what gets indexed.
		if err := put(map[string]any{"old_key": "v1"}); err != nil {
			return err
		}
		return put(s.Data)
	case "lifecycle/deleted":
		if err := put(s.Data); err != nil {
			return err
		}
		return f.admin.delete(ctx, s.Mount+"data/"+s.Path)
	case "lifecycle/destroyed":
		if err := put(s.Data); err != nil {
			return err
		}
		_, err := f.admin.write(ctx, s.Mount+"destroy/"+s.Path, map[string]any{"versions": []int{1}})
		return err
	}
	return put(s.Data)
}

func (f *Fixture) policies(ctx context.Context) error {
	kv2, kv1 := strings.TrimSuffix(f.KV2, "/"), strings.TrimSuffix(f.KV1, "/")
	pol := map[string]string{
		// Lists everything except restricted/, reads prod/ and kv1 only.
		f.Reader: fmt.Sprintf(`
path "%[1]s/metadata/*" { capabilities = ["list"] }
path "%[1]s/metadata/restricted/*" { capabilities = ["deny"] }
path "%[1]s/data/prod/*" { capabilities = ["read"] }
path "%[2]s/*" { capabilities = ["read", "list"] }
`, kv2, kv1),
		// Lists everything, reads nothing.
		f.ListOnly: fmt.Sprintf(`
path "%[1]s/metadata/*" { capabilities = ["list"] }
path "%[2]s/*" { capabilities = ["list"] }
`, kv2, kv1),
		// Used without the default policy: no cubbyhole access.
		f.NoCubby: fmt.Sprintf(`
path "auth/token/lookup-self" { capabilities = ["read"] }
path "sys/internal/ui/mounts" { capabilities = ["read"] }
path "sys/internal/ui/mounts/*" { capabilities = ["read"] }
path "%[1]s/metadata/*" { capabilities = ["list"] }
path "%[1]s/data/*" { capabilities = ["read"] }
`, kv2),
	}
	for name, body := range pol {
		if _, err := f.admin.write(ctx, "sys/policies/acl/"+name, map[string]string{"policy": body}); err != nil {
			return fmt.Errorf("policy %s: %w", name, err)
		}
	}
	return nil
}

// TokenOptions for Token.
type TokenOptions struct {
	Policies        []string
	TTL             time.Duration
	NoDefaultPolicy bool
}

// Token creates a child token of the admin token.
func (f *Fixture) Token(ctx context.Context, o TokenOptions) (string, error) {
	body := map[string]any{"policies": o.Policies, "no_default_policy": o.NoDefaultPolicy}
	if o.TTL > 0 {
		body["ttl"] = o.TTL.String()
	}
	raw, err := f.admin.write(ctx, "auth/token/create", body)
	if err != nil {
		return "", err
	}
	var a struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || a.Auth.ClientToken == "" {
		return "", fmt.Errorf("token create: no token in response (%v)", err)
	}
	return a.Auth.ClientToken, nil
}

// Revoke revokes a token.
func (f *Fixture) Revoke(ctx context.Context, token string) error {
	_, err := f.admin.write(ctx, "auth/token/revoke", map[string]string{"token": token})
	return err
}

// Teardown removes mounts and policies.
func (f *Fixture) Teardown(ctx context.Context) {
	for _, m := range []string{f.KV2, f.KV1, f.Empty, f.Transit} {
		_ = f.admin.delete(ctx, "sys/mounts/"+strings.TrimSuffix(m, "/"))
	}
	for _, p := range []string{f.Reader, f.ListOnly, f.NoCubby} {
		_ = f.admin.delete(ctx, "sys/policies/acl/"+p)
	}
}

// Namespaces is a small tree in two nested namespaces, for servers that
// support them (Vault Enterprise, OpenBao).
type Namespaces struct {
	Parent, Child string // e.g. "it1234-ns", "it1234-ns/child"
	// RootPolicy is a policy in the root namespace granting read/list on
	// both namespaces' secret/ mounts: the "log in at the root, work in a
	// team namespace" setup.
	RootPolicy string
	// Secrets per namespace, in a KV v2 mount named "secret/".
	Secrets map[string][]Secret
	admin   *Admin
}

// ErrNoNamespaces means the server does not support namespaces.
var ErrNoNamespaces = fmt.Errorf("server does not support namespaces")

// ProvisionNamespaces creates <prefix>-ns and <prefix>-ns/child, each with
// a KV v2 mount at secret/, a few secrets and a "reader" policy.
func ProvisionNamespaces(ctx context.Context, admin *Admin, prefix string) (*Namespaces, error) {
	n := &Namespaces{Parent: prefix + "-ns", Child: prefix + "-ns/child", RootPolicy: prefix + "-ns-reader", admin: admin, Secrets: map[string][]Secret{}}
	if _, err := admin.write(ctx, "sys/namespaces/"+n.Parent, map[string]any{}); err != nil {
		if strings.Contains(err.Error(), "HTTP 404") || strings.Contains(err.Error(), "unsupported path") {
			return nil, ErrNoNamespaces
		}
		return nil, err
	}
	if _, err := admin.In(n.Parent).write(ctx, "sys/namespaces/child", map[string]any{}); err != nil {
		return nil, err
	}
	n.Secrets[n.Parent] = []Secret{
		{Mount: "secret/", Path: "team/app/db", Data: map[string]any{"password": "ns-parent-pass", "ns_only_key": "p"}},
		{Mount: "secret/", Path: "team/app/api", Data: map[string]any{"api_key": "ns-parent-api"}},
	}
	n.Secrets[n.Child] = []Secret{
		{Mount: "secret/", Path: "nested/thing", Data: map[string]any{"child_key": "ns-child-value"}},
	}
	for ns, secrets := range n.Secrets {
		a := admin.In(ns)
		if _, err := a.write(ctx, "sys/mounts/secret", map[string]any{"type": "kv", "options": map[string]string{"version": "2"}}); err != nil {
			return nil, fmt.Errorf("mount in %s: %w", ns, err)
		}
		if _, err := a.write(ctx, "sys/policies/acl/reader", map[string]string{
			"policy": `path "secret/*" { capabilities = ["read", "list"] }`,
		}); err != nil {
			return nil, fmt.Errorf("policy in %s: %w", ns, err)
		}
		f := &Fixture{admin: a, KV1: "-"}
		for _, sec := range secrets {
			if err := f.write(ctx, sec); err != nil {
				return nil, fmt.Errorf("namespace %s: %w", ns, err)
			}
		}
	}
	if _, err := admin.write(ctx, "sys/policies/acl/"+n.RootPolicy, map[string]string{"policy": fmt.Sprintf(`
path "%[1]s/secret/*" { capabilities = ["read", "list"] }
path "%[2]s/secret/*" { capabilities = ["read", "list"] }
`, n.Parent, n.Child)}); err != nil {
		return nil, fmt.Errorf("root policy: %w", err)
	}
	return n, nil
}

// Token creates a token inside namespace ns with the given policies.
func (n *Namespaces) Token(ctx context.Context, ns string, o TokenOptions) (string, error) {
	f := &Fixture{admin: n.admin.In(ns)}
	return f.Token(ctx, o)
}

// Teardown deletes both namespaces, child first. Namespace deletion is
// asynchronous, so it waits for each to disappear.
func (n *Namespaces) Teardown(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	parent := n.admin.In(n.Parent)
	_ = parent.delete(ctx, "sys/namespaces/child")
	waitGone(ctx, parent, "child/")
	_ = n.admin.delete(ctx, "sys/namespaces/"+n.Parent)
	waitGone(ctx, n.admin, n.Parent+"/")
	_ = n.admin.delete(ctx, "sys/policies/acl/"+n.RootPolicy)
}

func waitGone(ctx context.Context, a *Admin, name string) {
	for ctx.Err() == nil {
		raw, err := a.doQuery(ctx, http.MethodGet, "sys/namespaces", "?list=true", nil)
		if err != nil {
			return // 404: no namespaces left
		}
		var d struct {
			Data struct {
				Keys []string `json:"keys"`
			} `json:"data"`
		}
		_ = json.Unmarshal(raw, &d)
		found := false
		for _, k := range d.Data.Keys {
			found = found || k == name
		}
		if !found {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
