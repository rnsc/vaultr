//go:build integration

package integration

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/rnsc/vaultr/internal/testvault"
	"github.com/rnsc/vaultr/internal/vault"
)

// The web UI picks the login method in the browser: the server answers 200
// to any /ui/ address, then the UI rewrites with= (or drops it) when the
// method is not one it can offer. So these tests load `vaultr open`
// addresses in headless Chrome and look at where it ends up.

// chromePath finds Chrome or Chromium: $VAULTR_TEST_CHROME, else the usual
// names. Without one the browser tests skip, unless VAULTR_REQUIRE_BROWSER=1
// (set in CI, where the runners have Chrome).
func chromePath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("VAULTR_TEST_CHROME"); p != "" {
		return p
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	const mac = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := os.Stat(mac); err == nil {
		return mac
	}
	if os.Getenv("VAULTR_REQUIRE_BROWSER") == "1" {
		t.Fatal("no Chrome or Chromium found (set VAULTR_TEST_CHROME)")
	}
	t.Skip("no Chrome or Chromium found (set VAULTR_TEST_CHROME)")
	return ""
}

// browser starts a fresh headless browser (its own profile, so no UI
// session) and returns a tab.
func browser(t *testing.T) context.Context {
	t.Helper()
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chromePath(t)))
	if os.Geteuid() == 0 {
		opts = append(opts, chromedp.NoSandbox) // Chrome's sandbox refuses root (containers)
	}
	actx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	t.Cleanup(cancel)
	tab, cancel := chromedp.NewContext(actx)
	t.Cleanup(cancel)
	tab, cancel = context.WithTimeout(tab, 2*time.Minute)
	t.Cleanup(cancel)
	if err := chromedp.Run(tab); err != nil { // starts the browser
		t.Fatalf("starting %s: %v", chromePath(t), err)
	}
	return tab
}

// settle waits until the page has an address matching done, and that
// address has not changed for a second (the UI rewrites with= a moment after
// the login form renders), then returns it.
func settle(t *testing.T, tab context.Context, done func(*url.URL) bool) *url.URL {
	t.Helper()
	var last string
	stable := 0
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		var cur string
		if err := chromedp.Run(tab, chromedp.Location(&cur)); err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(cur)
		if err == nil && done(u) && cur == last {
			if stable++; stable >= 4 {
				return u
			}
		} else {
			stable = 0
		}
		last = cur
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("the UI did not settle; last address %s", last)
	return nil
}

// loginForm is true on the login page once the UI has rendered its form.
func loginForm(tab context.Context) func(*url.URL) bool {
	return func(u *url.URL) bool {
		if !strings.HasSuffix(u.Path, "/vault/auth") {
			return false
		}
		var n int
		_ = chromedp.Run(tab, chromedp.Evaluate(`document.querySelectorAll('form button[type="submit"]').length`, &n))
		return n > 0
	}
}

func open(t *testing.T, tab context.Context, u string) {
	t.Helper()
	if err := chromedp.Run(tab, chromedp.Navigate(u)); err != nil {
		t.Fatalf("loading %s: %v", u, err)
	}
}

