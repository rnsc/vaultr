package nativehost

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/vault"
)

// fakeBackend holds secrets per namespace, values included, so tests can
// check values only ever leave through get.
type fakeBackend struct {
	mu        sync.Mutex
	info      Info
	secrets   map[string]map[string]map[string]string // ns -> path -> key -> value
	expires   time.Time
	loggedIn  bool
	recent    map[string][]string
	indexed   []string // namespaces whose index was loaded, in order
	rebuilt   int
	logins    []string
	reloads   int
	indexErr  error
	block     chan struct{} // Index waits on it (or ctx) when set
	namespace []string
	changed   bool // what the next Reload reports
	nsCalls   int
}

func newFake() *fakeBackend {
	return &fakeBackend{
		info: Info{ConfigPath: "/home/u/.config/vaultr/config.toml", ConfigFound: true, Addr: "https://vault.example", AddrSource: "config", AuthMethod: "oidc"},
		secrets: map[string]map[string]map[string]string{
			"": {
				"secret/prod/db":  {"username": "dbuser", "password": "s3cr3t-db"},
				"secret/prod/api": {"token": "s3cr3t-api"},
				"secret/empty":    {},
			},
			"team-a": {
				"kv/app": {"password": "s3cr3t-team-a"},
			},
		},
		expires:   time.Now().Add(time.Hour),
		loggedIn:  true,
		recent:    map[string][]string{},
		namespace: []string{"", "team-a"},
	}
}

func (f *fakeBackend) Info() Info { f.mu.Lock(); defer f.mu.Unlock(); return f.info }
func (f *fakeBackend) Reload() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads++
	changed := f.changed
	f.changed = false
	return changed
}

func (f *fakeBackend) Status(context.Context) (Status, error) {
	if !f.loggedIn {
		return Status{LoggedIn: false, Reason: "no token", AuthMethod: f.info.AuthMethod}, nil
	}
	return Status{LoggedIn: true, TTL: 3600, AuthMethod: f.info.AuthMethod}, nil
}

func (f *fakeBackend) Namespaces(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nsCalls++
	return f.namespace, nil
}

func (f *fakeBackend) entries(ns string) []index.Entry {
	var out []index.Entry
	for p, keys := range f.secrets[ns] {
		e := index.Entry{Path: p, Mount: p[:strings.IndexByte(p, '/')+1], KV: 2}
		for k := range keys {
			e.Keys = append(e.Keys, k)
		}
		out = append(out, e)
	}
	return out
}

func (f *fakeBackend) Index(ctx context.Context, ns string, rebuild bool, onProgress func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, cache.Header{}, "", ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.loggedIn {
		return nil, cache.Header{}, "", fmt.Errorf("%w; run `vaultr login`", vault.ErrTokenInvalid)
	}
	if f.indexErr != nil {
		return nil, cache.Header{}, "", f.indexErr
	}
	f.indexed = append(f.indexed, ns)
	if rebuild {
		f.rebuilt++
	}
	if onProgress != nil {
		onProgress(index.Progress{Secrets: 3, Lists: 1})
	}
	return f.entries(ns), cache.Header{Created: time.Now(), Expires: f.expires}, "", nil
}

func (f *fakeBackend) AllNamespaces(ctx context.Context, rebuild bool, onProgress func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
	var all []index.Entry
	for _, ns := range f.namespace {
		es, _, _, err := f.Index(ctx, ns, rebuild, onProgress)
		if err != nil {
			return nil, cache.Header{}, "", err
		}
		for _, e := range es {
			e.Namespace = ns
			all = append(all, e)
		}
	}
	return all, cache.Header{Created: time.Now(), Expires: f.expires}, "", nil
}

func (f *fakeBackend) Value(_ context.Context, ns, path, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys, ok := f.secrets[ns][path]
	if !ok {
		return "", fmt.Errorf("%s: %w", path, vault.ErrNotFound)
	}
	v, ok := keys[key]
	if !ok {
		return "", fmt.Errorf("%s has no key %q: %w", path, key, vault.ErrNotFound)
	}
	return v, nil
}

