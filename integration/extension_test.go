//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/rnsc/vaultr/extension"
	"github.com/rnsc/vaultr/internal/browserext"
	"github.com/rnsc/vaultr/internal/testvault"
)

// The extension's JavaScript, in headless Chrome: the fill function on
// fixture pages, the popup against a stubbed background, and the whole
// thing (extension, native host, Vault) end to end.

// formPage has a field of every kind fill.js meets. #react mimics React's
// value tracker: an onChange only fires when the DOM value differs from
// what the element's own value setter last saw.
const formPage = `<!doctype html><meta charset="utf-8"><title>form</title>
<input id="plain" name="username">
<input id="pw" type="password" autocomplete="current-password">
<input id="box" type="checkbox">
<input id="ro" readonly value="fixed">
<textarea id="ta"></textarea>
<div id="ce" contenteditable="true"></div>
<input id="react">
<div id="host"></div>
<iframe id="same" src="/frame"></iframe>
<iframe id="cross" src="CROSS/frame"></iframe>
<script>
window.events = [];
for (const type of ["input", "change"]) {
  document.addEventListener(type, (e) => events.push(type + ":" + e.composedPath()[0].id), true);
}
const react = document.getElementById("react");
let tracked = "";
const desc = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
Object.defineProperty(react, "value", {
  configurable: true,
  get() { return desc.get.call(this); },
  set(v) { tracked = String(v); desc.set.call(this, v); },
});
window.reactSaw = null;
react.addEventListener("input", () => {
  if (react.value !== tracked) window.reactSaw = react.value;
  tracked = react.value;
});
const outer = document.getElementById("host").attachShadow({ mode: "open" });
outer.innerHTML = '<div id="host2"></div>';
const inner = outer.getElementById("host2").attachShadow({ mode: "open" });
inner.innerHTML = '<input id="deep" type="email">';
</script>`

const framePage = `<!doctype html><input id="inner" name="otp">`

func formServer(t *testing.T) (same, cross *httptest.Server) {
	t.Helper()
	cross = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, framePage)
	}))
	t.Cleanup(cross.Close)
	// The same server under another name is another origin.
	crossURL := strings.Replace(cross.URL, "127.0.0.1", "localhost", 1)
	same = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/frame" {
			fmt.Fprint(w, framePage)
			return
		}
		fmt.Fprint(w, strings.Replace(formPage, "CROSS", crossURL, 1))
	}))
	t.Cleanup(same.Close)
	return same, cross
}

