// Package complete computes shell completion candidates for the vaultr
// command line. It knows the command grammar; secret paths and key names
// come from a Source (the cached index, or live Vault listings).
package complete

import (
	"context"
	"sort"
	"strings"
)

// Source supplies secret paths and key names.
type Source interface {
	// Children returns the entries directly under dir, which is "" (the
	// mount level) or a logical path ending in "/", e.g. "secret/prod/".
	// Folders end with "/". The second result is false when the source
	// cannot answer (no cache, no access).
	Children(ctx context.Context, dir string) ([]string, bool)
	// Keys returns the key names of the secret at path.
	Keys(ctx context.Context, path string) ([]string, bool)
}

// Commands and their flags, for completing the first word and options.
var (
	Commands = []string{"find", "get", "login", "index", "refresh", "status", "purge", "config", "completion", "version", "help"}

	flags = map[string][]string{
		"find":  {"--json", "--values", "-n", "-r", "--refresh"},
		"get":   {"--json"},
		"login": {"-method", "-mount", "-namespace", "-username", "-role", "-callback-port", "-no-save"},
	}
	subcommands = map[string][]string{
		"config": {"show", "path", "init"},
	}
	aliases = map[string]string{"search": "find", "f": "find", "g": "get", "reindex": "index"}
)

// Candidates returns completions for the last word of args, the words
// typed after "vaultr" (the last one possibly empty). Folder candidates end
// with "/": shells should not add a space after them. Sources are tried in
// order until one has a match, so a stale cache falls back to Vault for
// secrets added since it was built.
func Candidates(ctx context.Context, sources []Source, args []string) []string {
	if len(args) == 0 {
		args = []string{""}
	}
	cur := args[len(args)-1]
	prev := args[:len(args)-1]
	if len(prev) == 0 {
		return filter(Commands, cur)
	}
	cmd := prev[0]
	if a, ok := aliases[cmd]; ok {
		cmd = a
	}
	if strings.HasPrefix(cur, "-") {
		return filter(flags[cmd], cur)
	}
	switch cmd {
	case "get":
		pos := positional(prev[1:], nil)
		switch len(pos) {
		case 0:
			return firstMatch(sources, func(src Source) []string { return paths(ctx, src, cur) })
		case 1:
			path := strings.Trim(pos[0], "/")
			return firstMatch(sources, func(src Source) []string {
				keys, _ := src.Keys(ctx, path)
				return filter(keys, cur)
			})
		}
	case "login":
		if last := prev[len(prev)-1]; last == "-method" || last == "--method" {
			return filter([]string{"oidc", "ldap", "userpass", "token"}, cur)
		}
	case "config":
		if len(prev) == 1 {
			return filter(subcommands[cmd], cur)
		}
	case "completion":
		if len(prev) == 1 {
			return filter(append([]string{"install", "list"}, ShellNames()...), cur)
		}
		if len(prev) == 2 && prev[1] == "install" {
			return filter(ShellNames(), cur)
		}
	}
	return nil
}

func firstMatch(sources []Source, f func(Source) []string) []string {
	for _, src := range sources {
		if src == nil {
			continue
		}
		if out := f(src); len(out) > 0 {
			return out
		}
	}
	return nil
}

// positional drops flags (and the values of flags that take one).
func positional(words []string, withValue map[string]bool) []string {
	var out []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		if strings.HasPrefix(w, "-") {
			if withValue[strings.TrimLeft(w, "-")] {
				i++
			}
			continue
		}
		out = append(out, w)
	}
	return out
}

// paths completes a secret path one segment at a time.
func paths(ctx context.Context, src Source, cur string) []string {
	cur = strings.TrimLeft(cur, "/")
	dir := ""
	if i := strings.LastIndexByte(cur, '/'); i >= 0 {
		dir = cur[:i+1]
	}
	children, _ := src.Children(ctx, dir)
	var out []string
	for _, c := range children {
		if full := dir + c; strings.HasPrefix(full, cur) {
			out = append(out, full)
		}
	}
	sort.Strings(out)
	return dedup(out)
}

func filter(list []string, prefix string) []string {
	var out []string
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			out = append(out, s)
		}
	}
	return out
}

func dedup(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}
