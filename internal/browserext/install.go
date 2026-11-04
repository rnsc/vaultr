package browserext

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// HostManifest is the native messaging host manifest for b, pointing to
// the vaultr binary at exe. Only vaultr's own extension may start it.
func HostManifest(b Browser, exe string) ([]byte, error) {
	m := map[string]any{
		"name":        HostName,
		"description": "vaultr: search Vault KV paths and fill values in the browser",
		"path":        exe,
		"type":        "stdio",
	}
	if b == Firefox {
		m["allowed_extensions"] = []string{GeckoID}
	} else {
		m["allowed_origins"] = []string{"chrome-extension://" + ChromeID + "/"}
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

var releaseVersion = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// ExtensionManifest adapts the shared manifest to one browser family and
// stamps vaultr's version on it. The shared file lists both background
// forms (Chrome's service worker, Firefox's scripts); each browser gets
// only its own, and only the keys it knows, so neither warns.
func ExtensionManifest(src []byte, b Browser, version string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(src, &m); err != nil {
		return nil, fmt.Errorf("extension manifest: %w", err)
	}
	bgm, _ := m["background"].(map[string]any)
	if bgm == nil {
		return nil, errors.New("extension manifest: no background")
	}
	if b.IsChromium() {
		delete(m, "browser_specific_settings")
		delete(bgm, "scripts")
	} else {
		delete(m, "key") // Firefox's ID comes from browser_specific_settings
		delete(bgm, "service_worker")
	}
	// Browsers want 1 to 4 dot-separated numbers: "1.4.2-rc.1" becomes
	// 1.4.2, development builds 0.0.0.
	v := "0.0.0"
	if g := releaseVersion.FindStringSubmatch(version); g != nil {
		v = g[1] + "." + g[2] + "." + g[3]
		if full := strings.TrimPrefix(version, "v"); full != v && b.IsChromium() {
			m["version_name"] = full
		}
	}
	m["version"] = v
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// WriteExtension writes the unpacked extension for b to dir, replacing an
// earlier copy.
func WriteExtension(files fs.FS, dir string, b Browser, version string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := fs.ReadFile(files, p)
		if err != nil {
			return err
		}
		if p == "manifest.json" {
			if data, err = ExtensionManifest(data, b, version); err != nil {
				return err
			}
		}
		return os.WriteFile(dst, data, 0o644)
	})
}

// Registry sets and removes default values of keys under
// HKEY_CURRENT_USER (Windows).
type Registry interface {
	SetDefault(key, value string) error
	Delete(key string) error
}

// Installer installs the extension for one user.
type Installer struct {
	Env     Env
	Exe     string // stable path to the vaultr binary
	Version string // vaultr's
	Files   fs.FS  // the extension's sources
	XPI     []byte // signed Firefox add-on; nil when this build has none
	// Registry registers the host on Windows; nil elsewhere.
	Registry Registry
	// Copy puts the path the user has to pick in the browser on the
	// clipboard, to paste into the file picker (nil: don't).
	Copy func(string) error
}

// Result says what was written, and what the user does next.
type Result struct {
	HostManifest string
	RegistryKey  string // Windows
	ExtensionDir string // unpacked extension, if written
	XPI          string // signed add-on, if written
	// Also: other copies of the host manifest written, or kept on
	// uninstall, with why.
	Also  []string
	Steps []string
}

// Install writes the host manifest (and registers it on Windows) and the
// extension's files for b.
func (in *Installer) Install(b Browser) (Result, error) {
	var res Result
	if in.Env.GOOS == "windows" && in.Registry == nil {
		return res, errors.New("no registry access") // a bug: main sets it
	}
	hm, err := HostManifest(b, in.Exe)
	if err != nil {
		return res, err
	}
	paths := in.Env.HostManifestPaths(b)
	res.HostManifest = paths[0]
	for i, p := range paths {
		if err := writeFile(p, hm); err != nil {
			return res, err
		}
		if i > 0 {
			res.Also = append(res.Also, "Also written to "+Quote(in.Env.GOOS, p)+": "+b.Title()+" sometimes looks there.")
		}
	}
	if in.Env.GOOS == "windows" {
		res.RegistryKey = RegistryKey(b)
		if err := in.Registry.SetDefault(res.RegistryKey, res.HostManifest); err != nil {
			return res, fmt.Errorf("registering the host under HKCU\\%s: %w", res.RegistryKey, err)
		}
	}

	if b == Firefox && in.XPI != nil {
		res.XPI = in.Env.XPIPath()
		if err := writeFile(res.XPI, in.XPI); err != nil {
			return res, err
		}
		hint := in.pick(res.XPI)
		res.Steps = []string{
			"Open the add-on in Firefox and click Add. Either run " + firefoxCommand(in.Env.GOOS, res.XPI) +
				", or drag " + Quote(in.Env.GOOS, res.XPI) + " onto a Firefox window, or use File > Open File. " + hint,
		}
		return res, nil
	}

	res.ExtensionDir = in.Env.ExtensionDir(b)
	if err := WriteExtension(in.Files, res.ExtensionDir, b, in.Version); err != nil {
		return res, err
	}
	if b == Firefox {
		manifest := in.Env.join(res.ExtensionDir, "manifest.json")
		hint := in.pick(manifest)
		res.Steps = []string{
			"This vaultr build has no signed Firefox add-on (release builds include one). To try the extension until Firefox restarts:",
			"Open about:debugging#/runtime/this-firefox.",
			"Click Load Temporary Add-on and pick " + Quote(in.Env.GOOS, manifest) + ". " + hint,
		}
		return res, nil
	}
	hint := in.pick(res.ExtensionDir)
	res.Steps = []string{
		"Open " + b.ExtensionsPage() + " in " + b.Title() + ".",
		"Turn on Developer mode.",
		"Click Load unpacked and pick the folder " + Quote(in.Env.GOOS, res.ExtensionDir) + ". " + hint,
		"If vaultr was already listed, click its reload button instead.",
	}
	return res, nil
}

// pick copies p to the clipboard (when it can) and says how to reach it
// in the browser's file picker: the folders vaultr writes to are hidden
// there on every OS (~/Library, ~/.local, AppData).
func (in *Installer) pick(p string) string {
	copied := in.Copy != nil && in.Copy(p) == nil
	paste := "paste the path"
	if copied {
		paste = "paste the path (vaultr copied it to the clipboard)"
	}
	switch in.Env.GOOS {
	case "darwin":
		return "The picker hides ~/Library: press Cmd+Shift+G, " + paste + " and press Enter."
	case "windows":
		return "The picker hides AppData: " + paste + " into its address bar."
	}
	return "The picker hides folders starting with a dot: press Ctrl+L and " + paste + "."
}

// firefoxCommand is a shell command that opens path in Firefox.
func firefoxCommand(goos, path string) string {
	switch goos {
	case "darwin":
		return "open -a Firefox " + Quote(goos, path)
	case "windows":
		return "start firefox " + Quote(goos, path)
	}
	return "firefox " + Quote(goos, path)
}

// Quote makes a path safe to paste into a shell: macOS puts
// "Application Support" in the way.
func Quote(goos, p string) string {
	if goos == "windows" {
		if strings.ContainsAny(p, " &()^%!") {
			return `"` + p + `"`
		}
		return p
	}
	if p != "" && !strings.ContainsAny(p, " \t'\"$`\\!*?#&;()<>|~{}[]") {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// Uninstall removes what Install wrote for b. Files shared with another
// browser of the same family stay unless all goes.
func (in *Installer) Uninstall(b Browser) (Result, error) {
	var res Result
	res.HostManifest = in.Env.HostManifestPath(b)
	keep := false
	if b == Chrome && in.Env.GOOS == "darwin" {
		// Brave on macOS may read this copy (see HostManifestPaths).
		if _, err := os.Stat(in.Env.HostManifestPath(Brave)); err == nil {
			keep = true
			res.Also = append(res.Also, "Kept "+Quote(in.Env.GOOS, res.HostManifest)+": vaultr is still set up for Brave, which may read it.")
		}
	}
	if !keep {
		if err := os.Remove(res.HostManifest); err != nil && !errors.Is(err, os.ErrNotExist) {
			return res, err
		}
	}
	for _, p := range in.Env.HostManifestPaths(b)[1:] {
		// The copy in another browser's folder may be that browser's own
		// install: leave it, and say how to remove it.
		if _, err := os.Stat(p); err == nil {
			res.Also = append(res.Also, "Left "+Quote(in.Env.GOOS, p)+", which Google Chrome uses too: vaultr extension uninstall --browser chrome removes it.")
		}
	}
	if in.Env.GOOS == "windows" && in.Registry != nil {
		res.RegistryKey = RegistryKey(b)
		if err := in.Registry.Delete(res.RegistryKey); err != nil {
			return res, err
		}
	}
	if b == Firefox {
		res.XPI = in.Env.XPIPath()
		if err := os.Remove(res.XPI); err != nil && !errors.Is(err, os.ErrNotExist) {
			return res, err
		}
	}
	// The unpacked Chromium extension serves several browsers: keep it
	// while another one still has the host installed.
	shared := false
	if b.IsChromium() {
		for _, o := range Browsers {
			if o != b && o.IsChromium() {
				if _, err := os.Stat(in.Env.HostManifestPath(o)); err == nil {
					shared = true
				}
			}
		}
	}
	if !shared {
		res.ExtensionDir = in.Env.ExtensionDir(b)
		if err := os.RemoveAll(res.ExtensionDir); err != nil {
			return res, err
		}
	}
	res.Steps = []string{"Remove vaultr from " + b.ExtensionsPage() + " in " + b.Title() + "."}
	return res, nil
}

func writeFile(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// StableExecutable picks the path the host manifest should point to. It
// must survive upgrades: Homebrew installs each version in its own
// folder (Caskroom/vaultr/1.2.3/vaultr) and links it from its bin
// folder, so the linked path on PATH is the one to use, when it is this
// binary. exe is os.Executable's answer.
func StableExecutable(exe string, lookPath func(string) (string, error), evalSymlinks func(string) (string, error)) (string, error) {
	real, err := evalSymlinks(exe)
	if err != nil {
		real = exe
	}
	onPath := ""
	if p, err := lookPath("vaultr"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if pr, err := evalSymlinks(p); err == nil && pr == real {
			if !versioned(p) {
				return p, nil
			}
		} else {
			onPath = p
		}
	}
	switch {
	case versioned(exe) || versioned(real):
		hint := ""
		if onPath != "" {
			hint = fmt.Sprintf(" (the vaultr on PATH, %s, is another binary)", onPath)
		}
		return "", fmt.Errorf("vaultr runs from %s, a folder that changes with every Homebrew upgrade%s: pass --path with a path that stays, such as $(brew --prefix)/bin/vaultr", real, hint)
	case strings.Contains(filepath.ToSlash(real), "/go-build"):
		return "", errors.New("this vaultr is a temporary build (go run): install it first, or pass --path")
	}
	return exe, nil
}

// versioned reports paths inside a Homebrew version folder.
func versioned(p string) bool {
	p = filepath.ToSlash(p)
	return strings.Contains(p, "/Caskroom/vaultr/") || strings.Contains(p, "/Cellar/vaultr/")
}
