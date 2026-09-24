//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// completeCLI runs `vaultr __complete` and returns its candidates.
func completeCLI(t *testing.T, c *cli, words ...string) []string {
	t.Helper()
	r := c.run(append([]string{"__complete"}, words...)...)
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("__complete %q: exit %d, stderr %q", words, r.code, r.stderr)
	}
	return lines(r.stdout)
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestCompleteFromCache(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	c.ok("index")
	cases := []struct {
		words []string
		want  string
	}{
		{[]string{"get", ""}, fx.KV2},
		{[]string{"get", fx.KV2 + "pr"}, fx.KV2 + "prod/"},
		{[]string{"get", fx.KV2 + "prod/"}, fx.KV2 + "prod/payments/"},
		{[]string{"get", fx.KV2 + "prod/d"}, fx.KV2 + "prod/db"}, // secret and folder of the same name
		{[]string{"get", fx.KV2 + "prod/d"}, fx.KV2 + "prod/db/"},
		{[]string{"get", fx.KV1 + "legacy/"}, fx.KV1 + "legacy/ldap"},
		{[]string{"get", fx.KV1 + "legacy/ldap", "bind_p"}, "bind_password"},
		{[]string{"get", fx.KV2 + `odd/with\ space/`}, fx.KV2 + "odd/with space/secret name"},
		{[]string{"get", fx.KV2 + "odd/hash#"}, fx.KV2 + "odd/hash#tag?q"},
		{[]string{"ge"}, "get"},
		{[]string{"find", "--val"}, "--values"},
	}
	for _, tc := range cases {
		if got := completeCLI(t, c, tc.words...); !has(got, tc.want) {
			t.Errorf("%q: got %q, want %q among them", tc.words, got, tc.want)
		}
	}
}

func TestCompleteWithoutCacheAndNewSecrets(t *testing.T) {
	t.Parallel()
	mount := freshMount(t)
	c := newCLI(t, rootChild(t, time.Hour))
	c.env["VAULTR_MOUNTS"] = mount

	// No cache at all: listed live, and no index gets built as a side effect.
	if got := completeCLI(t, c, "get", mount); !has(got, mount+"seed") {
		t.Errorf("no cache: %q", got)
	}
	if st := c.ok("status").stdout; !strings.Contains(st, "cache:    none") {
		t.Errorf("completion built an index:\n%s", st)
	}

	// With a cache that predates the secret: the live fallback finds it.
	c.ok("index")
	putSecret(t, mount, "added/later", "late_key")
	if got := completeCLI(t, c, "get", mount+"ad"); !has(got, mount+"added/") {
		t.Errorf("new folder: %q", got)
	}
	if got := completeCLI(t, c, "get", mount+"added/later", ""); !has(got, "late_key") {
		t.Errorf("new keys: %q", got)
	}
}

func TestCompleteIsQuietWithoutAccess(t *testing.T) {
	t.Parallel()
	for name, tok := range map[string]string{"no token": "", "bad token": "hvs.invalid"} {
		c := newCLI(t, tok)
		if tok == "" {
			delete(c.env, "VAULT_TOKEN")
		}
		if got := completeCLI(t, c, "get", fx.KV2); len(got) != 0 {
			t.Errorf("%s: paths %q", name, got)
		}
		if got := completeCLI(t, c, "sta"); !has(got, "status") {
			t.Errorf("%s: commands %q", name, got)
		}
	}
	c := newCLI(t, rootChild(t, time.Hour))
	c.env["VAULT_ADDR"] = "http://127.0.0.1:1"
	if got := completeCLI(t, c, "get", "sec"); len(got) != 0 {
		t.Errorf("unreachable server: %q", got)
	}
}

// shellEnv returns the environment for running a shell with vaultr on PATH.
func shellEnv(c *cli) []string {
	env := []string{"PATH=" + filepath.Dir(binPath) + string(os.PathListSeparator) + os.Getenv("PATH")}
	for k, v := range c.env {
		env = append(env, k+"="+v)
	}
	return env
}

func vaultrOnPath(t *testing.T) {
	t.Helper()
	if filepath.Base(binPath) != "vaultr" {
		t.Skip("binary is not named vaultr")
	}
}

func TestBashCompletion(t *testing.T) {
	t.Parallel()
	vaultrOnPath(t)
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	c := newCLI(t, rootChild(t, time.Hour))
	c.ok("index")
	script := `source <(vaultr completion bash)
COMP_WORDS=(vaultr get "$1"); COMP_CWORD=2
_vaultr
printf '%s\n' "${COMPREPLY[@]}"`
	for word, want := range map[string]string{
		fx.KV2 + "pr":             fx.KV2 + "prod/",
		fx.KV2 + `odd/with\ sp`:   fx.KV2 + `odd/with\ space/`, // escaped for the command line
		fx.KV2 + "prod/payments/": fx.KV2 + "prod/payments/stripe",
	} {
		cmd := exec.Command("bash", "-c", script, "bash", word)
		cmd.Env = shellEnv(c)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bash: %v\n%s", err, out)
		}
		if got := lines(string(out)); !has(got, want) {
			t.Errorf("bash %q: got %q, want %q", word, got, want)
		}
	}
}

func TestFishCompletion(t *testing.T) {
	t.Parallel()
	vaultrOnPath(t)
	if _, err := exec.LookPath("fish"); err != nil {
		t.Skip("fish not installed")
	}
	c := newCLI(t, rootChild(t, time.Hour))
	c.ok("index")
	cmd := exec.Command("fish", "--no-config", "-c",
		`vaultr completion fish | source; complete -C "vaultr get $argv[1]"`, fx.KV2+"pro")
	cmd.Env = shellEnv(c)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fish: %v\n%s", err, out)
	}
	if got := lines(string(out)); !has(got, fx.KV2+"prod/") {
		t.Errorf("fish: got %q", got)
	}
}

func TestZshCompletionScriptParses(t *testing.T) {
	t.Parallel()
	vaultrOnPath(t)
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh not installed")
	}
	c := newCLI(t, "")
	// Loads compinit and registers the completion without errors.
	cmd := exec.Command("zsh", "-f", "-c", `source <(vaultr completion zsh) && (( $+_comps[vaultr] )) && echo registered`)
	cmd.Env = append(shellEnv(c), "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "registered" {
		t.Errorf("zsh: %v\n%s", err, out)
	}
}

func TestCompletionInstallCommand(t *testing.T) {
	t.Parallel()
	c := newCLI(t, "")
	c.env["SHELL"] = "/bin/zsh"
	c.env["ZDOTDIR"] = ""
	if r := c.ok("completion", "install"); !strings.Contains(r.stderr, ".zshrc") {
		t.Errorf("install: %+v", r)
	}
	b, err := os.ReadFile(filepath.Join(c.env["HOME"], ".zshrc"))
	if err != nil || !strings.Contains(string(b), "source <(vaultr completion zsh)") {
		t.Errorf(".zshrc: %v %q", err, b)
	}
	if r := c.ok("completion", "install"); !strings.Contains(r.stderr, "already") {
		t.Errorf("second install: %+v", r)
	}
	if r := c.ok("completion", "bash"); !strings.Contains(r.stdout, "complete -F _vaultr vaultr") {
		t.Errorf("bash script: %q", r.stdout)
	}
	if r := c.run("completion", "tcsh"); r.code == 0 {
		t.Error("tcsh accepted")
	}
	c.env["SHELL"] = ""
	if r := c.run("completion", "install"); r.code == 0 || !strings.Contains(r.stderr, "SHELL") {
		t.Errorf("no SHELL: %+v", r)
	}
}