func (f *fakeBackend) Login(_ context.Context, method, token string, openURL func(string)) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logins = append(f.logins, method)
	if method == "oidc" {
		openURL("https://idp.example/authorize")
	}
	if method == "token" && token != "good" {
		return "", "", fmt.Errorf("login failed: %w", vault.ErrForbidden)
	}
	f.loggedIn = true
	return "logged in", "", nil
}

func (f *fakeBackend) Recent(ns string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recent[ns]
}
func (f *fakeBackend) AddRecent(ns, item string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recent[ns] = append([]string{item}, f.recent[ns]...)
}

func ptr(s string) *string { return &s }

type call struct {
	resp   Response
	events []any
}

func do(t *testing.T, h *Handler, req Request) call {
	t.Helper()
	var c call
	c.resp = h.Handle(context.Background(), req, func(ev any) { c.events = append(c.events, ev) })
	if c.resp.ID != req.ID {
		t.Errorf("response id %d, want %d", c.resp.ID, req.ID)
	}
	return c
}

func mustOK[T any](t *testing.T, c call) T {
	t.Helper()
	if c.resp.Error != nil {
		t.Fatalf("error %s: %s", c.resp.Error.Code, c.resp.Error.Message)
	}
	v, ok := c.resp.Result.(T)
	if !ok {
		t.Fatalf("result %T, want %T", c.resp.Result, v)
	}
	return v
}

func mustFail(t *testing.T, c call, code string) {
	t.Helper()
	if c.resp.Error == nil {
		t.Fatalf("no error, want %s (result %+v)", code, c.resp.Result)
	}
	if c.resp.Error.Code != code {
		t.Fatalf("error %s (%s), want %s", c.resp.Error.Code, c.resp.Error.Message, code)
	}
}

func TestHello(t *testing.T) {
	f := newFake()
	h := &Handler{Backend: f, Version: "1.4.2"}
	r := mustOK[HelloResult](t, do(t, h, Request{ID: 1, Type: "hello", Protocol: Protocol, Version: "1.4.2"}))
	if r.Protocol != Protocol || r.Version != "1.4.2" || r.Addr != "https://vault.example" || r.Problem != nil || len(r.Warnings) != 0 {
		t.Errorf("hello: %+v", r)
	}
	if f.reloads != 1 {
		t.Errorf("hello reloaded the config %d times, want 1", f.reloads)
	}
	r = mustOK[HelloResult](t, do(t, h, Request{ID: 2, Type: "hello", Protocol: Protocol, Version: "1.3.0"}))
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "vaultr extension install") {
		t.Errorf("version mismatch warnings: %q", r.Warnings)
	}
	for _, v := range [][2]string{{"0.0.0", "1.4.2"}, {"1.4.2", "dev"}, {"1.4.2", "v1.4.2"}, {"1.4.2", "1.4.2-rc.1"}} {
		if w := versionWarning(v[0], v[1]); w != "" {
			t.Errorf("versionWarning(%q, %q) = %q, want none", v[0], v[1], w)
		}
	}
}

func TestHelloProtocolMismatch(t *testing.T) {
	h := &Handler{Backend: newFake(), Version: "1.4.2"}
	c := do(t, h, Request{ID: 1, Type: "hello", Protocol: Protocol - 1})
	mustFail(t, c, CodeProtocol)
	if !strings.Contains(c.resp.Error.Message, "vaultr extension install") {
		t.Errorf("older extension: %s", c.resp.Error.Message)
	}
	c = do(t, h, Request{ID: 2, Type: "hello", Protocol: Protocol + 1})
	mustFail(t, c, CodeProtocol)
	if !strings.Contains(c.resp.Error.Message, "update vaultr") {
		t.Errorf("newer extension: %s", c.resp.Error.Message)
	}
}

