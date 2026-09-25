//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A rate limit quota makes Vault answer 429 to part of the burst of
// requests an index build sends; vaultr waits and retries instead of
// leaving those folders out.
func TestIndexUnderRateLimitQuota(t *testing.T) {
	t.Parallel()
	mount := freshMount(t)
	const n = 40
	for i := 0; i < n; i++ {
		putSecret(t, mount, "dir"+itoa(i%8)+"/s"+itoa(i), "k")
	}
	quota := "sys/quotas/rate-limit/" + strings.TrimSuffix(mount, "/")
	if _, err := root.Write(ctx(t), quota, map[string]any{"path": mount, "rate": 10, "interval": "1s"}); err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "unsupported path") {
			t.Skip("server has no rate limit quotas")
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Delete(context.Background(), quota) })

	c := newCLI(t, rootChild(t, time.Hour))
	c.env["VAULTR_MOUNTS"] = mount
	c.env["VAULTR_WORKERS"] = "32"
	r := c.ok("index")
	if !strings.Contains(r.stderr, "indexed "+itoa(n+1)+" secrets") || strings.Contains(r.stderr, "error") {
		t.Errorf("index under a quota: %q", r.stderr)
	}
}
