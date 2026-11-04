//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/browserext"
	"github.com/rnsc/vaultr/internal/nativehost"
	"github.com/rnsc/vaultr/internal/testvault"
)

// vaultr as the browser starts it: as a native messaging host, with the
// browser's arguments and without the shell's VAULT_* variables. It reads
// ~/.config/vaultr/config.toml and ~/.vault-token from home.

var (
	chromeArgs  = []string{"chrome-extension://" + browserext.ChromeID + "/"}
	firefoxArgs = []string{"/home/u/.mozilla/native-messaging-hosts/be.rnsc.vaultr.json", browserext.GeckoID}
)

// browserHome makes a home folder with a config file (unless cfg is
// empty) and a saved token (unless tok is empty).
func browserHome(t *testing.T, cfg, tok string) string {
	t.Helper()
	home := t.TempDir()
	if cfg != "" {
		p := filepath.Join(home, ".config", "vaultr", "config.toml")
		if runtime.GOOS == "windows" {
			p = filepath.Join(home, "AppData", "Roaming", "vaultr", "config.toml") // %APPDATA%
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if tok != "" {
		if err := os.WriteFile(filepath.Join(home, ".vault-token"), []byte(tok+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// hostConfig is a config file for the fixture mounts.
func hostConfig(t *testing.T, extra string) string {
	t.Helper()
	return fmt.Sprintf("address = %q\nmounts = [%q, %q]\ncache_dir = %q\n%s", addr, fx.KV2, fx.KV1, t.TempDir(), extra)
}

// homeEnv is the environment of a program the browser starts: the user's
// folders, no VAULT_* variables.
func homeEnv(home string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	if runtime.GOOS == "windows" {
		env = append(env,
			"USERPROFILE="+home,
			"APPDATA="+filepath.Join(home, "AppData", "Roaming"),
			"LOCALAPPDATA="+filepath.Join(home, "AppData", "Local"),
			"SYSTEMROOT="+os.Getenv("SYSTEMROOT"), // networking needs it
			"TEMP="+os.Getenv("TEMP"),
		)
	}
	return env
}

type hostProc struct {
	t      *testing.T
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr bytes.Buffer
	id     int64
	frames [][]byte // everything vaultr wrote
}

func startHost(t *testing.T, home string, args []string, env ...string) *hostProc {
	t.Helper()
	h := &hostProc{t: t}
	h.cmd = exec.Command(binPath, args...)
	h.cmd.Env = append(homeEnv(home), env...)
	h.cmd.Stderr = &h.stderr
	var err error
	if h.in, err = h.cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	out, err := h.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	h.out = bufio.NewReader(out)
	if err := h.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	watchdog := time.AfterFunc(2*time.Minute, func() { _ = h.cmd.Process.Kill() })
	t.Cleanup(func() {
		watchdog.Stop()
		h.in.Close()
		_ = h.cmd.Process.Kill()
		_ = h.cmd.Wait()
	})
	return h
}

type hostResp struct {
	ID     int64             `json:"id"`
	Result json.RawMessage   `json:"result"`
	Event  json.RawMessage   `json:"event"`
	Error  *nativehost.Error `json:"error"`
	events []json.RawMessage
	raw    []byte // every frame of the exchange
}

// call sends one request and reads up to its response.
func (h *hostProc) call(r nativehost.Request) hostResp {
	h.t.Helper()
	h.id++
	r.ID = h.id
	if err := nativehost.WriteMsg(h.in, r); err != nil {
		h.t.Fatalf("sending %s: %v", r.Type, err)
	}
	var res hostResp
	for {
		b, err := nativehost.ReadMsg(h.out)
		if err != nil {
			h.t.Fatalf("reading the answer to %s: %v\nstderr: %s", r.Type, err, h.stderr.String())
		}
		h.frames = append(h.frames, b)
		res.raw = append(res.raw, b...)
		var m hostResp
		if err := json.Unmarshal(b, &m); err != nil {
			h.t.Fatalf("not JSON: %q", b)
		}
		if m.ID != r.ID {
			h.t.Fatalf("answer for id %d, want %d: %s", m.ID, r.ID, b)
		}
		if m.Event != nil {
			res.events = append(res.events, m.Event)
			continue
		}
		res.ID, res.Result, res.Error = m.ID, m.Result, m.Error
		return res
	}
}

func ok[T any](t *testing.T, r hostResp) T {
	t.Helper()
	var v T
	if r.Error != nil {
		t.Fatalf("error %s: %s", r.Error.Code, r.Error.Message)
	}
	if err := json.Unmarshal(r.Result, &v); err != nil {
		t.Fatalf("result %s: %v", r.Result, err)
	}
	return v
}

func fails(t *testing.T, r hostResp, code string) {
	t.Helper()
	if r.Error == nil || r.Error.Code != code {
		t.Fatalf("got %+v (result %s), want error %s", r.Error, r.Result, code)
	}
}

// finish closes stdin like the browser does: vaultr must exit 0, having
// written nothing but whole protocol frames, and nothing on stderr.
func (h *hostProc) finish() {
	h.t.Helper()
	h.in.Close()
	rest, _ := io.ReadAll(h.out)
	if err := h.cmd.Wait(); err != nil {
		h.t.Errorf("vaultr exited with %v\nstderr: %s", err, h.stderr.String())
	}
	if len(rest) > 0 {
		h.t.Errorf("stray output after the last answer: %q", rest)
	}
	if h.stderr.Len() > 0 {
		h.t.Errorf("stderr: %s", h.stderr.String())
	}
}

func hello(t *testing.T, h *hostProc) nativehost.HelloResult {
	t.Helper()
	return ok[nativehost.HelloResult](t, h.call(nativehost.Request{Type: "hello", Protocol: nativehost.Protocol, Version: "0.0.0"}))
}

func TestNativeHostSearchAndGet(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{"chrome": chromeArgs, "firefox": firefoxArgs} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tok := token(t, testvault.TokenOptions{Policies: []string{fx.Reader}, TTL: time.Hour})
			home := browserHome(t, hostConfig(t, ""), tok)
			h := startHost(t, home, args)
			hi := hello(t, h)
			if hi.Addr != addr || !hi.ConfigFound || hi.Problem != nil || hi.Protocol != nativehost.Protocol {
				t.Fatalf("hello: %+v", hi)
			}
			if st := ok[nativehost.Status](t, h.call(nativehost.Request{Type: "status"})); !st.LoggedIn || st.TTL <= 0 {
				t.Errorf("status: %+v", st)
			}

			r := h.call(nativehost.Request{Type: "search", Query: "prod postgres k:password"})
			res := ok[nativehost.SearchResult](t, r)
			if len(res.Items) != 1 || res.Items[0].Path != fx.KV2+"prod/db/postgres" || res.Items[0].Key != "password" {
				t.Fatalf("search: %+v", res)
			}
			if len(r.events) == 0 {
				t.Error("no progress events while building the index")
			}
			// Search results never carry values, whatever the query.
			for _, q := range []string{"", "pg-prod-pass", "k:password", "prod"} {
				if b := h.call(nativehost.Request{Type: "search", Query: q, Limit: nativehost.MaxLimit}).raw; bytes.Contains(b, []byte("pg-prod-pass")) || bytes.Contains(b, []byte("sk_live_fixture")) {
					t.Errorf("search %q returned a value: %s", q, b)
				}
			}

			v := ok[nativehost.GetResult](t, h.call(nativehost.Request{Type: "get", Path: fx.KV2 + "prod/db/postgres", Key: "password"}))
			if v.Value != "pg-prod-pass" {
				t.Errorf("get: %q", v.Value)
			}
			if v := ok[nativehost.GetResult](t, h.call(nativehost.Request{Type: "get", Path: fx.KV1 + "legacy/ldap", Key: "bind_password"})); v.Value != "ldap-pass" {
				t.Errorf("get kv1: %q", v.Value)
			}
			if v := ok[nativehost.GetResult](t, h.call(nativehost.Request{Type: "get", Path: fx.KV2 + "prod/db/postgres", Key: "port"})); v.Value != "5432" {
				t.Errorf("get a number: %q", v.Value)
			}
			fails(t, h.call(nativehost.Request{Type: "get", Path: fx.KV2 + "prod/db/postgres", Key: "nope"}), nativehost.CodeNotFound)
			fails(t, h.call(nativehost.Request{Type: "get", Path: fx.KV2 + "restricted/hidden/crown-jewels", Key: "top_secret"}), nativehost.CodeForbidden)

			// Recently filled keys come first on an empty query.
			res = ok[nativehost.SearchResult](t, h.call(nativehost.Request{Type: "search"}))
			if res.Recent != 3 || res.Items[0].Key != "port" || res.Items[1].Path != fx.KV1+"legacy/ldap" || res.Items[2].Key != "password" {
				t.Errorf("recent: %d %+v", res.Recent, res.Items[:2])
			}
			if rr := ok[nativehost.RefreshResult](t, h.call(nativehost.Request{Type: "refresh"})); rr.Secrets < 100 {
				t.Errorf("refresh: %+v", rr)
			}
			h.finish()

			// A new connection (the next popup) loads the saved index and
			// its recent keys without crawling.
			h = startHost(t, home, args)
			hello(t, h)
			r = h.call(nativehost.Request{Type: "search"})
			if res := ok[nativehost.SearchResult](t, r); res.Recent != 3 || len(r.events) != 0 {
				t.Errorf("second connection: recent %d, %d progress events", res.Recent, len(r.events))
			}
			h.finish()
		})
	}
}

func TestNativeHostNoConfig(t *testing.T) {
	t.Parallel()
	h := startHost(t, browserHome(t, "", ""), chromeArgs)
	hi := hello(t, h)
	if hi.Problem == nil || hi.Problem.Code != nativehost.CodeNoConfig || !strings.Contains(hi.Problem.Message, "vaultr config init") || hi.ConfigFound {
		t.Fatalf("hello without config: %+v", hi)
	}
	fails(t, h.call(nativehost.Request{Type: "search", Query: "x"}), nativehost.CodeNoConfig)
	fails(t, h.call(nativehost.Request{Type: "status"}), nativehost.CodeNoConfig)
	h.finish()

	h = startHost(t, browserHome(t, "adress = \"typo\"\n", ""), chromeArgs)
	if hi := hello(t, h); hi.Problem == nil || hi.Problem.Code != nativehost.CodeConfig || !strings.Contains(hi.Problem.Message, "adress") {
		t.Fatalf("hello with a bad config: %+v", hi)
	}
	h.finish()
}

func TestNativeHostProtocolMismatch(t *testing.T) {
	t.Parallel()
	h := startHost(t, browserHome(t, hostConfig(t, ""), ""), chromeArgs)
	fails(t, h.call(nativehost.Request{Type: "hello", Protocol: nativehost.Protocol + 1}), nativehost.CodeProtocol)
	h.finish()
}

func TestNativeHostTokenLogin(t *testing.T) {
	t.Parallel()
	home := browserHome(t, hostConfig(t, ""), "")
	h := startHost(t, home, chromeArgs)
	hello(t, h)
	if st := ok[nativehost.Status](t, h.call(nativehost.Request{Type: "status"})); st.LoggedIn || st.Reason != "none" {
		t.Errorf("status without a token: %+v", st)
	}
	fails(t, h.call(nativehost.Request{Type: "search", Query: "x"}), nativehost.CodeNotLoggedIn)
	fails(t, h.call(nativehost.Request{Type: "login", Method: "ldap"}), nativehost.CodeUseTerminal)
	if r := h.call(nativehost.Request{Type: "login", Method: "token", Token: "hvs.bogus"}); r.Error == nil || strings.Contains(r.Error.Message, "hvs.bogus") {
		t.Errorf("login with a bad token: %+v", r.Error)
	}

	tok := token(t, testvault.TokenOptions{Policies: []string{fx.Reader}, TTL: time.Hour})
	r := h.call(nativehost.Request{Type: "login", Method: "token", Token: tok})
	if lr := ok[nativehost.LoginResult](t, r); !strings.Contains(lr.Summary, "logged in") {
		t.Errorf("login: %+v", lr)
	}
	if bytes.Contains(r.raw, []byte(tok)) {
		t.Error("the login answer echoes the token")
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".vault-token")); strings.TrimSpace(string(b)) != tok {
		t.Error("token not saved to ~/.vault-token")
	}
	if res := ok[nativehost.SearchResult](t, h.call(nativehost.Request{Type: "search", Query: "stripe k:api_key"})); len(res.Items) != 1 {
		t.Errorf("search after login: %+v", res)
	}
	h.finish()
}

func TestNativeHostOIDCLogin(t *testing.T) {
	t.Parallel()
	role, port := oidcRole(t)
	cfg := hostConfig(t, fmt.Sprintf("[auth]\nmethod = \"oidc\"\nmount = %q\nrole = %q\ncallback_port = %d\n", oidcMount, role, port))
	home := browserHome(t, cfg, "")
	browser := fakeBrowser
	if runtime.GOOS == "windows" {
		browser = "curl -fsS -L -o NUL"
	}
	h := startHost(t, home, chromeArgs, "BROWSER="+browser)
	hello(t, h)
	if st := ok[nativehost.Status](t, h.call(nativehost.Request{Type: "status"})); st.LoggedIn || st.AuthMethod != "oidc" {
		t.Errorf("status: %+v", st)
	}
	r := h.call(nativehost.Request{Type: "login"})
	ok[nativehost.LoginResult](t, r)
	if len(r.events) != 1 || !bytes.Contains(r.events[0], []byte(`"login_url"`)) || !bytes.Contains(r.events[0], []byte(idp.URL)) {
		t.Errorf("events: %s", r.events)
	}
	mustBeValid(t, strings.TrimSpace(readFile(t, filepath.Join(home, ".vault-token"))), "")
	if st := ok[nativehost.Status](t, h.call(nativehost.Request{Type: "status"})); !st.LoggedIn {
		t.Errorf("status after login: %+v", st)
	}
	h.finish()
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A `vaultr login` in a terminal while the browser holds a connection:
// the next popup (hello) picks up the new token. An expired token says so.
func TestNativeHostPicksUpNewToken(t *testing.T) {
	t.Parallel()
	home := browserHome(t, hostConfig(t, ""), "hvs.expired-or-bogus")
	h := startHost(t, home, chromeArgs)
	hello(t, h)
	if st := ok[nativehost.Status](t, h.call(nativehost.Request{Type: "status"})); st.LoggedIn || st.Reason != "expired" {
		t.Errorf("status with a dead token: %+v", st)
	}
	fails(t, h.call(nativehost.Request{Type: "search", Query: "x"}), nativehost.CodeNotLoggedIn)
	tok := token(t, testvault.TokenOptions{Policies: []string{fx.Reader}, TTL: time.Hour})
	if err := os.WriteFile(filepath.Join(home, ".vault-token"), []byte(tok), 0o600); err != nil {
		t.Fatal(err)
	}
	hello(t, h)
	if st := ok[nativehost.Status](t, h.call(nativehost.Request{Type: "status"})); !st.LoggedIn {
		t.Errorf("status after a terminal login: %+v", st)
	}
	ok[nativehost.SearchResult](t, h.call(nativehost.Request{Type: "search", Query: "postgres"}))
	h.finish()
}

func TestNativeHostNamespaces(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	cfg := fmt.Sprintf("address = %q\nnamespace = %q\ncache_dir = %q\n", addr, nsx.Parent, t.TempDir())
	h := startHost(t, browserHome(t, cfg, rootLoginToken(t, time.Hour)), chromeArgs)
	if hi := hello(t, h); hi.Namespace != nsx.Parent {
		t.Errorf("hello: %+v", hi)
	}
	nr := ok[nativehost.NamespacesResult](t, h.call(nativehost.Request{Type: "namespaces"}))
	if nr.Configured != nsx.Parent || !strings.Contains(strings.Join(nr.Namespaces, ","), nsx.Child) {
		t.Errorf("namespaces: %+v", nr)
	}
	// The configured namespace by default.
	res := ok[nativehost.SearchResult](t, h.call(nativehost.Request{Type: "search", Query: "k:ns_only_key"}))
	if len(res.Items) != 1 || res.Items[0].Namespace != nsx.Parent || res.Items[0].Path != "secret/team/app/db" {
		t.Errorf("configured namespace: %+v", res.Items)
	}
	// Another one by name.
	child := nsx.Child
	res = ok[nativehost.SearchResult](t, h.call(nativehost.Request{Type: "search", Query: "child_key", Namespace: &child}))
	if len(res.Items) != 1 || res.Items[0].Namespace != nsx.Child {
		t.Errorf("child namespace: %+v", res.Items)
	}
	v := ok[nativehost.GetResult](t, h.call(nativehost.Request{Type: "get", Namespace: &child, Path: "secret/nested/thing", Key: "child_key"}))
	if v.Value != "ns-child-value" {
		t.Errorf("get in the child: %q", v.Value)
	}
	// All of them: each item says where it lives, and get uses that.
	res = ok[nativehost.SearchResult](t, h.call(nativehost.Request{Type: "search", Query: "child k:child_key", AllNamespaces: true}))
	if len(res.Items) != 1 || res.Items[0].Namespace != nsx.Child {
		t.Fatalf("all namespaces: %+v", res.Items)
	}
	it := res.Items[0]
	if v := ok[nativehost.GetResult](t, h.call(nativehost.Request{Type: "get", Namespace: &it.Namespace, Path: it.Path, Key: it.Key})); v.Value != "ns-child-value" {
		t.Errorf("get from an all-namespaces result: %q", v.Value)
	}
	h.finish()
}
