package vault

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNewBadTLSFiles(t *testing.T) {
	if _, err := New(Config{Addr: "https://x", Token: "t", CACert: "/does/not/exist"}); err == nil {
		t.Error("missing CA file accepted")
	}
	p := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(p, []byte("not pem"), 0o600)
	if _, err := New(Config{Addr: "https://x", Token: "t", CACert: p}); err == nil {
		t.Error("CA file without certificates accepted")
	}
}

func TestEscapePath(t *testing.T) {
	cases := map[string]string{
		"secret/data/a/b":     "secret/data/a/b",
		"kv/with space/x":     "kv/with%20space/x",
		"kv/hash#tag?q":       "kv/hash%23tag%3Fq",
		"kv/100%/x":           "kv/100%25/x",
		"kv/ünï":              "kv/%C3%BCn%C3%AF",
		"kv/metadata/folder/": "kv/metadata/folder/",
	}
	for in, want := range cases {
		if got := escapePath(in); got != want {
			t.Errorf("escapePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequestsAndErrors(t *testing.T) {
	var gotPath, gotNS, gotTok, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotNS, gotTok, gotQuery = r.URL.EscapedPath(), r.Header.Get("X-Vault-Namespace"), r.Header.Get("X-Vault-Token"), r.URL.RawQuery
		w.Header().Set("Date", "Mon, 02 Jan 2006 15:04:05 GMT")
		switch r.URL.Path {
		case "/v1/kv/metadata/a b/":
			_, _ = w.Write([]byte(`{"data":{"keys":["x","y/"]}}`))
		case "/v1/kv/data/x":
			_, _ = w.Write([]byte(`{"data":{"data":{"k":"v","n":1},"metadata":{}}}`))
		case "/v1/old/x":
			_, _ = w.Write([]byte(`{"data":{"k":"v1"}}`))
		case "/v1/forbidden":
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
		case "/v1/boom":
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"errors":["internal","explosion"]}`))
		case "/v1/badjson":
			_, _ = w.Write([]byte(`{nope`))
		case "/v1/auth/token/lookup-self":
			_, _ = w.Write([]byte(`{"data":{"accessor":"acc","expire_time":null,"ttl":90}}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"errors":[]}`))
		}
	}))
	defer srv.Close()
	c, _ := New(Config{Addr: srv.URL + "/", Token: "tok", Namespace: "/ns1/"})
	ctx := context.Background()

	keys, err := c.ListSecrets(ctx, Mount{Path: "kv/", KVVersion: 2}, "a b/")
	if err != nil || len(keys) != 2 {
		t.Fatalf("list: %v %v", keys, err)
	}
	if gotPath != "/v1/kv/metadata/a%20b/" || gotQuery != "list=true" || gotNS != "ns1" || gotTok != "tok" {
		t.Errorf("request: path %q query %q ns %q token %q", gotPath, gotQuery, gotNS, gotTok)
	}

	d, err := c.ReadSecret(ctx, Mount{Path: "kv/", KVVersion: 2}, "x")
	if err != nil || d["k"] != "v" {
		t.Errorf("kv2 read: %v %v", d, err)
	}
	if s := Stringify(d); s["n"] != "1" {
		t.Errorf("stringify number: %q", s["n"])
	}
	d, err = c.ReadSecret(ctx, Mount{Path: "old/", KVVersion: 1}, "x")
	if err != nil || d["k"] != "v1" {
		t.Errorf("kv1 read: %v %v", d, err)
	}

	if _, err := c.Read(ctx, "forbidden"); !errors.Is(err, ErrForbidden) {
		t.Errorf("403: %v", err)
	}
	if _, err := c.Read(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("404: %v", err)
	}
	if _, err := c.Read(ctx, "boom"); err == nil || err.Error() != "GET boom: HTTP 500: internal; explosion" {
		t.Errorf("500: %v", err)
	}
	if _, err := c.Read(ctx, "badjson"); err == nil {
		t.Error("malformed JSON accepted")
	}

	ti, err := c.LookupSelf(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantNow := time.Date(2006, 1, 2, 15, 4, 5, 0, time.UTC)
	if !ti.ServerTime.Equal(wantNow) || !ti.ExpireTime.Equal(wantNow.Add(90*time.Second)) {
		t.Errorf("lookup-self: %+v", ti)
	}
}

func TestStringify(t *testing.T) {
	got := Stringify(map[string]any{
		"s": "x", "nil": nil, "b": false, "f": 1.5, "m": map[string]any{"a": "b"}, "l": []any{"a"},
	})
	want := map[string]string{"s": "x", "nil": "", "b": "false", "f": "1.5", "m": `{"a":"b"}`, "l": `["a"]`}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
}

// namespaceServer answers sys/internal/ui/namespaces (direct children, as
// OpenBao does) and token lookups, from a tree of namespace -> children.
func namespaceServer(t *testing.T, tree map[string][]string, uiEndpoint bool) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ns := r.Header.Get("X-Vault-Namespace")
		switch {
		case r.URL.Path == "/v1/auth/token/lookup-self":
			_, _ = w.Write([]byte(`{"data":{"accessor":"a","ttl":3600}}`))
		case r.URL.Path == "/v1/sys/internal/ui/namespaces" && uiEndpoint,
			r.URL.Path == "/v1/sys/namespaces" && r.URL.Query().Get("list") == "true" && tree != nil:
			if ns == "team-a/forbidden" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
				return
			}
			if len(tree[ns]) == 0 {
				w.WriteHeader(http.StatusNotFound) // how Vault answers an empty list
				_, _ = w.Write([]byte(`{"errors":[]}`))
				return
			}
			kids, _ := json.Marshal(tree[ns])
			_, _ = w.Write([]byte(`{"data":{"keys":` + string(kids) + `}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":["1 error occurred:\n\t* unsupported path\n\n"]}`))
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{Addr: srv.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestListNamespaces(t *testing.T) {
	tree := map[string][]string{
		"":                 {"team-a/", "team-b/"},
		"team-a":           {"child/", "forbidden/"},
		"team-a/child":     {"deeper/"},
		"team-a/forbidden": {"hidden/"},
	}
	want := []string{"", "team-a", "team-a/child", "team-a/child/deeper", "team-a/forbidden", "team-b"}
	for _, ui := range []bool{true, false} {
		got, err := namespaceServer(t, tree, ui).ListNamespaces(context.Background())
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ui endpoint %v: %q %v, want %q", ui, got, err, want)
		}
	}

	// A token issued in a namespace sees it and what is below it.
	c := namespaceServer(t, tree, true)
	c.SetToken("t", "team-a")
	got, err := c.ListNamespaces(context.Background())
	if want := []string{"team-a", "team-a/child", "team-a/child/deeper", "team-a/forbidden"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("from team-a: %q %v, want %q", got, err, want)
	}

	// Servers answering with every descendant (nested relative paths).
	flat := map[string][]string{"": {"x/", "x/y/", "x/y/z/"}}
	got, err = namespaceServer(t, flat, true).ListNamespaces(context.Background())
	if want := []string{"", "x", "x/y", "x/y/z"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("nested answer: %q %v, want %q", got, err, want)
	}

	// No namespaces at all (Vault community edition).
	if _, err := namespaceServer(t, nil, false).ListNamespaces(context.Background()); !errors.Is(err, ErrNoNamespaces) {
		t.Errorf("no namespace support: %v", err)
	}
}

func TestInNamespace(t *testing.T) {
	c, _ := New(Config{Addr: "http://x", Token: "t", Namespace: "a"})
	c.SetToken("t2", "root-ish")
	n := c.InNamespace("/b/c/")
	ns, by, ok := n.KnownTokenNamespace()
	if n.Namespace != "b/c" || n.Token() != "t2" || ns != "root-ish" || by != "login" || !ok || c.Namespace != "a" {
		t.Errorf("InNamespace: %+v (%q %q %v), original %q", n, ns, by, ok, c.Namespace)
	}
}

func TestRetryOnRateLimit(t *testing.T) {
	var waits []time.Duration
	orig := sleep
	sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	t.Cleanup(func() { sleep = orig })

	limited := 0 // how many 429s are left to send
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if limited > 0 {
			limited--
			if limited == 1 {
				w.Header().Set("Retry-After", "3")
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errors":["request path \"x\": rate limit quota exceeded"]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	t.Cleanup(srv.Close)
	c, _ := New(Config{Addr: srv.URL, Token: "t"})

	// Three 429s, then success; the body is sent again each time.
	limited = 3
	if _, err := c.Write(context.Background(), "secret/data/x", map[string]string{"a": "b"}); err != nil {
		t.Fatalf("after retries: %v", err)
	}
	if len(bodies) != 4 || bodies[3] != `{"a":"b"}` {
		t.Errorf("requests %q", bodies)
	}
	if len(waits) != 3 || waits[1] != 3*time.Second {
		t.Errorf("waits %v (the second 429 carries Retry-After: 3)", waits)
	}
	for i, w := range waits[:1] {
		if w < 125*time.Millisecond || w > 250*time.Millisecond {
			t.Errorf("backoff %d: %v", i, w)
		}
	}

	// It gives up after maxRetries and reports the 429.
	limited, waits = 100, nil
	_, err := c.Read(context.Background(), "secret/data/x")
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") || len(waits) != maxRetries {
		t.Errorf("err %v after %d waits", err, len(waits))
	}

	// A cancelled context stops the waiting.
	sleep = orig
	limited = 100
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Read(ctx, "secret/data/x"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestRetryDelay(t *testing.T) {
	if d := retryDelay(0, "120"); d != 10*time.Second {
		t.Errorf("Retry-After capped: %v", d)
	}
	if d := retryDelay(20, ""); d > 10*time.Second || d < 5*time.Second {
		t.Errorf("backoff capped: %v", d)
	}
	if d := retryDelay(2, "soon"); d < 500*time.Millisecond || d > time.Second {
		t.Errorf("bad Retry-After falls back to backoff: %v", d)
	}
}

func TestSecretVersions(t *testing.T) {
	var lastQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/secret/metadata/app/db":
			_, _ = w.Write([]byte(`{"data":{"current_version":3,"created_time":"2026-01-01T10:00:00Z","updated_time":"2026-01-03T10:00:00Z",
				"custom_metadata":{"owner":"team-a"},"versions":{
				"3":{"created_time":"2026-01-03T10:00:00Z","deletion_time":"","destroyed":false},
				"1":{"created_time":"2026-01-01T10:00:00Z","deletion_time":"","destroyed":true},
				"2":{"created_time":"2026-01-02T10:00:00Z","deletion_time":"2026-01-02T11:00:00Z","destroyed":false}}}}`))
		case "/v1/secret/data/app/db":
			lastQuery = r.URL.RawQuery
			if r.URL.Query().Get("version") == "2" { // deleted: 404 with metadata, no data
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"data":{"data":null,"metadata":{"version":2}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":{"data":{"password":"v` + r.URL.Query().Get("version") + `"}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c, _ := New(Config{Addr: srv.URL, Token: "t"})
	kv2 := Mount{Path: "secret/", KVVersion: 2}
	ctx := context.Background()

	m, err := c.SecretMetadata(ctx, kv2, "app/db")
	if err != nil || m.Current != 3 || len(m.Versions) != 3 || m.Custom["owner"] != "team-a" {
		t.Fatalf("metadata %+v %v", m, err)
	}
	if m.Versions[0].N != 1 || !m.Versions[0].Destroyed || m.Versions[1].Deleted.IsZero() || !m.Versions[2].Live() {
		t.Errorf("versions %+v", m.Versions)
	}
	if v, ok := m.Version(2); !ok || v.Live() {
		t.Errorf("version 2: %+v %v", v, ok)
	}

	data, err := c.ReadSecretVersion(ctx, kv2, "app/db", 3)
	if err != nil || data["password"] != "v3" || lastQuery != "version=3" {
		t.Errorf("version 3: %v %v (query %q)", data, err, lastQuery)
	}
	if _, err := c.ReadSecretVersion(ctx, kv2, "app/db", 2); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted version: %v", err)
	}
	kv1 := Mount{Path: "kv/", KVVersion: 1}
	if _, err := c.SecretMetadata(ctx, kv1, "x"); !errors.Is(err, ErrNoVersions) {
		t.Errorf("kv1 metadata: %v", err)
	}
	if _, err := c.ReadSecretVersion(ctx, kv1, "x", 1); !errors.Is(err, ErrNoVersions) {
		t.Errorf("kv1 version: %v", err)
	}
}

func TestUIURL(t *testing.T) {
	c, _ := New(Config{Addr: "https://vault.example.com:8200/", Token: "t"})
	if got := c.UIURL(Mount{Path: "secret/", KVVersion: 2}, "app/db"); got != "https://vault.example.com:8200/ui/vault/secrets/secret/show/app/db" {
		t.Errorf("plain: %s", got)
	}
	ns := c.InNamespace("team-a/child")
	if got := ns.UIURL(Mount{Path: "team/kv/", KVVersion: 1}, "odd/with space/hash#tag"); got !=
		"https://vault.example.com:8200/ui/vault/secrets/team%2Fkv/show/odd/with%20space/hash%23tag?namespace=team-a%2Fchild" {
		t.Errorf("namespace, nested mount, odd names: %s", got)
	}
}
