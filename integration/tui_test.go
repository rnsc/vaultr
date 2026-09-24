//go:build integration

package integration

// Drives the real binary's TUI through a pseudo-terminal.

import (
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*\x07|\x1b[()][A-Z0-9]`)

type term struct {
	t    *testing.T
	f    *os.File
	cmd  *exec.Cmd
	mu   sync.Mutex
	buf  strings.Builder
	done chan struct{}
}

func startTUI(t *testing.T, c *cli, args ...string) *term {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TERM=xterm-256color"}
	for k, v := range c.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 110})
	if err != nil {
		t.Fatal(err)
	}
	tm := &term{t: t, f: f, cmd: cmd, done: make(chan struct{})}
	go func() {
		defer close(tm.done)
		b := make([]byte, 4096)
		for {
			n, err := f.Read(b)
			tm.mu.Lock()
			tm.buf.Write(b[:n])
			tm.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = f.Close()
	})
	return tm
}

// text is everything rendered so far, without escape sequences.
func (tm *term) text() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return ansiRE.ReplaceAllString(tm.buf.String(), "")
}

// since returns rendered text after mark.
func (tm *term) mark() int { tm.mu.Lock(); defer tm.mu.Unlock(); return tm.buf.Len() }

func (tm *term) waitFor(from int, want string) {
	tm.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		tm.mu.Lock()
		out := ansiRE.ReplaceAllString(tm.buf.String()[from:], "")
		tm.mu.Unlock()
		if strings.Contains(out, want) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	tm.t.Fatalf("timed out waiting for %q; screen output:\n%s", want, tm.text())
}

func (tm *term) send(s string) {
	tm.t.Helper()
	if _, err := io.WriteString(tm.f, s); err != nil {
		tm.t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond) // let the UI process each key batch
}

func (tm *term) waitExit() {
	tm.t.Helper()
	errc := make(chan error, 1)
	go func() { errc <- tm.cmd.Wait() }()
	select {
	case err := <-errc:
		if err != nil {
			tm.t.Errorf("TUI exited with %v; output:\n%s", err, tm.text())
		}
	case <-time.After(10 * time.Second):
		tm.t.Fatal("TUI did not exit")
	}
}

const (
	keyEnter = "\r"
	keyEsc   = "\x1b"
	keyCtrlR = "\x12"
	keyCtrlC = "\x03"
)

func TestTUISearchRevealQuit(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	tm := startTUI(t, c)

	// No cache yet: the TUI builds the index itself.
	tm.waitFor(0, "indexed "+itoa(len(fx.Secrets))+" secrets")

	m := tm.mark()
	tm.send("stripe webhook")
	tm.waitFor(m, "1/")
	tm.waitFor(m, "webhook_secret")

	m = tm.mark()
	tm.send(keyEnter)
	tm.waitFor(m, "••••••••")
	if strings.Contains(tm.text(), "whsec_fixture") {
		t.Fatal("value visible before reveal")
	}
	m = tm.mark()
	tm.send("r")
	tm.waitFor(m, "whsec_fixture")

	tm.send(keyEsc)
	m = tm.mark()
	tm.send(keyCtrlR)
	tm.waitFor(m, "indexed ")
	tm.send(keyCtrlC) // clears "stripe webhook"
	tm.send(keyCtrlC) // quits
	tm.waitExit()

	// The TUI left a valid cache behind for the CLI.
	if r := c.ok("status"); !strings.Contains(r.stdout, "bound to token cubbyhole") {
		t.Errorf("status after TUI:\n%s", r.stdout)
	}
}

func TestTUICrossNamespace(t *testing.T) {
	t.Parallel()
	needNamespaces(t)
	c := nsCLI(t, rootLoginToken(t, time.Hour))
	writeConfig(t, c, `namespace = "`+nsx.Parent+`"`+"\n")
	tm := startTUI(t, c, "ns_only")

	tm.waitFor(0, "ns "+nsx.Parent)
	tm.waitFor(0, "secret/team/app/db")
	m := tm.mark()
	tm.send(keyEnter)
	tm.waitFor(m, "ns_only_key")
	m = tm.mark()
	tm.send("r")
	tm.waitFor(m, "ns-parent-pass")
	tm.send(keyEsc)   // back to the list
	tm.send(keyCtrlC) // clears the search
	tm.send(keyCtrlC) // quits
	tm.waitExit()

	st := c.ok("status").stdout
	if !strings.Contains(st, "token ns: (root)") || !strings.Contains(st, "bound to token cubbyhole") {
		t.Errorf("status after cross-namespace TUI session:\n%s", st)
	}
}

func TestTUIInvalidTokenOpensLogin(t *testing.T) {
	t.Parallel()
	c := newCLI(t, "hvs.invalid")
	tm := startTUI(t, c)
	tm.waitFor(0, "Log in to Vault")
	tm.waitFor(0, "expired or revoked")
	tm.send(keyCtrlC)
	tm.waitExit()
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestTUIRefreshFlagAndNoMatch(t *testing.T) {
	t.Parallel()
	mount := freshMount(t)
	c := newCLI(t, rootChild(t, time.Hour))
	c.env["VAULTR_MOUNTS"] = mount
	c.ok("index")
	putSecret(t, mount, "fresh/one", "first_new_key")

	// Cached index: no match, the hint offers a refresh, enter does it.
	tm := startTUI(t, c, "first_new_key")
	tm.waitFor(0, "Press enter or ^r to refresh")
	m := tm.mark()
	tm.send(keyEnter)
	tm.waitFor(m, "indexed ")
	tm.waitFor(m, "fresh/one")
	tm.send(keyCtrlC)
	tm.send(keyCtrlC)
	tm.waitExit()

	// vaultr -r rebuilds at startup even with a valid cache.
	putSecret(t, mount, "fresh/two", "second_new_key")
	tm = startTUI(t, c, "-r", "second_new_key")
	tm.waitFor(0, "indexed ")
	tm.waitFor(0, "fresh/two")
	tm.send(keyCtrlC)
	tm.send(keyCtrlC)
	tm.waitExit()
}
