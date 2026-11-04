package browserext

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rnsc/vaultr/extension"
)

func TestChromeIDMatchesManifestKey(t *testing.T) {
	src, err := extension.Files.ReadFile("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(src, &m); err != nil {
		t.Fatal(err)
	}
	id, err := ExtensionIDFromKey(m.Key)
	if err != nil {
		t.Fatal(err)
	}
	if id != ChromeID {
		t.Errorf("the manifest key gives extension ID %s, ChromeID says %s", id, ChromeID)
	}
	if len(id) != 32 || strings.Trim(id, "abcdefghijklmnop") != "" {
		t.Errorf("malformed ID %q", id)
	}
}

func TestParseBrowser(t *testing.T) {
	for _, s := range []string{"chrome", "Chromium", " brave ", "edge", "FIREFOX"} {
		if _, err := ParseBrowser(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	if _, err := ParseBrowser("safari"); err == nil || !strings.Contains(err.Error(), "chrome, chromium, brave, edge, firefox") {
		t.Errorf("safari: %v", err)
	}
}

func TestHostManifestPaths(t *testing.T) {
	linux := Env{GOOS: "linux", Home: "/home/u"}
	xdg := Env{GOOS: "linux", Home: "/home/u", XDGConfigHome: "/cfg", XDGDataHome: "/data"}
	mac := Env{GOOS: "darwin", Home: "/Users/u"}
	win := Env{GOOS: "windows", Home: `C:\Users\u`, LocalAppData: `C:\Users\u\AppData\Local`}
	const f = "/be.rnsc.vaultr.json"
	for _, tc := range []struct {
		env  Env
		b    Browser
		want string
	}{
		{linux, Chrome, "/home/u/.config/google-chrome/NativeMessagingHosts" + f},
		{linux, Chromium, "/home/u/.config/chromium/NativeMessagingHosts" + f},
		{linux, Brave, "/home/u/.config/BraveSoftware/Brave-Browser/NativeMessagingHosts" + f},
		{linux, Edge, "/home/u/.config/microsoft-edge/NativeMessagingHosts" + f},
		{linux, Firefox, "/home/u/.mozilla/native-messaging-hosts" + f},
		{xdg, Chrome, "/cfg/google-chrome/NativeMessagingHosts" + f},
		{xdg, Firefox, "/home/u/.mozilla/native-messaging-hosts" + f},
		{mac, Chrome, "/Users/u/Library/Application Support/Google/Chrome/NativeMessagingHosts" + f},
		{mac, Chromium, "/Users/u/Library/Application Support/Chromium/NativeMessagingHosts" + f},
		{mac, Brave, "/Users/u/Library/Application Support/BraveSoftware/Brave-Browser/NativeMessagingHosts" + f},
		{mac, Edge, "/Users/u/Library/Application Support/Microsoft Edge/NativeMessagingHosts" + f},
		{mac, Firefox, "/Users/u/Library/Application Support/Mozilla/NativeMessagingHosts" + f},
		{win, Chrome, `C:\Users\u\AppData\Local\vaultr\NativeMessagingHosts\chrome\be.rnsc.vaultr.json`},
		{win, Edge, `C:\Users\u\AppData\Local\vaultr\NativeMessagingHosts\edge\be.rnsc.vaultr.json`},
		{win, Firefox, `C:\Users\u\AppData\Local\vaultr\NativeMessagingHosts\firefox\be.rnsc.vaultr.json`},
	} {
		if got := tc.env.HostManifestPath(tc.b); got != tc.want {
			t.Errorf("%s %s:\n got %s\nwant %s", tc.env.GOOS, tc.b, got, tc.want)
		}
	}
}

func TestDataPaths(t *testing.T) {
	for _, tc := range []struct {
		env                    Env
		chromium, firefox, xpi string
	}{
		{Env{GOOS: "linux", Home: "/home/u"}, "/home/u/.local/share/vaultr/extension/chromium", "/home/u/.local/share/vaultr/extension/firefox", "/home/u/.local/share/vaultr/extension/vaultr.xpi"},
		{Env{GOOS: "linux", Home: "/home/u", XDGDataHome: "/data"}, "/data/vaultr/extension/chromium", "/data/vaultr/extension/firefox", "/data/vaultr/extension/vaultr.xpi"},
		{Env{GOOS: "darwin", Home: "/Users/u"}, "/Users/u/Library/Application Support/vaultr/extension/chromium", "/Users/u/Library/Application Support/vaultr/extension/firefox", "/Users/u/Library/Application Support/vaultr/extension/vaultr.xpi"},
		{Env{GOOS: "windows", Home: `C:\Users\u`}, `C:\Users\u\AppData\Local\vaultr\extension\chromium`, `C:\Users\u\AppData\Local\vaultr\extension\firefox`, `C:\Users\u\AppData\Local\vaultr\extension\vaultr.xpi`},
	} {
		if got := tc.env.ExtensionDir(Brave); got != tc.chromium {
			t.Errorf("%s chromium dir: %s", tc.env.GOOS, got)
		}
		if got := tc.env.ExtensionDir(Firefox); got != tc.firefox {
			t.Errorf("%s firefox dir: %s", tc.env.GOOS, got)
		}
		if got := tc.env.XPIPath(); got != tc.xpi {
			t.Errorf("%s xpi: %s", tc.env.GOOS, got)
		}
	}
}

func TestRegistryKeys(t *testing.T) {
	for b, want := range map[Browser]string{
		Chrome:   `Software\Google\Chrome\NativeMessagingHosts\be.rnsc.vaultr`,
		Chromium: `Software\Chromium\NativeMessagingHosts\be.rnsc.vaultr`,
		Brave:    `Software\BraveSoftware\Brave-Browser\NativeMessagingHosts\be.rnsc.vaultr`,
		Edge:     `Software\Microsoft\Edge\NativeMessagingHosts\be.rnsc.vaultr`,
		Firefox:  `Software\Mozilla\NativeMessagingHosts\be.rnsc.vaultr`,
	} {
		if got := RegistryKey(b); got != want {
			t.Errorf("%s: %s", b, got)
		}
	}
}

func TestHostManifest(t *testing.T) {
	var m map[string]any
	b, err := HostManifest(Chrome, "/opt/homebrew/bin/vaultr")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(b, &m)
	if m["name"] != HostName || m["path"] != "/opt/homebrew/bin/vaultr" || m["type"] != "stdio" {
		t.Errorf("chrome: %s", b)
	}
	if o := m["allowed_origins"].([]any); len(o) != 1 || o[0] != "chrome-extension://"+ChromeID+"/" {
		t.Errorf("allowed_origins %v", o)
	}
	if _, ok := m["allowed_extensions"]; ok {
		t.Error("chrome manifest has allowed_extensions")
	}
	m = nil
	b, _ = HostManifest(Firefox, `C:\vaultr\vaultr.exe`)
	_ = json.Unmarshal(b, &m)
	if o := m["allowed_extensions"].([]any); len(o) != 1 || o[0] != GeckoID {
		t.Errorf("allowed_extensions %v", o)
	}
	if m["path"] != `C:\vaultr\vaultr.exe` {
		t.Errorf("path %v", m["path"])
	}
	if _, ok := m["allowed_origins"]; ok {
		t.Error("firefox manifest has allowed_origins")
	}
}

func manifestFor(t *testing.T, b Browser, version string) map[string]any {
	t.Helper()
	src, _ := extension.Files.ReadFile("manifest.json")
	out, err := ExtensionManifest(src, b, version)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestExtensionManifest(t *testing.T) {
	c := manifestFor(t, Chrome, "1.4.2")
	if c["version"] != "1.4.2" || c["key"] == nil || c["browser_specific_settings"] != nil || c["version_name"] != nil {
		t.Errorf("chrome manifest: %v", c)
	}
	bg := c["background"].(map[string]any)
	if bg["service_worker"] != "background.js" || bg["scripts"] != nil {
		t.Errorf("chrome background: %v", bg)
	}
	f := manifestFor(t, Firefox, "v1.4.2")
	bg = f["background"].(map[string]any)
	if f["version"] != "1.4.2" || f["key"] != nil || bg["service_worker"] != nil || bg["scripts"] == nil {
		t.Errorf("firefox manifest: %v", f)
	}
	gecko := f["browser_specific_settings"].(map[string]any)["gecko"].(map[string]any)
	if gecko["id"] != GeckoID {
		t.Errorf("gecko id %v", gecko["id"])
	}
	if v := manifestFor(t, Chrome, "1.5.0-rc.1"); v["version"] != "1.5.0" || v["version_name"] != "1.5.0-rc.1" {
		t.Errorf("prerelease: %v %v", v["version"], v["version_name"])
	}
	if v := manifestFor(t, Chrome, "dev"); v["version"] != "0.0.0" {
		t.Errorf("dev: %v", v["version"])
	}
	// Permissions stay minimal: no host permissions, no content scripts.
	perms := c["permissions"].([]any)
	if len(perms) != 3 || c["host_permissions"] != nil || c["content_scripts"] != nil || c["externally_connectable"] != nil {
		t.Errorf("permissions %v, manifest %v", perms, c)
	}
}

// Every file the extension references is embedded.
func TestEmbeddedFilesComplete(t *testing.T) {
	want := []string{"manifest.json", "background.js", "fill.js", "popup.html", "popup.css", "popup.js",
		"icons/16.png", "icons/32.png", "icons/48.png", "icons/128.png"}
	for _, f := range want {
		if _, err := fs.Stat(extension.Files, f); err != nil {
			t.Errorf("%s not embedded: %v", f, err)
		}
	}
	html, _ := extension.Files.ReadFile("popup.html")
	for _, f := range []string{"popup.css", "popup.js"} {
		if !strings.Contains(string(html), f) {
			t.Errorf("popup.html doesn't load %s", f)
		}
	}
}

type fakeRegistry map[string]string

func (r fakeRegistry) SetDefault(k, v string) error { r[k] = v; return nil }
func (r fakeRegistry) Delete(k string) error        { delete(r, k); return nil }

func testInstaller(t *testing.T) (*Installer, fakeRegistry) {
	t.Helper()
	home := t.TempDir()
	env := Env{GOOS: runtime.GOOS, Home: home, LocalAppData: filepath.Join(home, "AppData", "Local")}
	reg := fakeRegistry{}
	return &Installer{Env: env, Exe: "/usr/local/bin/vaultr", Version: "1.4.2", Files: extension.Files, Registry: reg}, reg
}

func TestInstallChromium(t *testing.T) {
	in, reg := testInstaller(t)
	res, err := in.Install(Chrome)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(res.HostManifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), ChromeID) {
		t.Errorf("host manifest %s", b)
	}
	if runtime.GOOS == "windows" {
		if reg[RegistryKey(Chrome)] != res.HostManifest {
			t.Errorf("registry %v", reg)
		}
	} else if len(reg) != 0 {
		t.Errorf("registry written on %s: %v", runtime.GOOS, reg)
	}
	m, err := os.ReadFile(filepath.Join(res.ExtensionDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(m), `"version": "1.4.2"`) || strings.Contains(string(m), "browser_specific_settings") {
		t.Errorf("written manifest %s", m)
	}
	for _, f := range []string{"popup.js", "icons/128.png"} {
		if _, err := os.Stat(filepath.Join(res.ExtensionDir, filepath.FromSlash(f))); err != nil {
			t.Error(err)
		}
	}
	if _, err := os.Stat(filepath.Join(res.ExtensionDir, "embed.go")); !errors.Is(err, os.ErrNotExist) {
		t.Error("embed.go was written out")
	}
	if steps := strings.Join(res.Steps, "\n"); !strings.Contains(steps, "Developer mode") || !strings.Contains(steps, res.ExtensionDir) || !strings.Contains(steps, "chrome://extensions") {
		t.Errorf("steps:\n%s", steps)
	}

	// Edge shares the unpacked folder: uninstalling Chrome keeps it.
	if _, err := in.Install(Edge); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Uninstall(Chrome); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.HostManifest); !errors.Is(err, os.ErrNotExist) {
		t.Error("chrome host manifest left behind")
	}
	if _, err := os.Stat(res.ExtensionDir); err != nil {
		t.Error("extension folder removed while edge still uses it")
	}
	if _, err := in.Uninstall(Edge); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.ExtensionDir); !errors.Is(err, os.ErrNotExist) {
		t.Error("extension folder left behind")
	}
	if len(reg) != 0 {
		t.Errorf("registry after uninstall: %v", reg)
	}
}

func TestInstallFirefox(t *testing.T) {
	in, _ := testInstaller(t)
	res, err := in.Install(Firefox)
	if err != nil {
		t.Fatal(err)
	}
	if res.XPI != "" || res.ExtensionDir == "" || !strings.Contains(strings.Join(res.Steps, " "), "Load Temporary Add-on") {
		t.Errorf("without a signed add-on: %+v", res)
	}
	b, _ := os.ReadFile(res.HostManifest)
	if !strings.Contains(string(b), GeckoID) {
		t.Errorf("host manifest %s", b)
	}
	in.XPI = []byte("PK signed")
	res, err = in.Install(Firefox)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(res.XPI); string(got) != "PK signed" {
		t.Errorf("xpi %q", got)
	}
	if !strings.Contains(strings.Join(res.Steps, " "), res.XPI) {
		t.Errorf("steps %q", res.Steps)
	}
	if _, err := in.Uninstall(Firefox); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{res.XPI, res.HostManifest} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind", p)
		}
	}
}

func TestStableExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the cases use Unix paths")
	}
	links := map[string]string{
		"/opt/homebrew/bin/vaultr":                                  "/opt/homebrew/Caskroom/vaultr/1.4.2/vaultr",
		"/opt/homebrew/Caskroom/vaultr/1.4.2/vaultr":                "/opt/homebrew/Caskroom/vaultr/1.4.2/vaultr",
		"/opt/homebrew/Caskroom/vaultr/1.3.0/vaultr":                "/opt/homebrew/Caskroom/vaultr/1.3.0/vaultr",
		"/usr/local/bin/vaultr":                                     "/usr/local/bin/vaultr",
		"/tmp/go-build123/b001/exe/vaultr":                          "/tmp/go-build123/b001/exe/vaultr",
		"/home/u/go/bin/vaultr":                                     "/home/u/go/bin/vaultr",
		"/home/linuxbrew/.linuxbrew/bin/vaultr":                     "/home/linuxbrew/.linuxbrew/Cellar/vaultr/1.4.2/bin/vaultr",
		"/home/linuxbrew/.linuxbrew/Cellar/vaultr/1.4.2/bin/vaultr": "/home/linuxbrew/.linuxbrew/Cellar/vaultr/1.4.2/bin/vaultr",
	}
	eval := func(p string) (string, error) {
		if r, ok := links[p]; ok {
			return r, nil
		}
		return "", os.ErrNotExist
	}
	on := func(p string) func(string) (string, error) {
		return func(string) (string, error) {
			if p == "" {
				return "", errors.New("not found")
			}
			return p, nil
		}
	}
	for _, tc := range []struct {
		exe, path, want, err string
	}{
		{"/opt/homebrew/Caskroom/vaultr/1.4.2/vaultr", "/opt/homebrew/bin/vaultr", "/opt/homebrew/bin/vaultr", ""},
		{"/opt/homebrew/bin/vaultr", "/opt/homebrew/bin/vaultr", "/opt/homebrew/bin/vaultr", ""},
		{"/home/linuxbrew/.linuxbrew/Cellar/vaultr/1.4.2/bin/vaultr", "/home/linuxbrew/.linuxbrew/bin/vaultr", "/home/linuxbrew/.linuxbrew/bin/vaultr", ""},
		{"/usr/local/bin/vaultr", "", "/usr/local/bin/vaultr", ""},
		{"/home/u/go/bin/vaultr", "/usr/local/bin/vaultr", "/home/u/go/bin/vaultr", ""},
		{"/opt/homebrew/Caskroom/vaultr/1.3.0/vaultr", "/opt/homebrew/bin/vaultr", "", "is another binary"},
		{"/opt/homebrew/Caskroom/vaultr/1.4.2/vaultr", "", "", "--path"},
		{"/tmp/go-build123/b001/exe/vaultr", "/usr/local/bin/vaultr", "", "go run"},
	} {
		got, err := StableExecutable(tc.exe, on(tc.path), eval)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: %q, %v; want error with %q", tc.exe, got, err, tc.err)
			}
			continue
		}
		if err != nil || filepath.ToSlash(got) != tc.want {
			t.Errorf("%s (PATH has %q): %q, %v; want %s", tc.exe, tc.path, got, err, tc.want)
		}
	}
}

