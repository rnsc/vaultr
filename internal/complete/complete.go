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

// NamespaceSource is a Source that can also list namespaces (full paths,
// "" for the root).
type NamespaceSource interface {
	Namespaces(ctx context.Context) ([]string, bool)
}

// Commands and their flags, for completing the first word and options.
var (
	Commands = []string{"find", "get", "login", "index", "refresh", "status", "purge", "config", "completion", "version", "help"}

	// Every command but login takes --ns/--namespace, before or after it.
	flags = map[string][]string{
		"":       {"-r", "--refresh", "--ns", "--namespace"}, // before any command
		"find":   {"--json", "--values", "-n", "-r", "--refresh", "--ns", "--namespace"},
		"get":    {"--json", "--ns", "--namespace"},
		"index":  {"--ns", "--namespace"},
		"status": {"--ns", "--namespace"},
		"purge":  {"--ns", "--namespace"},
		"login":  {"-method", "-mount", "-namespace", "-username", "-role", "-callback-port", "-no-save"},
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
	if len(prev) > 0 && isNamespaceFlag(prev[len(prev)-1]) {
		// Also login's -namespace: where to log in.
		return firstMatch(sources, func(src Source) []string { return namespaces(ctx, src, cur) })
	}
	prev = withoutNamespaceFlags(prev)
	if len(prev) == 0 {
		if strings.HasPrefix(cur, "-") {
			return filter(flags[""], cur)
		}
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

func isNamespaceFlag(w string) bool {
	switch strings.TrimLeft(w, "-") {
	case "ns", "namespace":
		return strings.HasPrefix(w, "-")
	}
	return false
}

// withoutNamespaceFlags drops --ns NS pairs (and --ns=NS), which may come
// before the command. login keeps its own -namespace.
func withoutNamespaceFlags(words []string) []string {
	if len(words) > 0 && words[0] == "login" {
		return words
	}
	var out []string
	for i := 0; i < len(words); i++ {
		name, _, hasVal := strings.Cut(words[i], "=")
		if isNamespaceFlag(name) {
			if !hasVal {
				i++
			}
			continue
		}
		out = append(out, words[i])
	}
	return out
}

// namespaces completes a namespace name; the root is "/".
func namespaces(ctx context.Context, src Source, cur string) []string {
	ns, ok := src.(NamespaceSource)
	if !ok {
		return nil
	}
	list, _ := ns.Namespaces(ctx)
	var out []string
	for _, n := range list {
		if n == "" {
			n = "/"
		}
		if strings.HasPrefix(n, strings.TrimPrefix(cur, "/")) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
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
