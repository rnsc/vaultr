package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// releaseRepo is a scratch git repository to run scripts/next-version.sh
// against.
type releaseRepo struct {
	t      *testing.T
	dir    string
	script string
}

func newReleaseRepo(t *testing.T) *releaseRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	script, err := filepath.Abs("scripts/next-version.sh")
	if err != nil {
		t.Fatal(err)
	}
	r := &releaseRepo{t: t, dir: t.TempDir(), script: script}
	r.git("init", "-q")
	return r
}

func (r *releaseRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}, args...)...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// commit writes the files (content = their name plus the message) and
// commits them with msg.
func (r *releaseRepo) commit(msg string, files ...string) {
	r.t.Helper()
	for _, f := range files {
		p := filepath.Join(r.dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f+msg), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
}

// next runs the script; it returns the tag printed and the reason logged.
func (r *releaseRepo) next(args ...string) (string, string) {
	r.t.Helper()
	cmd := exec.Command("bash", append([]string{r.script}, args...)...)
	cmd.Dir = r.dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		r.t.Fatalf("next-version %v: %v\n%s", args, err, stderr.String())
	}
	return strings.TrimSpace(string(out)), stderr.String()
}

func TestNextVersion(t *testing.T) {
	r := newReleaseRepo(t)
	r.commit("docs: first", "README.md")
	if got, _ := r.next(); got != "v0.1.0" {
		t.Fatalf("first release: %q", got)
	}
	r.git("tag", "v0.1.0")
	if got, why := r.next(); got != "" || !strings.Contains(why, "already released") {
		t.Errorf("tagged HEAD: %q (%s)", got, why)
	}

	steps := []struct {
		msg   string
		files []string
		args  []string
		want  string
		why   string // part of the reason when nothing is released
	}{
		// Nothing shipped since v0.1.0: no release, unless asked for.
		{"docs: readme", []string{"README.md", "AGENTS.md"}, nil, "", "nothing shipped changed since v0.1.0"},
		{"ci: runners", []string{".github/workflows/ci.yml", "scripts/install.sh"}, nil, "", "nothing shipped"},
		{"test: more", []string{"main_test.go", "integration/x_test.go", "internal/testvault/f.go", "tools/seed/main.go"}, nil, "", "nothing shipped"},
		{"docs: shells", []string{"internal/complete/shells/README.md"}, nil, "", "nothing shipped"},
		{"feat: docs only [release]", []string{"README.md"}, nil, "v0.2.0", ""},
		{"docs: again", []string{"README.md"}, []string{"patch"}, "v0.1.1", ""}, // an explicit bump always releases
		// Anything shipped releases, with the bump from the message.
		{"fix: code", []string{"internal/vault/client.go"}, nil, "v0.1.1", ""},
		{"feat: thing", []string{"main.go"}, nil, "v0.2.0", ""},
		{"deps", []string{"go.sum"}, nil, "v0.1.1", ""},
		{"brew", []string{".goreleaser.yaml"}, nil, "v0.1.1", ""},
		{"zsh", []string{"internal/complete/shells/zsh.zsh"}, nil, "v0.1.1", ""},
		{"feat!: breaking", []string{"main.go"}, nil, "v1.0.0", ""},
		{"fix: code [skip release]", []string{"main.go"}, nil, "", "skip the release"},
	}
	for _, s := range steps {
		// Each step starts again from v0.1.0 plus only its own change.
		r.git("reset", "-q", "--hard", "v0.1.0")
		r.commit(s.msg, s.files...)
		got, why := r.next(s.args...)
		if got != s.want || (s.why != "" && !strings.Contains(why, s.why)) {
			t.Errorf("%q %v: got %q (%s), want %q", s.msg, s.files, got, strings.TrimSpace(why), s.want)
		}
	}
}