func TestMissingConfig(t *testing.T) {
	for name, tc := range map[string]struct {
		info Info
		code string
		want string
	}{
		"no file":       {Info{ConfigPath: "/c.toml", AddrSource: "default"}, CodeNoConfig, "found no config file at /c.toml"},
		"no address":    {Info{ConfigPath: "/c.toml", ConfigFound: true, AddrSource: "default"}, CodeNoConfig, "/c.toml sets no address"},
		"unreadable":    {Info{ConfigPath: "/c.toml", ConfigError: "/c.toml: unknown setting(s): adress"}, CodeConfig, "adress"},
		"env (browser)": {Info{ConfigPath: "/c.toml", AddrSource: "env VAULT_ADDR"}, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.info = tc.info
			h := &Handler{Backend: f}
			r := mustOK[HelloResult](t, do(t, h, Request{ID: 1, Type: "hello", Protocol: Protocol}))
			if tc.code == "" {
				if r.Problem != nil {
					t.Fatalf("unexpected problem %+v", r.Problem)
				}
				return
			}
			if r.Problem == nil || r.Problem.Code != tc.code || !strings.Contains(r.Problem.Message, tc.want) {
				t.Fatalf("problem %+v, want %s containing %q", r.Problem, tc.code, tc.want)
			}
			if tc.code == CodeNoConfig && !strings.Contains(r.Problem.Message, "shell's environment") {
				t.Errorf("the message should say why VAULT_ADDR is missing: %s", r.Problem.Message)
			}
			for _, typ := range []string{"status", "search", "get", "login", "namespaces"} {
				mustFail(t, do(t, h, Request{ID: 2, Type: typ}), tc.code)
			}
		})
	}
}

func TestStatusAndNamespaces(t *testing.T) {
	f := newFake()
	f.loggedIn = false
	h := &Handler{Backend: f}
	st := mustOK[Status](t, do(t, h, Request{ID: 1, Type: "status"}))
	if st.LoggedIn || st.AuthMethod != "oidc" {
		t.Errorf("status %+v", st)
	}
	f.info.Namespace = "team-a"
	ns := mustOK[NamespacesResult](t, do(t, h, Request{ID: 2, Type: "namespaces"}))
	if len(ns.Namespaces) != 2 || ns.Configured != "team-a" {
		t.Errorf("namespaces %+v", ns)
	}
}

func items(r SearchResult) []string {
	var out []string
	for _, it := range r.Items {
		out = append(out, it.Namespace+"|"+it.Path+"#"+it.Key)
	}
	return out
}

func TestSearch(t *testing.T) {
	f := newFake()
	h := &Handler{Backend: f}
	c := do(t, h, Request{ID: 1, Type: "search", Query: "prod k:pass"})
	r := mustOK[SearchResult](t, c)
	if got := items(r); len(got) != 1 || got[0] != "|secret/prod/db#password" {
		t.Errorf("items %q", got)
	}
	if r.Total != 1 || r.Truncated || r.Expires != f.expires.Unix() {
		t.Errorf("result %+v", r)
	}
	if len(c.events) == 0 {
		t.Error("no progress event while loading the index")
	} else if p, ok := c.events[0].(ProgressEvent); !ok || p.Type != "progress" || p.Secrets != 3 {
		t.Errorf("event %+v", c.events[0])
	}
	// The index stays loaded: a second search doesn't load it again.
	mustOK[SearchResult](t, do(t, h, Request{ID: 2, Type: "search", Query: "api"}))
	if len(f.indexed) != 1 {
		t.Errorf("index loaded %d times, want 1", len(f.indexed))
	}
	// Values never appear in search responses.
	for _, q := range []string{"", "secret", "s3cr3t", "k:password"} {
		b, _ := json.Marshal(do(t, h, Request{ID: 3, Type: "search", Query: q}).resp)
		if bytes.Contains(b, []byte("s3cr3t")) || bytes.Contains(b, []byte("dbuser")) {
			t.Errorf("search %q leaked a value: %s", q, b)
		}
	}
}

