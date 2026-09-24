// Command vaultr is a fast keyword search over Vault KV paths and key names.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/config"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/search"
	"github.com/rnsc/vaultr/internal/tui"
	"github.com/rnsc/vaultr/internal/vault"
)

var version = "dev"

// errNoMatch exits 1 without a message, like grep.
var errNoMatch = errors.New("no match")

const usage = `vaultr: search Vault KV paths and key names, fetch values on demand.

Usage:
  vaultr [QUERY...]              interactive search (TUI)
  vaultr find [flags] QUERY...   print matching path/key pairs
  vaultr get [flags] PATH [KEY]  print a secret, or one key's value
  vaultr index                   rebuild the local index now
  vaultr status                  show cache state
  vaultr purge                   delete the local index and its key
  vaultr config [show|path|init] show settings, or write a config template
  vaultr version

Query syntax: space separated terms, all must match (case-insensitive
substring of the path or key name). Prefix a term with k: or p: to match
only key names or only paths, e.g. "prod k:password".

Connection uses the standard VAULT_ADDR (or VAULT_URL), VAULT_TOKEN (or
~/.vault-token), VAULT_NAMESPACE, VAULT_CACERT, VAULT_CLIENT_CERT,
VAULT_CLIENT_KEY and VAULT_SKIP_VERIFY variables.

Settings can also live in ~/.config/vaultr/config.toml (see "vaultr config
init"); environment variables take precedence over the file.

Settings:
  VAULTR_CONFIG      config file path
  VAULTR_MOUNTS      comma separated KV mounts to index (default: discover)
  VAULTR_WORKERS     concurrent requests while indexing (default 32)
  VAULTR_MAX_AGE     cache lifetime, capped at 2h (default 2h)
  VAULTR_PATHS_ONLY  "1" to index paths without reading key names
  VAULTR_CLIP_CLEAR  clear copied values from the clipboard after this
                     long in the TUI (default 45s, 0 disables)
  VAULTR_CACHE_DIR   where the encrypted index lives
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := run(ctx, os.Args[1:])
	stop()
	if errors.Is(err, errNoMatch) {
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vaultr:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	case "version", "--version":
		fmt.Println(version)
		return nil
	case "config":
		return configCmd(args[1:])
	}

	a, err := newApp()
	if err != nil {
		return err
	}
	switch cmd {
	case "find", "search", "f":
		return a.find(ctx, args[1:])
	case "get", "g":
		return a.get(ctx, args[1:])
	case "index", "reindex", "refresh":
		_, _, err := a.build(ctx, true)
		return err
	case "status":
		return a.status(ctx)
	case "purge":
		if err := a.store.Purge(ctx); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "cache purged")
		return nil
	}
	if strings.HasPrefix(cmd, "-") {
		return fmt.Errorf("unknown flag %s (see vaultr help)", cmd)
	}
	return a.interactive(ctx, strings.Join(args, " "))
}

type app struct {
	client    *vault.Client
	store     *cache.Store
	maxAge    time.Duration
	workers   int
	pathsOnly bool
	mounts    []string
	clipClear time.Duration
}

func newApp() (*app, error) {
	s, err := config.Load()
	if err != nil {
		return nil, err
	}
	if err := s.RequireToken(); err != nil {
		return nil, err
	}
	c, err := vault.New(s.Vault)
	if err != nil {
		return nil, err
	}
	return &app{
		client:    c,
		store:     &cache.Store{Dir: s.CacheDir, Client: c},
		maxAge:    s.MaxAge,
		workers:   s.Workers,
		pathsOnly: s.PathsOnly,
		mounts:    s.Mounts,
		clipClear: s.ClipClear,
	}, nil
}

func configCmd(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "", "show":
		s, err := config.Load()
		if err != nil {
			return err
		}
		fmt.Print(s.Describe())
		return nil
	case "path":
		fmt.Println(config.DefaultPath())
		return nil
	case "init":
		p := config.DefaultPath()
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%s already exists", p)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(config.Template), 0o600); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "wrote", p)
		return nil
	}
	return fmt.Errorf("unknown config command %q (show, path, init)", sub)
}

func (a *app) kvMounts(ctx context.Context) ([]vault.Mount, error) {
	if len(a.mounts) == 0 {
		ms, err := a.client.KVMounts(ctx)
		if err != nil {
			return nil, err
		}
		if len(ms) == 0 {
			return nil, errors.New("no KV mounts visible to this token (set VAULTR_MOUNTS)")
		}
		return ms, nil
	}
	var out []vault.Mount
	for _, p := range a.mounts {
		m, err := a.client.MountFor(ctx, p)
		if err != nil {
			return nil, fmt.Errorf("mount %s: %w", p, err)
		}
		out = append(out, m)
	}
	return out, nil
}

// buildIndex crawls and saves. It never prints.
func (a *app) buildIndex(ctx context.Context, onProgress func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
	if _, err := a.client.LookupSelf(ctx); err != nil {
		if errors.Is(err, vault.ErrForbidden) {
			err = errors.New("token is expired or invalid, run `vault login`")
		}
		return nil, cache.Header{}, "", err
	}
	mounts, err := a.kvMounts(ctx)
	if err != nil {
		return nil, cache.Header{}, "", err
	}
	res, err := index.Build(ctx, a.client, index.Options{
		Mounts:     mounts,
		Workers:    a.workers,
		PathsOnly:  a.pathsOnly,
		OnProgress: onProgress,
	})
	if err != nil {
		return nil, cache.Header{}, "", err
	}
	h, warn, err := a.store.Save(ctx, res.Entries, a.maxAge)
	if err != nil {
		return nil, cache.Header{}, "", err
	}
	var notes []string
	if warn != "" {
		notes = append(notes, warn)
	}
	if res.Denied > 0 {
		notes = append(notes, fmt.Sprintf("%d paths denied by policy", res.Denied))
	}
	if len(res.Errors) > 0 {
		notes = append(notes, fmt.Sprintf("%d errors, first: %v", len(res.Errors), res.Errors[0]))
	}
	return res.Entries, h, strings.Join(notes, "; "), nil
}

// build rebuilds with progress on stderr.
func (a *app) build(ctx context.Context, verbose bool) ([]index.Entry, cache.Header, error) {
	tty := isatty.IsTerminal(os.Stderr.Fd())
	start := time.Now()
	entries, h, note, err := a.buildIndex(ctx, func(p index.Progress) {
		if tty {
			fmt.Fprintf(os.Stderr, "\r\033[Kindexing %s: %d secrets, %d folders", a.client.Addr, p.Secrets, p.Lists)
		}
	})
	if tty {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
	if err != nil {
		return nil, h, err
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "indexed %d secrets in %s, cache valid until %s\n",
			len(entries), time.Since(start).Round(time.Millisecond), h.Expires.Local().Format("15:04"))
	}
	if note != "" {
		fmt.Fprintln(os.Stderr, "warning:", note)
	}
	return entries, h, nil
}

// load returns the cached index, rebuilding it when stale.
func (a *app) load(ctx context.Context) ([]index.Entry, cache.Header, error) {
	entries, h, err := a.store.Load(ctx)
	if err == nil {
		return entries, h, nil
	}
	if !errors.Is(err, cache.ErrStale) {
		return nil, h, err
	}
	return a.build(ctx, false)
}

func (a *app) interactive(ctx context.Context, query string) error {
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return a.find(ctx, strings.Fields(query))
	}
	entries, h, err := a.store.Load(ctx)
	if err != nil && !errors.Is(err, cache.ErrStale) {
		return err
	}
	return tui.Run(tui.Options{
		Client:    a.client,
		Entries:   entries, // nil triggers a build inside the TUI
		Header:    h,
		Build:     a.buildIndex,
		Query:     query,
		ClipClear: a.clipClear,
	})
}

func (a *app) find(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output JSON lines")
	limit := fs.Int("n", 0, "maximum results (0 = all)")
	withValues := fs.Bool("values", false, "also fetch and print matching values (live from Vault)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	q := strings.Join(pos, " ")
	if strings.TrimSpace(q) == "" {
		return errors.New("find: missing query")
	}
	entries, _, err := a.load(ctx)
	if err != nil {
		return err
	}
	rows := search.New(entries).Search(q, *limit)
	enc := json.NewEncoder(os.Stdout)
	secrets := map[string]map[string]string{}
	for _, r := range rows {
		var val *string
		if *withValues && r.Key != "" {
			vals, ok := secrets[r.Entry.Path]
			if !ok {
				data, err := a.client.ReadSecret(ctx, vault.Mount{Path: r.Entry.Mount, KVVersion: r.Entry.KV}, r.Entry.Rel())
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: %s: %v\n", r.Entry.Path, err)
				}
				vals = vault.Stringify(data)
				secrets[r.Entry.Path] = vals
			}
			if v, ok := vals[r.Key]; ok {
				val = &v
			}
		}
		if *asJSON {
			_ = enc.Encode(struct {
				Path  string  `json:"path"`
				Key   string  `json:"key,omitempty"`
				Value *string `json:"value,omitempty"`
			}{r.Entry.Path, r.Key, val})
			continue
		}
		line := r.Entry.Path
		if r.Key != "" {
			line += "\t" + r.Key
		}
		if val != nil {
			line += "\t" + *val
		}
		fmt.Println(line)
	}
	if len(rows) == 0 {
		return errNoMatch
	}
	return nil
}

func (a *app) get(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output JSON")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return errors.New("usage: vaultr get PATH [KEY]")
	}
	path := strings.Trim(pos[0], "/")
	m, err := a.client.MountFor(ctx, path)
	if err != nil {
		return err
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(path, strings.TrimSuffix(m.Path, "/")), "/")
	data, err := a.client.ReadSecret(ctx, m, rel)
	if err != nil {
		return err
	}
	vals := vault.Stringify(data)
	if len(pos) == 2 {
		key := pos[1]
		v, ok := vals[key]
		if !ok {
			return fmt.Errorf("%s has no key %q", path, key)
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(data[key])
		}
		fmt.Print(v)
		if isatty.IsTerminal(os.Stdout.Fd()) {
			fmt.Println()
		}
		return nil
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(data)
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s\t%s\n", k, vals[k])
	}
	return nil
}

func (a *app) status(ctx context.Context) error {
	fmt.Printf("server:   %s\n", a.client.Addr)
	if a.client.Namespace != "" {
		fmt.Printf("namespace: %s\n", a.client.Namespace)
		if tns, by, err := a.client.TokenNamespace(ctx); err == nil {
			if tns == "" {
				tns = "(root)"
			}
			fmt.Printf("token ns: %s (%s)\n", tns, by)
		}
	}
	fmt.Printf("file:     %s\n", a.store.File())
	h, err := a.store.Status()
	if errors.Is(err, os.ErrNotExist) {
		fmt.Println("cache:    none")
		return nil
	}
	if err != nil {
		fmt.Printf("cache:    unreadable (%v)\n", err)
		return nil
	}
	fmt.Printf("created:  %s\n", h.Created.Local().Format(time.DateTime))
	left := time.Until(h.Expires).Round(time.Second)
	if left <= 0 {
		fmt.Printf("expires:  %s (expired, will be deleted on next use)\n", h.Expires.Local().Format(time.DateTime))
	} else {
		fmt.Printf("expires:  %s (in %s)\n", h.Expires.Local().Format(time.DateTime), left)
	}
	if h.Bound() {
		fmt.Println("key:      bound to token cubbyhole (unreadable once the token expires)")
	} else {
		fmt.Println("key:      token only (cubbyhole unavailable)")
	}
	if entries, _, err := a.store.Load(ctx); err == nil {
		fmt.Printf("secrets:  %d\n", len(entries))
	} else {
		fmt.Printf("state:    %v\n", err)
	}
	return nil
}

// parseInterspersed allows flags anywhere among positional arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
