package main

import (
	"archive/zip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJWT(t *testing.T) {
	c := &client{issuer: "user:1:2", secret: "s3cret"}
	now := time.Unix(1700000000, 0)
	tok := c.jwt(now)
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", tok)
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != parts[2] {
		t.Error("bad signature")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iss      string
		Jti      string
		Iat, Exp int64
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "user:1:2" || claims.Iat != now.Unix() || claims.Exp != now.Unix()+60 || len(claims.Jti) != 32 {
		t.Errorf("claims %+v", claims)
	}
	if c.jwt(now) == tok {
		t.Error("two tokens share a jti")
	}
}

func TestSignedFile(t *testing.T) {
	signed := []byte("PK signed add-on")
	sum := sha256.Sum256(signed)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "JWT ") {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		switch r.URL.EscapedPath() {
		case "/api/v5/addons/addon/vaultr@rnsc.be/versions/v1.4.2/":
			fmt.Fprintf(w, `{"version":"1.4.2","file":{"url":%q,"status":"public","hash":"sha256:%s"}}`,
				srv.URL+"/file.xpi", hex.EncodeToString(sum[:]))
		case "/api/v5/addons/addon/vaultr@rnsc.be/versions/v1.4.3/":
			fmt.Fprintf(w, `{"version":"1.4.3","file":{"url":%q,"status":"unreviewed"}}`, srv.URL+"/file.xpi")
		case "/api/v5/addons/addon/vaultr@rnsc.be/versions/v1.4.4/":
			fmt.Fprintf(w, `{"version":"1.4.4","file":{"url":%q,"status":"public","hash":"sha256:00"}}`, srv.URL+"/file.xpi")
		case "/file.xpi":
			_, _ = w.Write(signed)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &client{api: srv.URL + "/api/v5/", issuer: "i", secret: "s", http: srv.Client()}
	ctx := context.Background()
	b, err := c.signedFile(ctx, "1.4.2")
	if err != nil || string(b) != string(signed) {
		t.Fatalf("got %q, %v", b, err)
	}
	if _, err := c.signedFile(ctx, "1.4.3"); err == nil || !strings.Contains(err.Error(), "not signed yet") {
		t.Errorf("unreviewed: %v", err)
	}
	if _, err := c.signedFile(ctx, "1.4.4"); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Errorf("wrong hash: %v", err)
	}
	if _, err := c.signedFile(ctx, "9.9.9"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("unknown version: %v", err)
	}
}

func writeZip(t *testing.T, p string, files map[string]string) {
	t.Helper()
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	_ = f.Close()
}

func TestVerify(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "icons"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{}"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "icons", "16.png"), []byte("png"), 0o644)
	xpi := filepath.Join(t.TempDir(), "a.xpi")

	writeZip(t, xpi, map[string]string{"manifest.json": "{}", "icons/16.png": "png", "META-INF/mozilla.rsa": "sig"})
	if err := verify(xpi, dir); err != nil {
		t.Errorf("matching add-on: %v", err)
	}
	writeZip(t, xpi, map[string]string{"manifest.json": "{\"x\":1}", "icons/16.png": "png"})
	if err := verify(xpi, dir); err == nil || !strings.Contains(err.Error(), "manifest.json: differs") {
		t.Errorf("changed file: %v", err)
	}
	writeZip(t, xpi, map[string]string{"manifest.json": "{}"})
	if err := verify(xpi, dir); err == nil || !strings.Contains(err.Error(), "icons/16.png: missing") {
		t.Errorf("missing file: %v", err)
	}
	writeZip(t, xpi, map[string]string{"manifest.json": "{}", "icons/16.png": "png", "evil.js": "x"})
	if err := verify(xpi, dir); err == nil || !strings.Contains(err.Error(), "evil.js: not in this release") {
		t.Errorf("extra file: %v", err)
	}
}

func TestVerifyExplainsJSONChanges(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"version":"1.0.0","a":{"b":1,"c":2}}`), 0o644)
	xpi := filepath.Join(t.TempDir(), "a.xpi")
	writeZip(t, xpi, map[string]string{"manifest.json": `{"version":"1.0.0","a":{"b":1,"d":3},"x":true}`})
	err := verify(xpi, dir)
	for _, want := range []string{"manifest.json: differs:", "a.c: removed (was 2)", "a.d: added: 3", "x: added: true"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in: %v", want, err)
		}
	}
	writeZip(t, xpi, map[string]string{"manifest.json": "{\n  \"a\": {\"c\": 2, \"b\": 1},\n  \"version\": \"1.0.0\"\n}"})
	if err := verify(xpi, dir); err != nil {
		t.Errorf("reformatted, same JSON: %v", err)
	}
	// What AMO did to 0.12.0: the same manifest without its final newline.
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte("{\n  \"version\": \"0.12.0\"\n}\n"), 0o644)
	writeZip(t, xpi, map[string]string{"manifest.json": "{\n  \"version\": \"0.12.0\"\n}"})
	if err := verify(xpi, dir); err != nil {
		t.Errorf("final newline dropped: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"version":"1.0.0","a":{"b":1,"c":2}}`), 0o644)
	writeZip(t, xpi, map[string]string{"manifest.json": `{"version":"2.0.0","a":{"b":1,"c":2}}`})
	if err := verify(xpi, dir); err == nil || !strings.Contains(err.Error(), `version: "2.0.0", exported "1.0.0"`) {
		t.Errorf("changed value: %v", err)
	}
}

func TestRunUsage(t *testing.T) {
	t.Setenv("WEB_EXT_API_KEY", "")
	for _, args := range [][]string{nil, {"nope"}, {"fetch"}, {"fetch", "-version", "1", "-out", "x"}, {"verify"}} {
		if err := run(args); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
}
