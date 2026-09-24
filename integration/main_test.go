//go:build integration

// Package integration runs vaultr against a real Vault server.
//
// It needs VAULT_ADDR and a root VAULT_TOKEN (a dev server is fine):
//
//	vault server -dev -dev-root-token-id=root &
//	VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=root go test -tags integration ./integration
//
// Each run provisions its own uniquely prefixed mounts and policies and
// removes them afterwards, so runs can share a server.
package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/rnsc/vaultr/internal/testvault"
	"github.com/rnsc/vaultr/internal/vault"
)

var (
	nsx     *testvault.Namespaces // nil when the server has no namespaces
	addr    string
	root    *vault.Client
	fx      *testvault.Fixture
	binPath string
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	addr = os.Getenv("VAULT_ADDR")
	tok := os.Getenv("VAULT_TOKEN")
	if addr == "" || tok == "" {
		fmt.Fprintln(os.Stderr, "integration tests need VAULT_ADDR and a root VAULT_TOKEN")
		return 1
	}
	var err error
	root, err = vault.New(vault.Config{Addr: addr, Token: tok})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := waitReady(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "vault not ready:", err)
		return 1
	}

	b := make([]byte, 4)
	_, _ = rand.Read(b)
	fx, err = testvault.Provision(ctx, &testvault.Admin{Addr: addr, Token: tok}, "it"+hex.EncodeToString(b))
	if fx != nil {
		defer fx.Teardown(context.Background())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "provisioning:", err)
		return 1
	}

	nsx, err = testvault.ProvisionNamespaces(ctx, &testvault.Admin{Addr: addr, Token: tok}, fx.Prefix)
	switch {
	case errors.Is(err, testvault.ErrNoNamespaces):
		fmt.Fprintln(os.Stderr, "server has no namespace support: namespace tests will be skipped")
	case err != nil:
		fmt.Fprintln(os.Stderr, "provisioning namespaces:", err)
		return 1
	default:
		defer nsx.Teardown(context.Background())
	}

	dir, err := os.MkdirTemp("", "vaultr-it-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	binPath = filepath.Join(dir, "vaultr")
	build := exec.Command("go", "build", "-race", "-o", binPath, "github.com/rnsc/vaultr")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building vaultr:", err)
		return 1
	}
	return m.Run()
}

func waitReady(ctx context.Context) error {
	for {
		_, err := root.LookupSelf(ctx)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// token creates a token for the test and revokes it at cleanup.
func token(t *testing.T, o testvault.TokenOptions) string {
	t.Helper()
	tok, err := fx.Token(context.Background(), o)
	if err != nil {
		t.Fatalf("creating token: %v", err)
	}
	t.Cleanup(func() { _ = fx.Revoke(context.Background(), tok) })
	return tok
}

// rootChild is a revocable, optionally short-lived token with full access
// and its own cubbyhole.
func rootChild(t *testing.T, ttl time.Duration) string {
	t.Helper()
	return token(t, testvault.TokenOptions{Policies: []string{"root"}, TTL: ttl})
}

func client(t *testing.T, tok string) *vault.Client {
	t.Helper()
	c, err := vault.New(vault.Config{Addr: addr, Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return c
}

// cli runs the vaultr binary.
type cli struct {
	t   *testing.T
	env map[string]string
}

func newCLI(t *testing.T, tok string) *cli {
	t.Helper()
	return &cli{t: t, env: map[string]string{
		"VAULT_ADDR":       addr,
		"VAULT_TOKEN":      tok,
		"VAULTR_CACHE_DIR": t.TempDir(),
		"HOME":             t.TempDir(), // no stray ~/.vault-token
		"VAULTR_MOUNTS":    fx.KV2 + "," + fx.KV1 + "," + fx.Empty,
	}}
}

type result struct {
	stdout, stderr string
	code           int
}

func (c *cli) run(args ...string) result {
	c.t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for k, v := range c.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		c.t.Fatalf("running vaultr: %v", err)
	}
	return result{out.String(), errb.String(), code}
}

func (c *cli) ok(args ...string) result {
	c.t.Helper()
	r := c.run(args...)
	if r.code != 0 {
		c.t.Fatalf("vaultr %v: exit %d\nstdout: %s\nstderr: %s", args, r.code, r.stdout, r.stderr)
	}
	return r
}
