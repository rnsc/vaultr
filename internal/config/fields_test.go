package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFieldsCoverFileStruct(t *testing.T) {
	vals := File{}.Values()
	if len(vals) != len(Fields) {
		t.Errorf("Values has %d keys, Fields has %d", len(vals), len(Fields))
	}
	for _, f := range Fields {
		if _, ok := vals[f.Key]; !ok {
			t.Errorf("field %s missing from Values", f.Key)
		}
		if f.Help == "" || f.Example == "" {
			t.Errorf("field %s lacks help or example", f.Key)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	in := map[string]string{
		"address": "https://v.example.com", "namespace": "/team-a/", "token_namespace": "/",
		"ca_cert": "/ca.pem", "mounts": " secret/ , kv-team,, ", "workers": "8", "max_age": "30m",
		"paths_only": "true", "clip_clear": "0", "cache_dir": "/tmp/c",
		"auth.method": "LDAP", "auth.mount": "/corp-ldap/", "auth.username": "jdoe",
		"auth.namespace": "/", "auth.callback_port": "8300", "auth.save_token": "false",
	}
	f, err := FromValues(in)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "sub", "config.toml")
	if err := WriteFile(p, f); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	back, found, err := ReadFile(p)
	if err != nil || !found {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Values(), f.Values()) {
		t.Errorf("round trip changed values:\n got %v\nwant %v", back.Values(), f.Values())
	}
	want := map[string]string{
		"namespace": "team-a", "token_namespace": "/", "mounts": "secret, kv-team",
		"auth.method": "ldap", "auth.mount": "corp-ldap", "auth.namespace": "/", "auth.save_token": "false",
	}
	for k, v := range want {
		if got := back.Values()[k]; got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	// And it resolves as expected.
	isolate(t, nil, "")
	t.Setenv("VAULTR_CONFIG", p)
	s := load(t)
	if s.Vault.Namespace != "team-a" || s.Vault.TokenNamespace == nil || *s.Vault.TokenNamespace != "" {
		t.Errorf("vault settings %+v", s.Vault)
	}
	if s.Auth != (AuthSettings{Method: "ldap", Mount: "corp-ldap", Username: "jdoe", Namespace: "", CallbackPort: 8300, SaveToken: false}) {
		t.Errorf("auth settings %+v", s.Auth)
	}
}

func TestRenderDocumentsEverything(t *testing.T) {
	out := string(Template())
	for _, f := range Fields {
		if !strings.Contains(out, "# "+f.Name()+" = "+f.Example) {
			t.Errorf("template lacks example for %s", f.Key)
		}
		if f.Env != "" && !strings.Contains(out, "Overridden by "+f.Env) {
			t.Errorf("template lacks env note for %s", f.Key)
		}
	}
	if !strings.Contains(out, "[auth]") {
		t.Error("no [auth] table")
	}
	var f File
	isolate(t, nil, out)
	if s := load(t); s.Auth.SaveToken != true || s.Auth.Method != "" {
		t.Errorf("template changes auth defaults: %+v (%v)", s.Auth, f)
	}
}

func TestFromValuesErrors(t *testing.T) {
	cases := map[string]string{
		"workers":            "many",
		"max_age":            "soon",
		"clip_clear":         "later",
		"paths_only":         "maybe",
		"auth.method":        "kerberos",
		"auth.callback_port": "99999",
	}
	for k, v := range cases {
		vals := File{}.Values()
		vals[k] = v
		if _, err := FromValues(vals); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%q: error %v", k, v, err)
		}
	}
	vals := File{}.Values()
	vals["workers"] = "-3"
	if _, err := FromValues(vals); err == nil {
		t.Error("negative workers accepted")
	}
}

func TestAuthDefaults(t *testing.T) {
	isolate(t, nil, `token_namespace = "team-a"`)
	if s := load(t); s.Auth.Namespace != "team-a" || !s.Auth.SaveToken {
		t.Errorf("auth namespace should default to token_namespace: %+v", s.Auth)
	}
	isolate(t, nil, "token_namespace = \"team-a\"\n[auth]\nnamespace = \"/\"\n")
	if s := load(t); s.Auth.Namespace != "" {
		t.Errorf("explicit auth.namespace should win: %+v", s.Auth)
	}
	isolate(t, nil, "[auth]\nmethod = \"kerberos\"\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "auth.method") {
		t.Errorf("bad method: %v", err)
	}
	isolate(t, nil, "[auth]\npassword = \"x\"\n")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "unknown setting") {
		t.Errorf("password key: %v", err)
	}
}