func TestSearchNamespaces(t *testing.T) {
	f := newFake()
	f.info.Namespace = "team-a"
	h := &Handler{Backend: f}
	r := mustOK[SearchResult](t, do(t, h, Request{ID: 1, Type: "search", Query: "password"}))
	if got := items(r); len(got) != 1 || got[0] != "team-a|kv/app#password" {
		t.Errorf("configured namespace: %q", got)
	}
	r = mustOK[SearchResult](t, do(t, h, Request{ID: 2, Type: "search", Query: "password", Namespace: ptr("/")}))
	if got := items(r); len(got) != 1 || got[0] != "|secret/prod/db#password" {
		t.Errorf("root namespace: %q", got)
	}
	r = mustOK[SearchResult](t, do(t, h, Request{ID: 3, Type: "search", Query: "password", AllNamespaces: true}))
	if got := items(r); len(got) != 2 {
		t.Errorf("all namespaces: %q", got)
	}
	r = mustOK[SearchResult](t, do(t, h, Request{ID: 4, Type: "search", Query: "team-a pass", AllNamespaces: true}))
	if got := items(r); len(got) != 1 || got[0] != "team-a|kv/app#password" {
		t.Errorf("namespace as a search term: %q", got)
	}
	v := mustOK[GetResult](t, do(t, h, Request{ID: 5, Type: "get", Namespace: ptr("team-a"), Path: "kv/app", Key: "password"}))
	if v.Value != "s3cr3t-team-a" {
		t.Errorf("get in a namespace: %q", v.Value)
	}
}

func TestSearchLimitAndRecent(t *testing.T) {
	f := newFake()
	for i := range 300 {
		f.secrets[""][fmt.Sprintf("secret/bulk/%03d", i)] = map[string]string{"k": "v"}
	}
	h := &Handler{Backend: f}
	r := mustOK[SearchResult](t, do(t, h, Request{ID: 1, Type: "search", Query: "bulk"}))
	if len(r.Items) != DefaultLimit || r.Total != 300 || !r.Truncated {
		t.Errorf("default limit: %d items of %d, truncated %v", len(r.Items), r.Total, r.Truncated)
	}
	r = mustOK[SearchResult](t, do(t, h, Request{ID: 2, Type: "search", Query: "bulk", Limit: 10000}))
	if len(r.Items) != MaxLimit {
		t.Errorf("max limit: %d items", len(r.Items))
	}
	mustOK[GetResult](t, do(t, h, Request{ID: 3, Type: "get", Path: "secret/prod/api", Key: "token"}))
	mustOK[GetResult](t, do(t, h, Request{ID: 4, Type: "get", Path: "/secret/prod/db", Key: "password"}))
	r = mustOK[SearchResult](t, do(t, h, Request{ID: 5, Type: "search"}))
	if got := items(r); r.Recent != 2 || got[0] != "|secret/prod/db#password" || got[1] != "|secret/prod/api#token" {
		t.Errorf("recent first: %d, %q", r.Recent, got[:3])
	}
	r = mustOK[SearchResult](t, do(t, h, Request{ID: 6, Type: "search", Query: "prod"}))
	if r.Recent != 0 {
		t.Errorf("recent with a query: %d", r.Recent)
	}
}

func TestSearchFitsInOneMessage(t *testing.T) {
	f := newFake()
	long := strings.Repeat("x", 20000)
	for i := range MaxLimit {
		f.secrets[""][fmt.Sprintf("secret/%s/%03d", long, i)] = map[string]string{"k": "v"}
	}
	h := &Handler{Backend: f}
	c := do(t, h, Request{ID: 1, Type: "search", Query: "xxx", Limit: MaxLimit})
	r := mustOK[SearchResult](t, c)
	b, err := Encode(c.resp)
	if err != nil || len(b) > MaxOutgoing {
		t.Fatalf("response is %d bytes: %v", len(b), err)
	}
	if len(r.Items) == 0 || len(r.Items) >= MaxLimit || !r.Truncated {
		t.Errorf("%d items, truncated %v", len(r.Items), r.Truncated)
	}
}