func TestQuote(t *testing.T) {
	for _, tc := range []struct{ goos, in, want string }{
		{"darwin", "/Users/u/Library/Application Support/vaultr/extension/chromium", "'/Users/u/Library/Application Support/vaultr/extension/chromium'"},
		{"linux", "/home/u/.local/share/vaultr/extension/chromium", "/home/u/.local/share/vaultr/extension/chromium"},
		{"linux", "/home/o'brien/x", `'/home/o'\''brien/x'`},
		{"windows", `C:\Users\u\AppData\Local\vaultr\extension\chromium`, `C:\Users\u\AppData\Local\vaultr\extension\chromium`},
		{"windows", `C:\Users\Jane Doe\AppData\Local\vaultr`, `"C:\Users\Jane Doe\AppData\Local\vaultr"`},
	} {
		if got := Quote(tc.goos, tc.in); got != tc.want {
			t.Errorf("%s %q: %s, want %s", tc.goos, tc.in, got, tc.want)
		}
	}
}

func TestPickerHints(t *testing.T) {
	var copied []string
	for goos, want := range map[string]string{
		"darwin":  "Cmd+Shift+G",
		"linux":   "Ctrl+L",
		"windows": "address bar",
	} {
		in := &Installer{Env: Env{GOOS: goos}, Copy: func(p string) error { copied = append(copied, p); return nil }}
		if h := in.pick("/x"); !strings.Contains(h, want) || !strings.Contains(h, "copied it to the clipboard") {
			t.Errorf("%s: %q", goos, h)
		}
		in.Copy = func(string) error { return errors.New("no clipboard") }
		if h := in.pick("/x"); strings.Contains(h, "copied") {
			t.Errorf("%s without a clipboard: %q", goos, h)
		}
	}
	if len(copied) != 3 || copied[0] != "/x" {
		t.Errorf("copied %q", copied)
	}
	for goos, want := range map[string]string{
		"darwin":  "open -a Firefox '/a b/vaultr.xpi'",
		"linux":   "firefox '/a b/vaultr.xpi'",
		"windows": `start firefox "/a b/vaultr.xpi"`,
	} {
		if got := firefoxCommand(goos, "/a b/vaultr.xpi"); got != want {
			t.Errorf("%s: %s", goos, got)
		}
	}
}