func evalJS(t *testing.T, tab context.Context, js string, out any) {
	t.Helper()
	await := func(p *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams { return p.WithAwaitPromise(true) }
	if err := chromedp.Run(tab, chromedp.Evaluate(js, out, await)); err != nil {
		t.Fatalf("evaluating %.80q: %v", js, err)
	}
}

type fieldResult struct {
	OK     bool   `json:"ok"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Origin string `json:"origin"`
	Secure bool   `json:"secure"`
	Reason string `json:"reason"`
}

func TestExtensionFill(t *testing.T) {
	t.Parallel()
	fill, err := extension.Files.ReadFile("fill.js")
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := formServer(t)
	tab := browser(t)
	if err := chromedp.Run(tab, chromedp.Navigate(srv.URL), chromedp.WaitReady("#cross")); err != nil {
		t.Fatal(err)
	}
	// Let the frames load.
	if err := chromedp.Run(tab, chromedp.Poll(`document.getElementById("same").contentDocument?.getElementById("inner") != null`, nil)); err != nil {
		t.Fatal(err)
	}
	evalJS(t, tab, string(fill)+";true", nil)
	origin := srv.URL

	run := func(focus, action, value, expect string) fieldResult {
		t.Helper()
		var r fieldResult
		evalJS(t, tab, fmt.Sprintf(`(() => { window.events = []; %s; return vaultrField(%q, %q, %q); })()`, focus, action, value, expect), &r)
		return r
	}
	value := func(js string) string {
		t.Helper()
		var v string
		evalJS(t, tab, js, &v)
		return v
	}

	// Probe describes the field without changing it.
	r := run(`document.getElementById("pw").focus()`, "probe", "", "")
	if !r.OK || r.Kind != "password" || r.Label != "current-password" || r.Origin != origin || !r.Secure {
		t.Errorf("probe password: %+v", r)
	}
	r = run(`document.getElementById("pw").focus()`, "fill", "s3cret", origin)
	if !r.OK || value(`document.getElementById("pw").value`) != "s3cret" {
		t.Errorf("fill password: %+v", r)
	}
	var events []string
	evalJS(t, tab, `events`, &events)
	if strings.Join(events, ",") != "input:pw,change:pw" {
		t.Errorf("events %q", events)
	}

	// React sees the change.
	if r = run(`document.getElementById("react").focus()`, "fill", "from-vault", origin); !r.OK {
		t.Errorf("fill react: %+v", r)
	}
	if v := value(`window.reactSaw`); v != "from-vault" {
		t.Errorf("React's onChange saw %q", v)
	}

	// Through two shadow roots; the events reach the document.
	if r = run(`document.getElementById("host").shadowRoot.getElementById("host2").shadowRoot.getElementById("deep").focus()`, "fill", "a@b.c", origin); !r.OK || r.Kind != "email" {
		t.Errorf("fill in shadow DOM: %+v", r)
	}
	if v := value(`document.getElementById("host").shadowRoot.getElementById("host2").shadowRoot.getElementById("deep").value`); v != "a@b.c" {
		t.Errorf("shadow value %q", v)
	}
	evalJS(t, tab, `events`, &events)
	if strings.Join(events, ",") != "input:deep,change:deep" {
		t.Errorf("shadow events %q", events)
	}

	// A same-origin frame.
	if r = run(`document.getElementById("same").contentDocument.getElementById("inner").focus()`, "fill", "123456", origin); !r.OK {
		t.Errorf("fill in a frame: %+v", r)
	}
	if v := value(`document.getElementById("same").contentDocument.getElementById("inner").value`); v != "123456" {
		t.Errorf("frame value %q", v)
	}

	// Textarea and contenteditable.
	if r = run(`document.getElementById("ta").focus()`, "fill", "multi\nline", origin); !r.OK || value(`document.getElementById("ta").value`) != "multi\nline" {
		t.Errorf("textarea: %+v", r)
	}
	if r = run(`document.getElementById("ce").focus()`, "fill", "edit", origin); !r.OK || value(`document.getElementById("ce").textContent`) != "edit" {
		t.Errorf("contenteditable: %+v", r)
	}

	// Refusals.
	for _, tc := range []struct{ focus, expect, reason string }{
		{`document.getElementById("box").focus()`, origin, "not-text"},
		{`document.getElementById("ro").focus()`, origin, "read-only"},
		{`document.activeElement.blur()`, origin, "none"},
		{`document.getElementById("cross").focus()`, origin, "cross-origin-frame"},
		{`document.getElementById("plain").focus()`, "https://elsewhere.example", "origin-changed"},
	} {
		if r = run(tc.focus, "fill", "nope", tc.expect); r.OK || r.Reason != tc.reason {
			t.Errorf("%s: %+v, want %s", tc.focus, r, tc.reason)
		}
	}
	if v := value(`document.getElementById("plain").value + document.getElementById("ro").value`); v != "fixed" {
		t.Errorf("a refused fill changed a field: %q", v)
	}
}

// popupStub replaces the background: answers come from window.answer,
// which each test sets, and every message sent is kept in window.sent.
const popupStub = `
window.sent = [];
window.didClose = false;
window.close = () => { window.didClose = true; }; // window.closed is built in
const listeners = [];
window.answer = () => undefined;
const port = {
  onMessage: { addListener(f) { listeners.push(f); } },
  postMessage(m) {
    window.sent.push(m);
    Promise.resolve(window.answer(m)).then((r) => {
      if (r !== undefined) listeners.forEach((f) => f(Object.assign({ id: m.id }, r)));
    });
  },
};
window.chrome = {
  runtime: { connect: () => port },
  tabs: { query: async () => [{ id: 7 }] },
  commands: { getAll: async () => [{ name: "_execute_action", shortcut: "Alt+Shift+V" }] },
};
const items = [
  { namespace: "", path: "secret/prod/db", key: "password" },
  { namespace: "", path: "secret/prod/db", key: "username" },
  { namespace: "", path: "secret/prod/api", key: "token" },
];
window.base = (m) => {
  if (m.op === "loginState") return { result: { pending: false } };
  if (m.op === "probe") return { result: { ok: true, kind: "password", label: "pw", origin: "https://app.example", secure: true } };
  if (m.op === "fill") return { result: { ok: true } };
  if (m.op !== "host") return undefined;
  switch (m.type) {
    case "hello": return { result: { protocol: 1, version: "1.0.0", namespace: "", configFound: true } };
    case "status": return { result: { loggedIn: true, authMethod: "oidc" } };
    case "namespaces": return { result: { namespaces: ["", "team-a"], configured: "" } };
    case "search": return { result: { items: items, total: 3, truncated: false, recent: 1 } };
    case "login": return { result: { summary: "logged in" } };
  }
};
window.answer = window.base;
`

// popupTab opens the popup page (served over HTTP, with the stub in place
// of the extension APIs); setup runs before popup.js and can change the
// answers.
func popupTab(t *testing.T, setup string) context.Context {
	t.Helper()
	srv := httptest.NewServer(http.FileServerFS(extension.Files))
	t.Cleanup(srv.Close)
	tab := browser(t)
	if err := chromedp.Run(tab, chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(popupStub + setup).Do(ctx)
		return err
	}), chromedp.Navigate(srv.URL+"/popup.html")); err != nil {
		t.Fatal(err)
	}
	return tab
}

func waitJS(t *testing.T, tab context.Context, cond string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(tab, 10*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Poll(cond, nil, chromedp.WithPollingInterval(20*time.Millisecond))); err != nil {
		var dump string
		_ = chromedp.Run(tab, chromedp.Evaluate(`document.body.innerText + "\nsent: " + JSON.stringify(window.sent)`, &dump))
		t.Fatalf("waiting for %s: %v\npage:\n%s", cond, err, dump)
	}
}

func key(t *testing.T, tab context.Context, k string) {
	t.Helper()
	if err := chromedp.Run(tab, chromedp.KeyEvent(k)); err != nil {
		t.Fatal(err)
	}
}

func TestExtensionPopup(t *testing.T) {
	t.Parallel()
	tab := popupTab(t, "")
	waitJS(t, tab, `document.querySelectorAll("#results li").length === 3`)
	var s string
	evalJS(t, tab, `document.getElementById("target").textContent`, &s)
	if s != "Fills the password field “pw” on https://app.example" {
		t.Errorf("target: %q", s)
	}
	evalJS(t, tab, `document.getElementById("shortcut").textContent`, &s)
	if s != "Shortcut: Alt+Shift+V" {
		t.Errorf("shortcut: %q", s)
	}
	evalJS(t, tab, `document.activeElement.id`, &s)
	if s != "query" {
		t.Errorf("focus on %q", s)
	}
	selected := func() int {
		var i int
		evalJS(t, tab, `[...document.querySelectorAll("#results li")].findIndex((li) => li.getAttribute("aria-selected") === "true")`, &i)
		return i
	}
	key(t, tab, kb.ArrowDown)
	key(t, tab, kb.ArrowDown)
	if i := selected(); i != 2 {
		t.Errorf("after two downs: %d", i)
	}
	key(t, tab, kb.ArrowDown)
	if i := selected(); i != 0 {
		t.Errorf("down wraps: %d", i)
	}
	key(t, tab, kb.ArrowUp)
	if i := selected(); i != 2 {
		t.Errorf("up wraps: %d", i)
	}

	// Typing searches (once, after a pause).
	if err := chromedp.Run(tab, chromedp.SendKeys("#query", "db pass")); err != nil {
		t.Fatal(err)
	}
	waitJS(t, tab, `window.sent.some((m) => m.type === "search" && m.params.query === "db pass")`)
	var n int
	evalJS(t, tab, `window.sent.filter((m) => m.type === "search").length`, &n)
	if n > 3 {
		t.Errorf("%d searches for one typed query: no debounce?", n)
	}

	// Namespaces: the list, and the choice goes with the search.
	evalJS(t, tab, `[...document.querySelectorAll("#namespace option")].map((o) => o.textContent).join("|")`, &s)
	if s != "/ (configured)|team-a|All namespaces" {
		t.Errorf("namespace options %q", s)
	}
	evalJS(t, tab, `(() => { const s = document.getElementById("namespace"); s.value = "ns:team-a"; s.dispatchEvent(new Event("change")); return true; })()`, nil)
	waitJS(t, tab, `window.sent.some((m) => m.type === "search" && m.params.namespace === "team-a")`)
	evalJS(t, tab, `localStorage.getItem("vaultr.scope")`, &s)
	if s != `{"all":false,"ns":"team-a"}` {
		t.Errorf("saved scope %q", s)
	}

	// Enter fills the selected row and closes the popup.
	waitJS(t, tab, `document.querySelectorAll("#results li").length === 3`)
	key(t, tab, kb.ArrowDown)
	key(t, tab, kb.Enter)
	waitJS(t, tab, `window.didClose`)
	var fill map[string]any
	evalJS(t, tab, `window.sent.find((m) => m.op === "fill")`, &fill)
	if fill["tabId"] != 7.0 || fill["origin"] != "https://app.example" || fill["path"] != "secret/prod/db" || fill["key"] != "username" {
		t.Errorf("fill request %v", fill)
	}
	// The popup never asks for a value itself.
	evalJS(t, tab, `JSON.stringify(window.sent)`, &s)
	if strings.Contains(s, `"type":"get"`) {
		t.Errorf("the popup sent a get: %s", s)
	}
}

func TestExtensionPopupEscape(t *testing.T) {
	t.Parallel()
	tab := popupTab(t, "")
	waitJS(t, tab, `document.querySelectorAll("#results li").length === 3`)
	if err := chromedp.Run(tab, chromedp.SendKeys("#query", "x")); err != nil {
		t.Fatal(err)
	}
	key(t, tab, kb.Escape) // clears the query
	var s string
	evalJS(t, tab, `document.getElementById("query").value + "|" + window.didClose`, &s)
	if s != "|false" {
		t.Errorf("first escape: %q", s)
	}
	key(t, tab, kb.Escape) // closes
	waitJS(t, tab, `window.didClose`)
}

func TestExtensionPopupStates(t *testing.T) {
	t.Parallel()
	text := func(tab context.Context, id string) string {
		var s string
		evalJS(t, tab, fmt.Sprintf(`document.getElementById(%q).textContent`, id), &s)
		return s
	}
	t.Run("host not installed", func(t *testing.T) {
		t.Parallel()
		tab := popupTab(t, `window.answer = (m) => m.type === "hello" ? { error: { code: "no_host", message: "x" } } : window.base(m);`)
		waitJS(t, tab, `!document.getElementById("message").hidden`)
		if s := text(tab, "message"); !strings.Contains(s, "vaultr extension install --browser ") {
			t.Errorf("message %q", s)
		}
	})
	t.Run("config problem", func(t *testing.T) {
		t.Parallel()
		tab := popupTab(t, `window.answer = (m) => m.type === "hello" ? { result: { protocol: 1, problem: { code: "no_config", message: "vaultr found no config file" } } } : window.base(m);`)
		waitJS(t, tab, `document.getElementById("message").textContent.includes("no config file")`)
	})
	t.Run("ldap login goes to the terminal", func(t *testing.T) {
		t.Parallel()
		tab := popupTab(t, `window.answer = (m) => m.type === "status" ? { result: { loggedIn: false, reason: "expired", authMethod: "ldap" } } : window.base(m);`)
		waitJS(t, tab, `!document.getElementById("login").hidden`)
		if s := text(tab, "login-why"); !strings.Contains(s, "expired") || !strings.Contains(s, "vaultr login") {
			t.Errorf("login text %q", s)
		}
		var hidden bool
		evalJS(t, tab, `document.getElementById("login-oidc").hidden`, &hidden)
		if !hidden {
			t.Error("OIDC offered for an ldap setup")
		}
	})
	t.Run("token login", func(t *testing.T) {
		t.Parallel()
		tab := popupTab(t, `
let loggedIn = false;
window.answer = (m) => {
  if (m.type === "status") return { result: { loggedIn: loggedIn, reason: "none", authMethod: "oidc" } };
  if (m.type === "login") { loggedIn = true; return { result: { summary: "logged in" } }; }
  return window.base(m);
};`)
		waitJS(t, tab, `!document.getElementById("login").hidden && !document.getElementById("login-oidc").hidden`)
		if err := chromedp.Run(tab, chromedp.SendKeys("#login-token", "hvs.abc"), chromedp.Click("#login-token-form button")); err != nil {
			t.Fatal(err)
		}
		waitJS(t, tab, `document.querySelectorAll("#results li").length === 3`)
		var login map[string]any
		evalJS(t, tab, `window.sent.find((m) => m.type === "login")`, &login)
		if p := login["params"].(map[string]any); p["method"] != "token" || p["token"] != "hvs.abc" {
			t.Errorf("login request %v", login)
		}
		if s := text(tab, "login-token"); s != "" {
			t.Errorf("token left in the form: %q", s)
		}
		var v string
		evalJS(t, tab, `document.getElementById("login-token").value`, &v)
		if v != "" {
			t.Errorf("token left in the input: %q", v)
		}
	})
	t.Run("fill refused", func(t *testing.T) {
		t.Parallel()
		tab := popupTab(t, `window.answer = (m) => m.op === "fill" ? { result: { ok: false, reason: "origin-changed" } } : window.base(m);`)
		waitJS(t, tab, `document.querySelectorAll("#results li").length === 3`)
		key(t, tab, kb.Enter)
		waitJS(t, tab, `document.getElementById("message").textContent.includes("page changed")`)
		var closed bool
		evalJS(t, tab, `window.didClose`, &closed)
		if closed {
			t.Error("the popup closed after a refused fill")
		}
	})
	t.Run("nothing focused", func(t *testing.T) {
		t.Parallel()
		tab := popupTab(t, `window.answer = (m) => m.op === "probe" ? { result: { ok: false, reason: "none" } } : window.base(m);`)
		waitJS(t, tab, `document.getElementById("target").textContent.includes("Click into a text field")`)
	})
}

// extensionChrome finds a Chrome that loads unpacked extensions from the
// command line: Chrome for Testing or Chromium. Google Chrome 137 and
// later ignore --load-extension.
func extensionChrome(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("VAULTR_TEST_EXTENSION_CHROME"); p != "" {
		return p
	}
	if m, _ := filepath.Glob("/opt/pw-browsers/chromium-*/chrome-linux64/chrome"); len(m) > 0 {
		return m[0]
	}
	return chromePath(t)
}

// TestExtensionEndToEnd loads the real extension in Chrome, with vaultr
// as its native host against the dev server, and fills a page's field.
// The popup UI can't be opened headless, so the test drives the
// background the way the popup does, from the popup's page in a tab.
func TestExtensionEndToEnd(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("native host locations in the test are Linux's")
	}
	srv, _ := formServer(t)
	chromeBin := extensionChrome(t)

	// The browser's world: a home with config and token, no VAULT_* vars.
	tok := token(t, testvault.TokenOptions{Policies: []string{fx.Reader}, TTL: time.Hour})
	home := browserHome(t, hostConfig(t, ""), tok)

	ext := filepath.Join(t.TempDir(), "ext")
	if out, err := exec.Command(binPath, "extension", "export", "--browser", "chromium", ext).CombinedOutput(); err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	// Test only: without a user gesture there is no activeTab grant, so
	// let the extension script the fixture server.
	mp := filepath.Join(ext, "manifest.json")
	var m map[string]any
	if err := json.Unmarshal([]byte(readFile(t, mp)), &m); err != nil {
		t.Fatal(err)
	}
	m["host_permissions"] = []string{"http://127.0.0.1/*"}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(mp, b, 0o644); err != nil {
		t.Fatal(err)
	}

	udd := profileDir(t)
	hm, err := browserext.HostManifest(browserext.Chromium, binPath)
	if err != nil {
		t.Fatal(err)
	}
	hmPath := filepath.Join(udd, "NativeMessagingHosts", browserext.HostName+".json")
	if err := os.MkdirAll(filepath.Dir(hmPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hmPath, hm, 0o644); err != nil {
		t.Fatal(err)
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromeBin),
		chromedp.UserDataDir(udd),
		chromedp.Flag("disable-extensions", false),
		chromedp.Flag("load-extension", ext),
		chromedp.Flag("disable-extensions-except", ext),
		// The host inherits the browser's environment: make it the
		// browser's, not this test's.
		chromedp.Env("HOME="+home, "VAULT_ADDR=", "VAULT_TOKEN=", "VAULTR_CONFIG=", "XDG_CONFIG_HOME="),
	)
	// Without the sandbox: a downloaded Chrome for Testing can't use it
	// on Ubuntu 23.10 and later, which restrict the user namespaces it
	// needs. This browser only opens the test's own local pages.
	opts = append(opts, chromedp.NoSandbox)
	actx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	t.Cleanup(cancel)
	page1, cancel := chromedp.NewContext(actx)
	t.Cleanup(cancel)
	ctx, cancel := context.WithTimeout(page1, time.Minute)
	t.Cleanup(cancel)
	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL), chromedp.WaitReady("#react"),
		chromedp.Evaluate(`document.getElementById("react").focus()`, nil)); err != nil {
		t.Fatal(err)
	}

	popup, cancel := chromedp.NewContext(page1)
	t.Cleanup(cancel)
	pctx, cancel := context.WithTimeout(popup, time.Minute)
	t.Cleanup(cancel)
	if err := chromedp.Run(pctx, chromedp.Navigate("chrome-extension://"+browserext.ChromeID+"/popup.html")); err != nil {
		if os.Getenv("VAULTR_REQUIRE_EXTENSION") == "1" {
			t.Fatalf("the extension did not load in %s: %v", chromeBin, err)
		}
		t.Skipf("%s did not load the extension (Google Chrome ignores --load-extension; set VAULTR_TEST_EXTENSION_CHROME to Chrome for Testing): %v", chromeBin, err)
	}

	// The popup's own start: hello and status through the real host. Its
	// tab is the active one, which it can't fill: it says so.
	waitJS(t, pctx, `typeof state !== "undefined" && state.hello && state.hello.addr === `+jsString(addr))
	waitJS(t, pctx, `document.getElementById("target").textContent.includes("can't fill fields on this page")`)
	if err := chromedp.Run(pctx, chromedp.SendKeys("#query", "prod postgres k:password")); err != nil {
		t.Fatal(err)
	}
	waitJS(t, pctx, `[...document.querySelectorAll("#results li")].some((li) => li.textContent.includes("prod/db/postgres"))`)

	var res struct {
		Probe fieldResult    `json:"probe"`
		Fill  fieldResult    `json:"fill"`
		Path  string         `json:"path"`
		Raw   map[string]any `json:"raw"`
	}
	evalJS(t, pctx, `(async () => {
  const tabs = await chrome.tabs.query({});
  const target = tabs.find((x) => x.url && x.url.startsWith(`+jsString(srv.URL)+`));
  const probe = await rpc("probe", { tabId: target.id });
  const found = await host("search", { query: "prod postgres k:password" });
  const it = found.items[0];
  const fill = await rpc("fill", { tabId: target.id, origin: probe.origin, namespace: it.namespace, path: it.path, key: it.key });
  return { probe: probe, fill: fill, path: it.path, raw: { found: found, fill: fill } };
})()`, &res)
	if !res.Probe.OK || res.Probe.Kind != "text" || res.Probe.Origin != srv.URL {
		t.Errorf("probe: %+v", res.Probe)
	}
	if !res.Fill.OK || res.Path != fx.KV2+"prod/db/postgres" {
		t.Fatalf("fill: %+v (%s)", res.Fill, res.Path)
	}
	if raw, _ := json.Marshal(res.Raw); strings.Contains(string(raw), "pg-prod-pass") {
		t.Errorf("the popup received the value: %s", raw)
	}
	var got []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`[document.getElementById("react").value, window.reactSaw]`, &got)); err != nil {
		t.Fatal(err)
	}
	if got[0] != "pg-prod-pass" || got[1] != "pg-prod-pass" {
		t.Errorf("page field and React: %q", got)
	}

	// The popup can't ask for a value directly.
	var refused string
	evalJS(t, pctx, `host("get", { path: "x", key: "y" }).then(() => "allowed", (e) => e.code)`, &refused)
	if refused != "bad_request" {
		t.Errorf("get from the popup: %q", refused)
	}
}

// profileDir is a Chrome profile folder, removed after the test. Not
// t.TempDir: Chrome's helper processes can still write to it for a
// moment after the browser is stopped, and TempDir fails the test when
// its removal does. This retries for a few seconds, then gives up quietly.
func profileDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vaultr-chrome-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 20; i++ {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	})
	return dir
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
