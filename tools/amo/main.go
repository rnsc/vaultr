// Command amo helps the release workflow sign the Firefox add-on on
// addons.mozilla.org (AMO); scripts/sign-firefox.sh calls it.
//
//	go run ./tools/amo fetch -version 1.4.2 -out vaultr.xpi
//	go run ./tools/amo verify -xpi vaultr.xpi -dir exported/
//
// fetch downloads the signed file of a version AMO has already signed:
// AMO signs each version once, so rerunning a release can't sign again.
// verify checks that a signed add-on holds exactly the exported files
// (plus AMO's signature in META-INF/), so a fetched file can't be from
// other sources than this release's.
//
// The API key comes from WEB_EXT_API_KEY (the JWT issuer) and
// WEB_EXT_API_SECRET, the variables web-ext reads too.
package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

// guid is the add-on's ID (browser_specific_settings.gecko.id).
const guid = "vaultr@rnsc.be"

const defaultAPI = "https://addons.mozilla.org/api/v5/"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "amo:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: amo fetch|verify [flags]")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "fetch":
		version := fs.String("version", "", "add-on version, e.g. 1.4.2")
		out := fs.String("out", "", "where to write the signed .xpi")
		api := fs.String("api", defaultAPI, "AMO API base address")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *version == "" || *out == "" {
			return errors.New("fetch needs -version and -out")
		}
		c := &client{
			api:    strings.TrimSuffix(*api, "/") + "/",
			issuer: os.Getenv("WEB_EXT_API_KEY"),
			secret: os.Getenv("WEB_EXT_API_SECRET"),
			http:   &http.Client{Timeout: 2 * time.Minute},
		}
		if c.issuer == "" || c.secret == "" {
			return errors.New("WEB_EXT_API_KEY and WEB_EXT_API_SECRET must be set")
		}
		b, err := c.signedFile(context.Background(), *version)
		if err != nil {
			return err
		}
		return os.WriteFile(*out, b, 0o644)
	case "verify":
		xpi := fs.String("xpi", "", "signed add-on")
		dir := fs.String("dir", "", "the exported add-on it was signed from")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *xpi == "" || *dir == "" {
			return errors.New("verify needs -xpi and -dir")
		}
		return verify(*xpi, *dir)
	}
	return fmt.Errorf("unknown command %q (fetch, verify)", args[0])
}

type client struct {
	api            string
	issuer, secret string
	http           *http.Client
}

// jwt is AMO's API authentication: an HS256 token signed with the API
// secret, valid for a minute.
func (c *client) jwt(now time.Time) string {
	enc := base64.RawURLEncoding
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iss": c.issuer,
		"jti": hex.EncodeToString(nonce),
		"iat": now.Unix(),
		"exp": now.Add(time.Minute).Unix(),
	})
	unsigned := header + "." + enc.EncodeToString(claims)
	mac := hmac.New(sha256.New, []byte(c.secret))
	mac.Write([]byte(unsigned))
	return unsigned + "." + enc.EncodeToString(mac.Sum(nil))
}

func (c *client) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "JWT "+c.jwt(time.Now()))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d: %.300s", u, resp.StatusCode, b)
	}
	return b, nil
}

// signedFile downloads the signed file of version. AMO addresses a
// version by number with a "v" prefix (a bare number is a version ID).
func (c *client) signedFile(ctx context.Context, version string) ([]byte, error) {
	u := c.api + "addons/addon/" + url.PathEscape(guid) + "/versions/v" + url.PathEscape(version) + "/"
	b, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	var v struct {
		Version string `json:"version"`
		File    struct {
			URL    string `json:"url"`
			Status string `json:"status"`
			Hash   string `json:"hash"` // "sha256:..."
		} `json:"file"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("version %s: %w", version, err)
	}
	if v.Version != version || v.File.URL == "" {
		return nil, fmt.Errorf("version %s: unexpected answer %.300s", version, b)
	}
	if v.File.Status != "public" {
		return nil, fmt.Errorf("version %s is %q on AMO, not signed yet", version, v.File.Status)
	}
	data, err := c.get(ctx, v.File.URL)
	if err != nil {
		return nil, err
	}
	if want, ok := strings.CutPrefix(v.File.Hash, "sha256:"); ok {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != want {
			return nil, fmt.Errorf("version %s: downloaded file doesn't match AMO's hash", version)
		}
	}
	return data, nil
}

// jsonChanges describes how got differs from want, field by field, or
// says the difference is only in formatting. Values in the add-on's
// manifest aren't secret.
func jsonChanges(got, want []byte) string {
	var g, w any
	if json.Unmarshal(got, &g) != nil || json.Unmarshal(want, &w) != nil {
		return " (not valid JSON)"
	}
	var changes []string
	diffJSON("", g, w, &changes)
	if len(changes) == 0 {
		return " (formatting only, same JSON)"
	}
	return ":\n    " + strings.Join(changes, "\n    ")
}

func diffJSON(path string, got, want any, out *[]string) {
	gm, gok := got.(map[string]any)
	wm, wok := want.(map[string]any)
	if gok && wok {
		keys := map[string]bool{}
		for k := range gm {
			keys[k] = true
		}
		for k := range wm {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			p := k
			if path != "" {
				p = path + "." + k
			}
			gv, inG := gm[k]
			wv, inW := wm[k]
			switch {
			case !inG:
				*out = append(*out, p+": removed (was "+short(wv)+")")
			case !inW:
				*out = append(*out, p+": added: "+short(gv))
			default:
				diffJSON(p, gv, wv, out)
			}
		}
		return
	}
	if !reflect.DeepEqual(got, want) {
		*out = append(*out, path+": "+short(got)+", exported "+short(want))
	}
}

func short(v any) string {
	b, _ := json.Marshal(v)
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}

// verify checks that the .xpi holds exactly dir's files, byte for byte,
// besides META-INF/ (the signature).
func verify(xpi, dir string) error {
	zr, err := zip.OpenReader(xpi)
	if err != nil {
		return err
	}
	defer zr.Close()
	inZip := map[string]*zip.File{}
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "META-INF/") || strings.HasSuffix(f.Name, "/") {
			continue
		}
		inZip[f.Name] = f
	}
	var problems []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		name := filepath.ToSlash(rel)
		want, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, ok := inZip[name]
		if !ok {
			problems = append(problems, name+": missing from the add-on")
			return nil
		}
		delete(inZip, name)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			msg := name + ": differs"
			if strings.HasSuffix(name, ".json") {
				// Say what changed, so a change made by signing can be
				// told from a wrong file.
				msg += jsonChanges(got, want)
			}
			problems = append(problems, msg)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for name := range inZip {
		problems = append(problems, name+": not in this release's files")
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%s doesn't match %s:\n  %s", xpi, dir, strings.Join(problems, "\n  "))
	}
	return nil
}
