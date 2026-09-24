package complete

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rnsc/vaultr/internal/index"
)

var entries = []index.Entry{
	{Path: "secret/prod/db", Mount: "secret/", KV: 2, Keys: []string{"note"}},
	{Path: "secret/prod/db/postgres", Mount: "secret/", KV: 2, Keys: []string{"password", "port", "username"}},
	{Path: "secret/prod/payments/stripe", Mount: "secret/", KV: 2, Keys: []string{"api_key"}},
	{Path: "secret/staging/db", Mount: "secret/", KV: 2},
	{Path: "secret/odd/with space/name", Mount: "secret/", KV: 2, Keys: []string{"k"}},
	{Path: "kv/legacy/ldap", Mount: "kv/", KV: 1, Keys: []string{"bind_dn", "bind_password"}},
}

// fakeLive stands in for Vault: it knows one secret the cache doesn't.
type fakeLive struct{ calls int }

func (f *fakeLive) Children(_ context.Context, dir string) ([]string, bool) {
	f.calls++
	switch dir {
	case "":
		return []string{"kv/", "secret/"}, true
	case "secret/":
		return []string{"new/", "odd/", "prod/", "staging/"}, true
	case "secret/new/":
		return []string{"thing"}, true
	}
	return nil, false
}

func (f *fakeLive) Namespaces(context.Context) ([]string, bool) {
	return []string{"", "team-a", "team-a/child", "team-b"}, true
}

func (f *fakeLive) Keys(_ context.Context, path string) ([]string, bool) {
	f.calls++
	if path == "secret/new/thing" {
		return []string{"fresh_key"}, true
	}
	return nil, false
}

func run(t *testing.T, args ...string) []string {
	t.Helper()
	return Candidates(context.Background(), []Source{IndexSource{Entries: entries}, &fakeLive{}}, args)
}

func eq(t *testing.T, got, want []string, what string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %q, want %q", what, got, want)
	}
}

func TestCommandsAndFlags(t *testing.T) {
	eq(t, run(t, ""), Commands, "all commands")
	eq(t, run(t, "f"), []string{"find"}, "f")
	eq(t, run(t, "co"), []string{"config", "completion"}, "co")
	eq(t, run(t, "find", "--v"), []string{"--values"}, "find flags")
	eq(t, run(t, "search", "-"), flags["find"], "alias flags")
	eq(t, run(t, "get", "--"), []string{"--json", "--ns", "--namespace"}, "get flags")
	eq(t, run(t, "-"), []string{"-r", "--refresh", "--ns", "--namespace"}, "flags before a command")
	eq(t, run(t, "login", "-method", ""), []string{"oidc", "ldap", "userpass", "token"}, "login methods")
	eq(t, run(t, "config", ""), []string{"show", "path", "init"}, "config subcommands")
	eq(t, run(t, "completion", "install", "z"), []string{"zsh"}, "completion install shells")
	eq(t, run(t, "completion", "i"), []string{"install"}, "completion subcommand")
	eq(t, run(t, "completion", "f"), []string{"fish"}, "shell names from the registry")
	eq(t, run(t, "find", "stri"), nil, "find queries are free text")
}

func TestPathsFromCache(t *testing.T) {
	eq(t, run(t, "get", ""), []string{"kv/", "secret/"}, "mounts")
	eq(t, run(t, "get", "sec"), []string{"secret/"}, "mount prefix")
	eq(t, run(t, "get", "secret/"), []string{"secret/odd/", "secret/prod/", "secret/staging/"}, "first level")
	eq(t, run(t, "get", "secret/p"), []string{"secret/prod/"}, "folder prefix")
	// A secret sharing its name with a folder offers both.
	eq(t, run(t, "get", "secret/prod/d"), []string{"secret/prod/db", "secret/prod/db/"}, "secret and folder")
	eq(t, run(t, "get", "secret/prod/db/"), []string{"secret/prod/db/postgres"}, "leaf")
	eq(t, run(t, "get", "/secret/prod/pay"), []string{"secret/prod/payments/"}, "leading slash")
	eq(t, run(t, "get", "secret/odd/with "), []string{"secret/odd/with space/"}, "space in name")
	eq(t, run(t, "g", "kv/leg"), []string{"kv/legacy/"}, "alias g")
	eq(t, run(t, "get", "--json", "kv/legacy/l"), []string{"kv/legacy/ldap"}, "after a flag")
}

func TestKeys(t *testing.T) {
	eq(t, run(t, "get", "secret/prod/db/postgres", ""), []string{"password", "port", "username"}, "all keys")
	eq(t, run(t, "get", "secret/prod/db/postgres", "p"), []string{"password", "port"}, "key prefix")
	eq(t, run(t, "get", "/kv/legacy/ldap/", "bind_p"), []string{"bind_password"}, "trimmed path")
	eq(t, run(t, "get", "secret/prod/db/postgres", "password", ""), nil, "nothing after the key")
}

func TestFallsBackToLiveForNewSecrets(t *testing.T) {
	live := &fakeLive{}
	srcs := []Source{IndexSource{Entries: entries}, live}
	ctx := context.Background()
	// The cache has other children of secret/, but none matching "secret/n".
	eq(t, Candidates(ctx, srcs, []string{"get", "secret/n"}), []string{"secret/new/"}, "new folder")
	eq(t, Candidates(ctx, srcs, []string{"get", "secret/new/"}), []string{"secret/new/thing"}, "new secret")
	eq(t, Candidates(ctx, srcs, []string{"get", "secret/new/thing", ""}), []string{"fresh_key"}, "new keys")
	// When the cache answers, Vault is not asked.
	live.calls = 0
	Candidates(ctx, srcs, []string{"get", "secret/prod/"})
	if live.calls != 0 {
		t.Errorf("live source used although the cache matched (%d calls)", live.calls)
	}
}