func TestInstallCopiesWhatToPick(t *testing.T) {
	in, _ := testInstaller(t)
	var copied []string
	in.Copy = func(p string) error { copied = append(copied, p); return nil }
	res, err := in.Install(Brave)
	if err != nil {
		t.Fatal(err)
	}
	in.XPI = []byte("PK")
	ff, err := in.Install(Firefox)
	if err != nil {
		t.Fatal(err)
	}
	if len(copied) != 2 || copied[0] != res.ExtensionDir || copied[1] != ff.XPI {
		t.Errorf("copied %q, want the extension folder, then the .xpi", copied)
	}
	if steps := strings.Join(ff.Steps, "\n"); !strings.Contains(steps, firefoxCommand(runtime.GOOS, ff.XPI)) {
		t.Errorf("firefox steps lack the open command:\n%s", steps)
	}
}

// Brave on macOS may read Google Chrome's folder: install writes both,
// and uninstalling either leaves what the other still needs.
func TestBraveOnMacAlsoWritesChromesFolder(t *testing.T) {
	env := Env{GOOS: "darwin", Home: t.TempDir()}
	if got := env.HostManifestPaths(Brave); len(got) != 2 || got[1] != env.HostManifestPath(Chrome) {
		t.Fatalf("brave on macOS: %q", got)
	}
	for _, e := range []Env{{GOOS: "linux", Home: "/h"}, {GOOS: "windows", Home: `C:\h`}} {
		if got := e.HostManifestPaths(Brave); len(got) != 1 {
			t.Errorf("brave on %s: %q", e.GOOS, got)
		}
	}
	if got := env.HostManifestPaths(Chrome); len(got) != 1 {
		t.Errorf("chrome: %q", got)
	}

	in := &Installer{Env: env, Exe: "/opt/homebrew/bin/vaultr", Files: extension.Files}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	brave, chrome := env.HostManifestPath(Brave), env.HostManifestPath(Chrome)

	res, err := in.Install(Brave)
	if err != nil {
		t.Fatal(err)
	}
	if !exists(brave) || !exists(chrome) || len(res.Also) != 1 || !strings.Contains(res.Also[0], "Also written to") {
		t.Fatalf("install: brave %v chrome %v, %q", exists(brave), exists(chrome), res.Also)
	}
	// Uninstalling Chrome keeps the copy Brave reads.
	res, err = in.Uninstall(Chrome)
	if err != nil {
		t.Fatal(err)
	}
	if !exists(chrome) || len(res.Also) != 1 || !strings.Contains(res.Also[0], "Kept") {
		t.Errorf("uninstall chrome: chrome %v, %q", exists(chrome), res.Also)
	}
	// Uninstalling Brave removes its own and says how to remove the other.
	res, err = in.Uninstall(Brave)
	if err != nil {
		t.Fatal(err)
	}
	if exists(brave) || !exists(chrome) || len(res.Also) != 1 || !strings.Contains(res.Also[0], "--browser chrome") {
		t.Errorf("uninstall brave: brave %v chrome %v, %q", exists(brave), exists(chrome), res.Also)
	}
	// With Brave gone, uninstalling Chrome removes it.
	if _, err := in.Uninstall(Chrome); err != nil {
		t.Fatal(err)
	}
	if exists(chrome) {
		t.Error("chrome's manifest left after both uninstalls")
	}
}
