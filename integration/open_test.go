//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIOpen(t *testing.T) {
	t.Parallel()
	c := newCLI(t, rootChild(t, time.Hour))
	want := addr + "/ui/vault/secrets/" + strings.TrimSuffix(fx.KV2, "/") + "/show/odd/with%20space/secret%20name"
	if r := c.ok("open", "--print", fx.KV2+"odd/with space/secret name"); r.stdout != want+"\n" {
		t.Errorf("--print: %q, want %q", r.stdout, want)
	}

	// Without --print it hands the address to the browser ($BROWSER here).
	dir := t.TempDir()
	out := filepath.Join(dir, "url")
	browser := filepath.Join(dir, "browser")
	if err := os.WriteFile(browser, []byte("#!/bin/sh\nprintf '%s' \"$1\" > "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c.env["BROWSER"] = browser
	c.ok("open", fx.KV1+"legacy/ldap")
	deadline := time.Now().Add(5 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		if got, _ = os.ReadFile(out); len(got) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if string(got) != addr+"/ui/vault/secrets/"+strings.TrimSuffix(fx.KV1, "/")+"/show/legacy/ldap" {
		t.Errorf("browser got %q", got)
	}
	if r := c.run("open", fx.KV2+"nope/missing", "extra"); r.code == 0 {
		t.Error("two paths accepted")
	}
}