func TestNamespaces(t *testing.T) {
	all := []string{"/", "team-a", "team-a/child", "team-b"}
	eq(t, run(t, "--ns", ""), all, "all, root as /")
	eq(t, run(t, "get", "--namespace", "team-a"), []string{"team-a", "team-a/child"}, "prefix")
	eq(t, run(t, "find", "-ns", "/team-b"), []string{"team-b"}, "leading slash")
	eq(t, run(t, "login", "-namespace", "t"), all[1:], "login's -namespace")
	// The flag and its value don't count as the command or a path.
	eq(t, run(t, "--ns", "team-a", "ge"), []string{"get"}, "command after --ns")
	eq(t, run(t, "--ns", "team-a", "get", "sec"), []string{"secret/"}, "path after --ns")
	eq(t, run(t, "get", "--ns=team-a", "secret/prod/db/postgres", "p"), []string{"password", "port"}, "keys after --ns=")
	// Without a source that knows namespaces: nothing.
	eq(t, Candidates(context.Background(), []Source{IndexSource{Entries: entries}}, []string{"--ns", ""}), nil, "no namespace source")
}

func TestNoSources(t *testing.T) {
	// No token or no server: commands still complete, paths don't.
	eq(t, Candidates(context.Background(), nil, []string{"ge"}), []string{"get"}, "commands")
	eq(t, Candidates(context.Background(), nil, []string{"get", "sec"}), nil, "paths")
	eq(t, Candidates(context.Background(), []Source{IndexSource{}}, []string{"get", ""}), nil, "empty cache")
}

func TestUnescape(t *testing.T) {
	for in, want := range map[string]string{
		`secret/odd/with\ space/`: "secret/odd/with space/",
		`'secret/odd/with space/`: "secret/odd/with space/",
		`"secret/a b"`:            "secret/a b",
		`secret/hash\#tag`:        "secret/hash#tag",
		`plain`:                   "plain",
		`trailing\`:               `trailing\`,
	} {
		if got := Unescape(in); got != want {
			t.Errorf("Unescape(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every registered shell must pass these; a new shell gets them for free.
func TestEveryRegisteredShell(t *testing.T) {
	seen := map[string]bool{}
	for _, sh := range Shells {
		t.Run(sh.Name, func(t *testing.T) {
			if seen[sh.Name] {
				t.Fatal("registered twice")
			}
			seen[sh.Name] = true
			script, err := Script(sh.Name)
			if err != nil || !strings.Contains(script, "vaultr __complete") {
				t.Fatalf("script missing or does not call vaultr __complete: %v", err)
			}
			if (sh.RCFile == "") == (sh.CompletionFile == "") {
				t.Fatal("set exactly one of RCFile or CompletionFile")
			}
			if sh.RCFile != "" && !strings.Contains(sh.LoadLine, "%s") {
				t.Fatalf("LoadLine %q must contain %%s for the shell name", sh.LoadLine)
			}

			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", "")
			if sh.RCDirEnv != "" {
				t.Setenv(sh.RCDirEnv, "")
			}
			var first []byte
			for i := 0; i < 2; i++ {
				if _, err := Install(sh.Name); err != nil {
					t.Fatalf("install #%d: %v", i+1, err)
				}
				var target string
				if sh.CompletionFile != "" {
					target = filepath.Join(home, ".config", sh.CompletionFile)
				} else {
					target = filepath.Join(home, sh.RCFile)
				}
				b, err := os.ReadFile(target)
				if err != nil {
					t.Fatalf("install #%d wrote nothing to %s", i+1, target)
				}
				if i == 0 {
					first = b
				} else if string(b) != string(first) {
					t.Errorf("installing twice changed %s", target)
				}
			}

			t.Setenv("SHELL", "/usr/bin/"+sh.Name)
			if got, err := DetectShell(); err != nil || got != sh.Name {
				t.Errorf("DetectShell: %q %v", got, err)
			}
		})
	}
}

func TestUnknownShell(t *testing.T) {
	if _, err := Script("tcsh"); err == nil {
		t.Error("tcsh accepted")
	}
	if _, err := Install("tcsh"); err == nil {
		t.Error("tcsh install accepted")
	}
	for _, in := range []string{"", "/bin/tcsh"} {
		t.Setenv("SHELL", in)
		if _, err := DetectShell(); err == nil {
			t.Errorf("SHELL=%q accepted", in)
		}
	}
}

func TestInstallRCDetails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")
	_ = os.WriteFile(filepath.Join(home, ".zshrc"), []byte("export FOO=1"), 0o644) // no trailing newline
	if _, err := Install("zsh"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".zshrc"))
	if !strings.HasPrefix(string(b), "export FOO=1\n") || strings.Count(string(b), rcMarker) != 1 ||
		!strings.Contains(string(b), `eval "$(vaultr completion zsh)"`) {
		t.Errorf(".zshrc:\n%s", b)
	}
	zdot := t.TempDir()
	t.Setenv("ZDOTDIR", zdot)
	if _, err := Install("zsh"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(zdot, ".zshrc")); err != nil {
		t.Error("ZDOTDIR not honoured")
	}
}
