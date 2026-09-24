package vault

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