func TestIndexExpiryAndRefresh(t *testing.T) {
	f := newFake()
	h := &Handler{Backend: f}
	mustOK[SearchResult](t, do(t, h, Request{ID: 1, Type: "search", Query: "db"}))
	defer func() { now = time.Now }()
	now = func() time.Time { return f.expires.Add(time.Second) }
	mustOK[SearchResult](t, do(t, h, Request{ID: 2, Type: "search", Query: "db"}))
	if len(f.indexed) != 2 {
		t.Errorf("an expired index was not reloaded: %d loads", len(f.indexed))
	}
	now = time.Now
	r := mustOK[RefreshResult](t, do(t, h, Request{ID: 3, Type: "refresh"}))
	if f.rebuilt != 1 || r.Secrets != 3 {
		t.Errorf("refresh: rebuilt %d, %+v", f.rebuilt, r)
	}
}

func TestGet(t *testing.T) {
	f := newFake()
	h := &Handler{Backend: f}
	v := mustOK[GetResult](t, do(t, h, Request{ID: 1, Type: "get", Path: "secret/prod/db", Key: "password"}))
	if v.Value != "s3cr3t-db" {
		t.Errorf("value %q", v.Value)
	}
	if got := f.recent[""]; len(got) != 1 || got[0] != "secret/prod/db#password" {
		t.Errorf("recent %q", got)
	}
	mustFail(t, do(t, h, Request{ID: 2, Type: "get", Path: "secret/prod/db", Key: "nope"}), CodeNotFound)
	mustFail(t, do(t, h, Request{ID: 3, Type: "get", Path: "secret/prod/db"}), CodeBadRequest)
	mustFail(t, do(t, h, Request{ID: 4, Type: "get", Key: "k"}), CodeBadRequest)
	f.secrets[""]["secret/big"] = map[string]string{"blob": strings.Repeat("a", MaxOutgoing)}
	c := do(t, h, Request{ID: 5, Type: "get", Path: "secret/big", Key: "blob"})
	mustFail(t, c, CodeTooLarge)
	if strings.Contains(c.resp.Error.Message, "aaaa") {
		t.Error("the error contains the value")
	}
}

func TestLogin(t *testing.T) {
	f := newFake()
	f.loggedIn = false
	h := &Handler{Backend: f}
	mustFail(t, do(t, h, Request{ID: 1, Type: "search", Query: "db"}), CodeNotLoggedIn)
	c := do(t, h, Request{ID: 2, Type: "login"})
	if r := mustOK[LoginResult](t, c); r.Summary != "logged in" {
		t.Errorf("login %+v", r)
	}
	if len(c.events) != 1 || c.events[0].(LoginURLEvent).URL != "https://idp.example/authorize" {
		t.Errorf("login events %+v", c.events)
	}
	mustOK[SearchResult](t, do(t, h, Request{ID: 3, Type: "search", Query: "db"}))

	mustFail(t, do(t, h, Request{ID: 4, Type: "login", Method: "token"}), CodeBadRequest)
	c = do(t, h, Request{ID: 5, Type: "login", Method: "token", Token: "bad-secret-token"})
	mustFail(t, c, CodeForbidden)
	if strings.Contains(c.resp.Error.Message, "bad-secret-token") {
		t.Error("the error echoes the token")
	}
	mustOK[LoginResult](t, do(t, h, Request{ID: 6, Type: "login", Method: "token", Token: " good\n"}))
	// A login drops the loaded index (it may belong to someone else).
	mustOK[SearchResult](t, do(t, h, Request{ID: 7, Type: "search", Query: "db"}))
	if len(f.indexed) != 2 {
		t.Errorf("index loads after login: %d, want 2", len(f.indexed))
	}
	for _, m := range []string{"ldap", "userpass"} {
		c := do(t, h, Request{ID: 8, Type: "login", Method: m})
		mustFail(t, c, CodeUseTerminal)
		if !strings.Contains(c.resp.Error.Message, "vaultr login") {
			t.Errorf("%s: %s", m, c.resp.Error.Message)
		}
	}
	f.info.AuthMethod = "ldap"
	mustFail(t, do(t, h, Request{ID: 9, Type: "login"}), CodeUseTerminal)
	mustFail(t, do(t, h, Request{ID: 10, Type: "login", Method: "kerberos"}), CodeBadRequest)
	if got := strings.Join(f.logins, ","); got != "oidc,token,token" {
		t.Errorf("backend logins %s", got)
	}
}

