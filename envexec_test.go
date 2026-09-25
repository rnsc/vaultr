package main

import (
	"os/exec"
	"testing"
)

func TestEnvName(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"", "password"}:       "PASSWORD",
		{"APP_", "api_key"}:    "APP_API_KEY",
		{"", "key with space"}: "KEY_WITH_SPACE",
		{"", "we#ird"}:         "WE_IRD",
		{"", "clé"}:            "CL_",
		{"", "2fa"}:            "_2FA",
		{"app-", "db.host"}:    "APP_DB_HOST",
	} {
		if got := envName(in[0], in[1]); got != want {
			t.Errorf("envName(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// The quoted values must come back unchanged through the real shells.
func TestQuotingRoundTrips(t *testing.T) {
	values := []string{"plain", "it's", `back\slash`, "multi\nline", "$HOME `id` \"q\"", "", "trailing\\"}
	for _, sh := range []struct {
		name  string
		quote func(string) string
		args  func(string) []string
	}{
		{"sh", shQuote, func(q string) []string { return []string{"-c", "printf %s " + q} }},
		{"fish", fishQuote, func(q string) []string { return []string{"--no-config", "-c", "printf %s " + q} }},
	} {
		if _, err := exec.LookPath(sh.name); err != nil {
			t.Logf("%s not installed", sh.name)
			continue
		}
		for _, v := range values {
			out, err := exec.Command(sh.name, sh.args(sh.quote(v))...).Output()
			if err != nil || string(out) != v {
				t.Errorf("%s: %q came back as %q (%v)", sh.name, v, out, err)
			}
		}
	}
}
