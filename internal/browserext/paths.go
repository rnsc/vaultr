// Package browserext installs vaultr's browser extension: the extension's
// files, and the native messaging host manifest that lets the browser
// start vaultr for it.
package browserext

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"strings"
)

// HostName names vaultr's native messaging host. Browsers allow lowercase
// letters, digits, dots and underscores.
const HostName = "be.rnsc.vaultr"

// GeckoID is the Firefox add-on ID, fixed by browser_specific_settings in
// the manifest and owned by the AMO account that signs it.
const GeckoID = "vaultr@rnsc.be"

// ChromeID is the extension ID in Chromium browsers. Chrome derives it
// from the manifest's public key, so it is the same on every machine,
// which the host manifest's allowed_origins needs. A test checks it
// against the key.
const ChromeID = "gooidaaoebhnijeadicjepdgjakjocpm"

// Browser is a browser vaultr can install for.
type Browser string

// Supported browsers.
const (
	Chrome   Browser = "chrome"
	Chromium Browser = "chromium"
	Brave    Browser = "brave"
	Edge     Browser = "edge"
	Firefox  Browser = "firefox"
)

// Browsers lists them in display order.
var Browsers = []Browser{Chrome, Chromium, Brave, Edge, Firefox}

// ParseBrowser validates a --browser value.
func ParseBrowser(s string) (Browser, error) {
	for _, b := range Browsers {
		if string(b) == strings.ToLower(strings.TrimSpace(s)) {
			return b, nil
		}
	}
	names := make([]string, len(Browsers))
	for i, b := range Browsers {
		names[i] = string(b)
	}
	return "", fmt.Errorf("unknown browser %q (want %s)", s, strings.Join(names, ", "))
}

// IsChromium reports whether b is in the Chrome family, which shares the
// extension format and the host manifest format.
func (b Browser) IsChromium() bool { return b != Firefox }

// Title is the browser's name for people.
func (b Browser) Title() string {
	switch b {
	case Chrome:
		return "Google Chrome"
	case Chromium:
		return "Chromium"
	case Brave:
		return "Brave"
	case Edge:
		return "Microsoft Edge"
	case Firefox:
		return "Firefox"
	}
	return string(b)
}

// ExtensionsPage is where the browser lists extensions.
func (b Browser) ExtensionsPage() string {
	switch b {
	case Brave:
		return "brave://extensions"
	case Edge:
		return "edge://extensions"
	case Firefox:
		return "about:addons"
	}
	return "chrome://extensions"
}

// Env is what install paths depend on, so they can be computed (and
// tested) for any OS.
type Env struct {
	GOOS          string
	Home          string
	XDGConfigHome string // Linux; "" = ~/.config
	XDGDataHome   string // Linux; "" = ~/.local/share
	LocalAppData  string // Windows: %LOCALAPPDATA%
}

// CurrentEnv describes this machine.
func CurrentEnv(goos string) (Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Env{}, err
	}
	return Env{
		GOOS:          goos,
		Home:          home,
		XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"),
		XDGDataHome:   os.Getenv("XDG_DATA_HOME"),
		LocalAppData:  os.Getenv("LOCALAPPDATA"),
	}, nil
}

// join builds a path for env's OS, whatever OS this runs on.
func (e Env) join(parts ...string) string {
	p := path.Join(parts...)
	if e.GOOS == "windows" {
		p = strings.ReplaceAll(p, "/", `\`)
	}
	return p
}

func (e Env) macSupport() string {
	return e.join(e.Home, "Library", "Application Support")
}

func (e Env) linuxConfig() string {
	if e.XDGConfigHome != "" {
		return e.XDGConfigHome
	}
	return e.join(e.Home, ".config")
}

func (e Env) localAppData() string {
	if e.LocalAppData != "" {
		return e.LocalAppData
	}
	return e.join(e.Home, "AppData", "Local")
}

// DataDir is where vaultr keeps the extension's files.
func (e Env) DataDir() string {
	switch e.GOOS {
	case "darwin":
		return e.join(e.macSupport(), "vaultr", "extension")
	case "windows":
		return e.join(e.localAppData(), "vaultr", "extension")
	}
	d := e.XDGDataHome
	if d == "" {
		d = e.join(e.Home, ".local", "share")
	}
	return e.join(d, "vaultr", "extension")
}

// ExtensionDir is the unpacked extension for b: one folder serves every
// Chromium browser, another Firefox (for testing without a signed
// add-on).
func (e Env) ExtensionDir(b Browser) string {
	if b.IsChromium() {
		return e.join(e.DataDir(), "chromium")
	}
	return e.join(e.DataDir(), "firefox")
}

// XPIPath is where the signed Firefox add-on is written.
func (e Env) XPIPath() string {
	return e.join(e.DataDir(), "vaultr.xpi")
}

// HostManifestPath is the native messaging host manifest file for b. On
// macOS and Linux the browser looks in that folder; on Windows the file
// can be anywhere and RegistryKey points to it.
func (e Env) HostManifestPath(b Browser) string {
	file := HostName + ".json"
	switch e.GOOS {
	case "windows":
		family := "chromium"
		if b == Firefox {
			family = "firefox"
		}
		return e.join(e.localAppData(), "vaultr", "NativeMessagingHosts", family, file)
	case "darwin":
		dir := map[Browser]string{
			Chrome:   "Google/Chrome",
			Chromium: "Chromium",
			Brave:    "BraveSoftware/Brave-Browser",
			Edge:     "Microsoft Edge",
			Firefox:  "Mozilla",
		}[b]
		return e.join(e.macSupport(), dir, "NativeMessagingHosts", file)
	}
	// Linux and other Unix systems.
	if b == Firefox {
		return e.join(e.Home, ".mozilla", "native-messaging-hosts", file)
	}
	dir := map[Browser]string{
		Chrome:   "google-chrome",
		Chromium: "chromium",
		Brave:    "BraveSoftware/Brave-Browser",
		Edge:     "microsoft-edge",
	}[b]
	return e.join(e.linuxConfig(), dir, "NativeMessagingHosts", file)
}

// RegistryKey is the key under HKEY_CURRENT_USER whose default value
// points Windows browsers to the host manifest.
func RegistryKey(b Browser) string {
	vendor := map[Browser]string{
		Chrome:   `Google\Chrome`,
		Chromium: `Chromium`,
		Brave:    `BraveSoftware\Brave-Browser`,
		Edge:     `Microsoft\Edge`,
		Firefox:  `Mozilla`,
	}[b]
	return `Software\` + vendor + `\NativeMessagingHosts\` + HostName
}

// ExtensionIDFromKey computes a Chromium extension ID from the manifest's
// "key": the first 128 bits of the SHA-256 of the public key, written
// with the letters a to p for the hex digits 0 to f.
func ExtensionIDFromKey(key string) (string, error) {
	der, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return "", fmt.Errorf("manifest key: %w", err)
	}
	sum := sha256.Sum256(der)
	h := []byte(hex.EncodeToString(sum[:16]))
	for i, c := range h {
		if c >= 'a' {
			h[i] = c - 'a' + 10 + 'a'
		} else {
			h[i] = c - '0' + 'a'
		}
	}
	return string(h), nil
}