func TestErrorCodes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		err  error
		code string
	}{
		{fmt.Errorf("x: %w", vault.ErrTokenInvalid), CodeNotLoggedIn},
		{ErrNotLoggedIn, CodeNotLoggedIn},
		{fmt.Errorf("x: %w", vault.ErrForbidden), CodeForbidden},
		{fmt.Errorf("x: %w", vault.ErrNotFound), CodeNotFound},
		{ErrUseTerminal, CodeUseTerminal},
		{errors.New("connection refused"), CodeOther},
		{&Error{Code: CodeBadRequest, Message: "m"}, CodeBadRequest},
	} {
		if got := errorOf(ctx, tc.err); got.Code != tc.code {
			t.Errorf("%v: %s, want %s", tc.err, got.Code, tc.code)
		}
	}
	f := newFake()
	f.indexErr = errors.New("dial tcp: connection refused")
	mustFail(t, do(t, &Handler{Backend: f}, Request{ID: 1, Type: "search"}), CodeOther)
	mustFail(t, do(t, &Handler{Backend: f}, Request{ID: 1, Type: "nope"}), CodeBadRequest)
}

// session runs Serve over pipes, like the browser does.
type session struct {
	t    *testing.T
	in   *io.PipeWriter
	out  *io.PipeReader
	done chan error
}

func serve(t *testing.T, h *Handler) *session {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &session{t: t, in: inW, out: outR, done: make(chan error, 1)}
	go func() {
		s.done <- h.Serve(context.Background(), inR, outW)
		outW.Close()
	}()
	t.Cleanup(func() { inW.Close(); outR.Close() })
	return s
}

func (s *session) send(v any) {
	s.t.Helper()
	if err := WriteMsg(s.in, v); err != nil {
		s.t.Fatal(err)
	}
}

func (s *session) recv() map[string]any {
	s.t.Helper()
	b, err := ReadMsg(s.out)
	if err != nil {
		s.t.Fatalf("reading a response: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		s.t.Fatal(err)
	}
	return m
}

func (s *session) wait() error {
	s.t.Helper()
	select {
	case err := <-s.done:
		return err
	case <-time.After(5 * time.Second):
		s.t.Fatal("Serve did not return")
		return nil
	}
}

func TestServe(t *testing.T) {
	s := serve(t, &Handler{Backend: newFake(), Version: "1.0.0"})
	s.send(Request{ID: 1, Type: "hello", Protocol: Protocol})
	if m := s.recv(); m["id"] != 1.0 || m["result"].(map[string]any)["version"] != "1.0.0" {
		t.Errorf("hello: %v", m)
	}
	s.send(Request{ID: 2, Type: "search", Query: "db"})
	if m := s.recv(); m["event"].(map[string]any)["type"] != "progress" {
		t.Errorf("want a progress event first: %v", m)
	}
	if m := s.recv(); m["id"] != 2.0 || m["result"] == nil {
		t.Errorf("search: %v", m)
	}
	if _, err := s.in.Write(frame([]byte("{not json"))); err != nil {
		t.Fatal(err)
	}
	if m := s.recv(); m["error"].(map[string]any)["code"] != CodeBadRequest {
		t.Errorf("invalid JSON: %v", m)
	}
	s.in.Close()
	if err := s.wait(); err != nil {
		t.Errorf("Serve after EOF: %v", err)
	}
}

func TestServeCancel(t *testing.T) {
	f := newFake()
	f.block = make(chan struct{})
	s := serve(t, &Handler{Backend: f})
	s.send(Request{ID: 7, Type: "search", Query: "db"})
	s.send(Request{Type: "cancel", Target: 7})
	if m := s.recv(); m["id"] != 7.0 || m["error"].(map[string]any)["code"] != CodeCanceled {
		t.Errorf("canceled search: %v", m)
	}
	close(f.block)
	s.send(Request{ID: 8, Type: "status"})
	if m := s.recv(); m["id"] != 8.0 || m["result"] == nil {
		t.Errorf("after cancel: %v", m)
	}
}

func TestServeOversizedRequest(t *testing.T) {
	s := serve(t, &Handler{Backend: newFake()})
	var hdr [4]byte
	binary.NativeEndian.PutUint32(hdr[:], MaxIncoming+1)
	if _, err := s.in.Write(hdr[:]); err != nil {
		t.Fatal(err)
	}
	if m := s.recv(); m["error"].(map[string]any)["code"] != CodeTooLarge {
		t.Errorf("oversized request: %v", m)
	}
	if err := s.wait(); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Serve: %v", err)
	}
}

