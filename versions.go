package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rnsc/vaultr/internal/auth"
	"github.com/rnsc/vaultr/internal/vault"
)

// mountAndRel resolves a logical path to its mount and the path in it.
func (a *app) mountAndRel(ctx context.Context, path string) (vault.Mount, string, error) {
	path = strings.Trim(path, "/")
	m, err := a.client.MountFor(ctx, path)
	if err != nil {
		return vault.Mount{}, "", err
	}
	return m, strings.TrimPrefix(strings.TrimPrefix(path, strings.TrimSuffix(m.Path, "/")), "/"), nil
}

// versionState describes a version for people.
func versionState(v vault.Version, current int) string {
	switch {
	case v.Destroyed:
		return "destroyed"
	case !v.Deleted.IsZero():
		return "deleted " + v.Deleted.Local().Format(time.DateTime)
	case v.N == current:
		return "current"
	}
	return ""
}

// versions lists a KV v2 secret's versions, newest first. Vault records
// when each was written, not by whom.
func (a *app) versions(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("versions", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output JSON")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: vaultr versions [--json] PATH")
	}
	m, rel, err := a.mountAndRel(ctx, pos[0])
	if err != nil {
		return err
	}
	meta, err := a.client.SecretMetadata(ctx, m, rel)
	if err != nil {
		return err
	}
	if *asJSON {
		type ver struct {
			Version   int        `json:"version"`
			Created   time.Time  `json:"created"`
			Deleted   *time.Time `json:"deleted,omitempty"`
			Destroyed bool       `json:"destroyed,omitempty"`
			Current   bool       `json:"current,omitempty"`
		}
		out := []ver{}
		for i := len(meta.Versions) - 1; i >= 0; i-- {
			v := meta.Versions[i]
			j := ver{Version: v.N, Created: v.Created, Destroyed: v.Destroyed, Current: v.N == meta.Current}
			if !v.Deleted.IsZero() {
				j.Deleted = &v.Deleted
			}
			out = append(out, j)
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	out := tableWriter()
	defer out.Flush()
	fmt.Fprintln(out, "VERSION\tCREATED\tSTATE")
	for i := len(meta.Versions) - 1; i >= 0; i-- {
		v := meta.Versions[i]
		fmt.Fprintf(out, "%d\t%s\t%s\n", v.N, v.Created.Local().Format(time.DateTime), versionState(v, meta.Current))
	}
	return nil
}

// open opens a secret's page in the Vault web UI (or prints its address),
// for what vaultr doesn't do, like editing.
func (a *app) open(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	printOnly := fs.Bool("print", false, "print the address instead of opening a browser")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: vaultr open [--print] PATH")
	}
	m, rel, err := a.mountAndRel(ctx, pos[0])
	if err != nil {
		return err
	}
	u := a.client.UIURL(m, rel)
	if *printOnly {
		fmt.Println(u)
		return nil
	}
	fmt.Fprintln(os.Stderr, "opening", u)
	return auth.OpenBrowser(u)
}
