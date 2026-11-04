package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/atotto/clipboard"

	"github.com/rnsc/vaultr/extension"
	"github.com/rnsc/vaultr/internal/browserext"
	"github.com/rnsc/vaultr/internal/config"
)

func browserNames() string {
	names := make([]string, len(browserext.Browsers))
	for i, b := range browserext.Browsers {
		names[i] = string(b)
	}
	return strings.Join(names, ", ")
}

// extensionCmd is `vaultr extension install|uninstall --browser B`, and
// the release workflow's `vaultr extension export --browser firefox
// --version V DIR`, which writes the files to sign.
func extensionCmd(args []string, stderr io.Writer) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "install", "uninstall", "export":
	default:
		return fmt.Errorf("usage: vaultr extension install|uninstall --browser BROWSER (%s)", browserNames())
	}
	fs := flag.NewFlagSet("extension "+sub, flag.ContinueOnError)
	browser := fs.String("browser", "", browserNames())
	exePath := fs.String("path", "", "the vaultr binary the browser starts (default: this one)")
	ver := fs.String("version", version, "version to stamp on the extension (export)")
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return err
	}
	if *browser == "" {
		return fmt.Errorf("extension %s: --browser is required (%s)", sub, browserNames())
	}
	b, err := browserext.ParseBrowser(*browser)
	if err != nil {
		return err
	}
	if sub == "export" {
		if len(pos) != 1 {
			return errors.New("usage: vaultr extension export --browser BROWSER [--version V] DIR")
		}
		return browserext.WriteExtension(extension.Files, pos[0], b, *ver)
	}
	if len(pos) > 0 {
		return fmt.Errorf("extension %s: unexpected argument %q", sub, pos[0])
	}
	env, err := browserext.CurrentEnv(runtime.GOOS)
	if err != nil {
		return err
	}
	in := &browserext.Installer{
		Env:      env,
		Version:  version,
		Files:    extension.Files,
		XPI:      extension.SignedXPI(),
		Registry: browserext.SystemRegistry(),
		Copy:     copyPath,
	}
	if sub == "uninstall" {
		res, err := in.Uninstall(b)
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "Removed the native messaging host for %s.\n", b.Title())
		for _, a := range res.Also {
			fmt.Fprintln(stderr, a)
		}
		printSteps(stderr, res.Steps)
		return nil
	}

	if in.Exe, err = hostExecutable(*exePath); err != nil {
		return err
	}
	res, err := in.Install(b)
	if err != nil {
		return err
	}
	q := func(p string) string { return browserext.Quote(runtime.GOOS, p) }
	fmt.Fprintf(stderr, "Native messaging host for %s: %s\n", b.Title(), q(res.HostManifest))
	if res.RegistryKey != "" {
		fmt.Fprintf(stderr, "Registered under HKEY_CURRENT_USER\\%s\n", res.RegistryKey)
	}
	for _, a := range res.Also {
		fmt.Fprintln(stderr, a)
	}
	fmt.Fprintf(stderr, "It starts: %s\n", q(in.Exe))
	switch {
	case res.XPI != "":
		fmt.Fprintf(stderr, "Signed add-on: %s\n", q(res.XPI))
	case res.ExtensionDir != "":
		fmt.Fprintf(stderr, "Extension files: %s\n", q(res.ExtensionDir))
	}
	printSteps(stderr, res.Steps)
	for _, w := range configWarnings() {
		fmt.Fprintln(stderr, "warning:", w)
	}
	return nil
}

func printSteps(w io.Writer, steps []string) {
	fmt.Fprintln(w, "\nNext:")
	n := 0
	for _, s := range steps {
		if strings.HasSuffix(s, ":") { // a lead-in, not a step
			fmt.Fprintln(w, "  "+s)
			continue
		}
		n++
		fmt.Fprintf(w, "  %d. %s\n", n, s)
	}
}

// hostExecutable is the vaultr binary the host manifest points to.
func hostExecutable(flagPath string) (string, error) {
	if flagPath != "" {
		p, err := filepath.Abs(flagPath)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("--path: %w", err)
		}
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return browserext.StableExecutable(exe, exec.LookPath, filepath.EvalSymlinks)
}

// configWarnings lists settings this shell has that the browser won't:
// it starts vaultr without the shell's environment.
func configWarnings() []string {
	s, err := config.Load()
	if err != nil {
		return []string{"the config can't be read, so the extension won't work until it is fixed: " + err.Error()}
	}
	var out []string
	var fromEnv []string
	for name, src := range s.Source {
		if v, ok := strings.CutPrefix(src, "env "); ok && name != "token" {
			fromEnv = append(fromEnv, v)
		}
	}
	sort.Strings(fromEnv)
	if len(fromEnv) > 0 {
		out = append(out, fmt.Sprintf("%s set in this shell, but the browser starts vaultr without your shell's environment: put these settings in %s (vaultr config init writes a template)",
			strings.Join(fromEnv, ", "), s.Path))
	}
	if s.Source["address"] == "default" {
		out = append(out, fmt.Sprintf("no Vault address configured: set address in %s (vaultr config init writes a template)", s.Path))
	}
	if os.Getenv("VAULTR_CONFIG") != "" {
		out = append(out, "VAULTR_CONFIG is set in this shell; the browser reads "+defaultConfigWithoutEnv()+" instead")
	}
	if s.Source["token"] == "env VAULT_TOKEN" {
		out = append(out, "the browser doesn't see VAULT_TOKEN: it uses ~/.vault-token (vaultr login saves it there)")
	}
	return out
}

// defaultConfigWithoutEnv is the config path vaultr uses without
// VAULTR_CONFIG.
func defaultConfigWithoutEnv() string {
	v, had := os.LookupEnv("VAULTR_CONFIG")
	os.Unsetenv("VAULTR_CONFIG")
	defer func() {
		if had {
			os.Setenv("VAULTR_CONFIG", v)
		}
	}()
	return config.DefaultPath()
}

// copyPath puts a path on the clipboard, for the browser's file picker.
// Swappable in tests, which must not touch the real clipboard.
var copyPath = clipboard.WriteAll