func TestBrowserOpenLandsOnLoginMethod(t *testing.T) {
	t.Parallel()
	chromePath(t)
	sso := loginPageMount(t, "sso-ui")
	// Another listed method that sorts first, so a UI falling back to its
	// first listed method does not land on sso by chance.
	first := fx.Prefix + "-a-userpass"
	if err := testvault.EnableUserpass(ctx(t), admin, "", first, "alice", "alice-pass", []string{fx.Reader}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testvault.DisableAuth(context.Background(), admin, "", first) })
	if err := testvault.ShowOnLoginPage(ctx(t), admin, "", first); err != nil {
		t.Fatal(err)
	}

	var secret testvault.Secret
	for _, s := range fx.Secrets {
		if s.Mount == fx.KV2 && strings.HasPrefix(s.Path, "prod/") && !s.Deleted {
			secret = s
			break
		}
	}
	if secret.Path == "" {
		t.Fatal("no readable KV v2 fixture secret")
	}
	c := client(t, "")
	m := vault.Mount{Path: secret.Mount, KVVersion: 2}
	withLogin := c.UIURL(m, secret.Path, vault.UILogin{Method: "oidc", Mount: sso})
	plain := c.UIURL(m, secret.Path, vault.UILogin{})
	if !strings.Contains(withLogin, "/ui/vault/auth?with=") {
		t.Fatalf("not a login address: %s", withLogin)
	}

	t.Run("logged out, with the method", func(t *testing.T) {
		tab := browser(t)
		open(t, tab, withLogin)
		u := settle(t, tab, loginForm(tab))
		if got := u.Query().Get("with"); got != sso+"/" {
			t.Errorf("the UI offers %q, want %q (%s)", got, sso+"/", u)
		}
		var role int
		_ = chromedp.Run(tab, chromedp.Evaluate(`document.querySelectorAll('input[name="role"]').length`, &role))
		if role == 0 {
			t.Errorf("no OIDC role field on the login form (%s)", u)
		}
		if !strings.Contains(u.Query().Get("redirect_to"), "/show/"+secret.Path) {
			t.Errorf("the login page lost the secret: %s", u)
		}
	})

	t.Run("logged out, without the method", func(t *testing.T) {
		// The control: the secret's own address reaches the same login
		// page, but with the UI's default method.
		tab := browser(t)
		open(t, tab, plain)
		u := settle(t, tab, loginForm(tab))
		if got := u.Query().Get("with"); got == sso+"/" {
			t.Errorf("the UI offers %s without being asked: the test proves nothing (%s)", got, u)
		}
	})

	t.Run("logged in", func(t *testing.T) {
		// A logged-in browser goes through the login page to the secret
		// without showing it.
		tab := browser(t)
		open(t, tab, addr+"/ui/vault/auth?with="+url.QueryEscape(first+"/"))
		settle(t, tab, loginForm(tab))
		err := chromedp.Run(tab,
			chromedp.SendKeys(`input[name="username"]`, "alice", chromedp.ByQuery),
			chromedp.SendKeys(`input[name="password"]`, "alice-pass", chromedp.ByQuery),
			chromedp.Click(`form button[type="submit"]`, chromedp.ByQuery),
		)
		if err != nil {
			t.Fatal(err)
		}
		settle(t, tab, func(u *url.URL) bool { return !strings.HasSuffix(u.Path, "/vault/auth") })

		open(t, tab, withLogin)
		mount := strings.TrimSuffix(secret.Mount, "/")
		u := settle(t, tab, func(u *url.URL) bool {
			// /show/ in OpenBao and older Vault, the KV page in newer Vault.
			return !strings.HasSuffix(u.Path, "/vault/auth") && strings.Contains(u.Path, "/"+mount+"/")
		})
		if p := u.EscapedPath(); !strings.Contains(p, "prod") {
			t.Errorf("landed on %s, not the secret", u)
		}
	})
}

func TestBrowserOpenAcrossNamespaces(t *testing.T) {
	t.Parallel()
	chromePath(t)
	needNamespaces(t)
	// The login method is in the root namespace and the secret in a child
	// one: the login page must look for the method in the root.
	sso := loginPageMount(t, "sso-ns")
	s := nsx.Secrets[nsx.Child][0]
	c := client(t, "").InNamespace(nsx.Child)
	tab := browser(t)
	open(t, tab, c.UIURL(vault.Mount{Path: s.Mount, KVVersion: 2}, s.Path, vault.UILogin{Method: "oidc", Mount: sso}))
	u := settle(t, tab, loginForm(tab))
	if u.Query().Get("with") != sso+"/" || u.Query().Get("namespace") != "" {
		t.Errorf("login page: %s", u)
	}
	if r := u.Query().Get("redirect_to"); !strings.Contains(r, "namespace="+url.QueryEscape(nsx.Child)) && !strings.Contains(r, "namespace="+nsx.Child) {
		t.Errorf("the secret's namespace was lost: %s", u)
	}
}
