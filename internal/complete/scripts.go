package complete

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Shell support is a registry: each shell is one script in shells/ plus
// one entry in Shells saying how to install it. Adding a shell needs
// nothing else; see shells/README.md.

//go:embed shells/*
var scripts embed.FS

// Shell describes how completion works in one shell.
type Shell struct {
	// Name is what users type (`vaultr completion NAME`) and the base
	// name of $SHELL that selects it.
	Name string
	// File is the script in shells/.
	File string
	// Install is where `vaultr completion install` puts it. Exactly one of
	// RCFile or CompletionFile is set.
	//
	// RCFile: a file under $HOME (or under the directory in RCDirEnv when
	// that variable is set) that gets one line loading the script at
	// startup, `LoadLine` with %s replaced by the shell name. Prefer
	// eval "$(...)" over source <(...): bash 3.2 (macOS) silently reads
	// nothing from source <(...).
	RCFile   string
	RCDirEnv string
	LoadLine string
	// CompletionFile: a path under the user's config directory
	// ($XDG_CONFIG_HOME, else ~/.config) where the script itself is
	// written, for shells that autoload completion files.
	CompletionFile string
}

// Shells lists the supported shells.
var Shells = []Shell{
	{
		Name: "bash", File: "bash.bash",
		RCFile: ".bashrc", LoadLine: `command -v vaultr >/dev/null 2>&1 && eval "$(vaultr completion %s)"`,
	},
	{
		Name: "zsh", File: "zsh.zsh",
		RCFile: ".zshrc", RCDirEnv: "ZDOTDIR", LoadLine: `command -v vaultr >/dev/null 2>&1 && eval "$(vaultr completion %s)"`,
	},
	{
		Name: "fish", File: "fish.fish",
		CompletionFile: "fish/completions/vaultr.fish",
	},
}

// ShellNames returns the supported shell names, sorted.
func ShellNames() []string {
	var out []string
	for _, s := range Shells {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

func lookup(name string) (Shell, error) {
	for _, s := range Shells {
		if s.Name == name {
			return s, nil
		}
	}
	return Shell{}, fmt.Errorf("unsupported shell %q (supported: %s)", name, strings.Join(ShellNames(), ", "))
}

// Script returns the completion script for a shell.
func Script(name string) (string, error) {
	s, err := lookup(name)
	if err != nil {
		return "", err
	}
	b, err := scripts.ReadFile("shells/" + s.File)
	return string(b), err
}

// DetectShell returns the user's shell from $SHELL.
func DetectShell() (string, error) {
	sh := filepath.Base(os.Getenv("SHELL"))
	if sh == "." || sh == "" || sh == string(filepath.Separator) {
		return "", fmt.Errorf("cannot detect your shell ($SHELL is empty): pass one of %s", strings.Join(ShellNames(), ", "))
	}
	if _, err := lookup(sh); err != nil {
		return "", err
	}
	return sh, nil
}

const rcMarker = "# vaultr shell completion"

// Install sets up completion for a shell and returns what it did. Running
// it again changes nothing.
func Install(name string) (string, error) {
	s, err := lookup(name)
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if s.CompletionFile != "" {
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".config")
		}
		p := filepath.Join(dir, filepath.FromSlash(s.CompletionFile))
		script, err := Script(name)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(script), 0o644); err != nil {
			return "", err
		}
		return fmt.Sprintf("wrote %s (%s loads it automatically)", p, name), nil
	}

	base := home
	if s.RCDirEnv != "" && os.Getenv(s.RCDirEnv) != "" {
		base = os.Getenv(s.RCDirEnv)
	}
	rc := filepath.Join(base, s.RCFile)
	existing, err := os.ReadFile(rc)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if strings.Contains(string(existing), rcMarker) {
		return rc + " already loads vaultr completion", nil
	}
	f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	prefix := ""
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n"
	}
	if _, err := fmt.Fprintf(f, "%s%s  %s\n", prefix, fmt.Sprintf(s.LoadLine, name), rcMarker); err != nil {
		return "", err
	}
	return fmt.Sprintf("added to %s; open a new shell or run: source %s", rc, rc), nil
}

// Unescape undoes shell backslash escaping and surrounding quotes in a
// partially typed word, e.g. `odd/with\ space/` or `'odd/with space/`.
func Unescape(w string) string {
	if len(w) > 0 && (w[0] == '\'' || w[0] == '"') {
		w = strings.TrimSuffix(w[1:], w[:1])
	}
	if !strings.Contains(w, `\`) {
		return w
	}
	var b strings.Builder
	for i := 0; i < len(w); i++ {
		if w[i] == '\\' && i+1 < len(w) {
			i++
		}
		b.WriteByte(w[i])
	}
	return b.String()
}