func TestServeIdle(t *testing.T) {
	s := serve(t, &Handler{Backend: newFake(), Idle: 50 * time.Millisecond})
	if err := s.wait(); err != nil {
		t.Errorf("idle: %v", err)
	}
}

func TestWriterReplacesOversizedResponses(t *testing.T) {
	var buf bytes.Buffer
	(&writer{w: &buf}).send(Response{ID: 3, Result: GetResult{Value: strings.Repeat("\x01", MaxOutgoing/2)}})
	b, err := ReadMsg(&buf)
	if err != nil {
		t.Fatal(err)
	}
	var m Response
	_ = json.Unmarshal(b, &m)
	if m.ID != 3 || m.Error == nil || m.Error.Code != CodeTooLarge {
		t.Errorf("got %s", b)
	}
}

// Opening the popup again (hello) keeps the loaded index and namespace
// list, unless the config or token changed.
func TestHelloKeepsWhatIsLoaded(t *testing.T) {
	f := newFake()
	h := &Handler{Backend: f}
	hi := Request{ID: 1, Type: "hello", Protocol: Protocol}
	mustOK[HelloResult](t, do(t, h, hi))
	mustOK[SearchResult](t, do(t, h, Request{ID: 2, Type: "search", Query: "db"}))
	mustOK[NamespacesResult](t, do(t, h, Request{ID: 3, Type: "namespaces"}))
	for i := range 3 {
		mustOK[HelloResult](t, do(t, h, hi))
		mustOK[SearchResult](t, do(t, h, Request{ID: int64(10 + i), Type: "search", Query: "api"}))
		mustOK[NamespacesResult](t, do(t, h, Request{ID: int64(20 + i), Type: "namespaces"}))
	}
	if len(f.indexed) != 1 || f.nsCalls != 1 {
		t.Errorf("unchanged setup: index loaded %d times, namespaces listed %d times; want 1 and 1", len(f.indexed), f.nsCalls)
	}
	f.changed = true
	mustOK[HelloResult](t, do(t, h, hi))
	mustOK[SearchResult](t, do(t, h, Request{ID: 30, Type: "search", Query: "db"}))
	mustOK[NamespacesResult](t, do(t, h, Request{ID: 31, Type: "namespaces"}))
	if len(f.indexed) != 2 || f.nsCalls != 2 {
		t.Errorf("changed setup: index loaded %d times, namespaces listed %d times; want 2 and 2", len(f.indexed), f.nsCalls)
	}
	// A refresh lists namespaces again too.
	mustOK[RefreshResult](t, do(t, h, Request{ID: 32, Type: "refresh"}))
	mustOK[NamespacesResult](t, do(t, h, Request{ID: 33, Type: "namespaces"}))
	if f.nsCalls != 3 {
		t.Errorf("after refresh: namespaces listed %d times, want 3", f.nsCalls)
	}
}
