// Command vaultr is a fast keyword search over Vault KV paths and key names.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mattn/go-isatty"
	"golang.org/x/term"

	"github.com/rnsc/vaultr/internal/auth"
	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/complete"
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
  vaultr [-r] [QUERY...]         interactive search (TUI); -r refreshes the
                                 index first; ^n switches namespace
  vaultr find [flags] QUERY...   print matching path/key pairs (-r refreshes
                                 the index first; --all-ns searches every
                                 namespace, which becomes the first column)
  vaultr get [flags] PATH [KEY]  print a secret, or one key's value
  vaultr versions PATH           list a secret's versions (KV v2); read an
                                 older one with get --version N
  vaultr open [--print] PATH     open a secret in the Vault web UI
  vaultr env [flags] PATH...     print the secrets' keys as shell exports
                                 (--prefix APP_, --format sh|fish|json)
  vaultr exec PATH... -- CMD     run CMD with the secrets' keys as
                                 environment variables
  vaultr index                   refresh the local index now (also: refresh)
  vaultr status                  show cache state
  vaultr purge                   delete the local index and its key
  vaultr login [flags]           log in (oidc, ldap, userpass, token) and
                                 save the token to ~/.vault-token
  vaultr config [show|path|init] show settings, or write a config template
  vaultr completion install      set up tab completion for your shell
  vaultr completion SHELL        print the completion script (see
                                 vaultr completion list)
  vaultr version

Every command except login takes --ns NAMESPACE (or --namespace) to work
in another namespace for this run; "/" is the root namespace. Tab
completes namespace names.

Query syntax: space separated terms, all must match (case-insensitive
substring of the path or key name). Prefix a term with k: or p: to match
only key names or only paths, e.g. "prod k:password".

Connection uses the standard VAULT_ADDR (or VAULT_URL), VAULT_TOKEN (or
~/.vault-token), VAULT_NAMESPACE, VAULT_CACERT, VAULT_CLIENT_CERT,
VAULT_CLIENT_KEY and VAULT_SKIP_VERIFY variables.

Settings can also live in ~/.config/vaultr/config.toml (see "vaultr config
init", or press ctrl+e in the TUI); environment variables take precedence
over the file. Its [auth] section sets the defaults for "vaultr login".

Settings:
  VAULTR_CONFIG      config file path
  VAULTR_MOUNTS      comma separated KV mounts to index (default: discover)
  VAULTR_WORKERS     concurrent requests while indexing (default 32)
  VAULTR_MAX_AGE     cache lifetime, capped at 2h (default 2h)
  VAULTR_PATHS_ONLY  "1" to index paths without reading key names
  VAULTR_CLIP_CLEAR  clear copied values from the clipboard after this
                     long in the TUI (default 45s, 0 disables)
  VAULTR_REVEAL_TIMEOUT hide revealed values in the TUI again after
                     this long (default 30s, 0 keeps them shown)
  VAULTR_CACHE_DIR   where the encrypted index lives
  VAULTR_ALLOW_DEBUG "1" lets debuggers attach to vaultr (it normally
                     blocks that, and memory reads by other processes)
`

func main() {
	harden()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := run(ctx, os.Args[1:])
	stop()
	if errors.Is(err, errNoMatch) {
		os.Exit(1)
	}
	var ee exitError
	if errors.As(err, &ee) {
		os.Exit(ee.code) // vaultr exec: the command's own status
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
	case "completion":
		return completionCmd(args[1:])
	case "__complete":
		completeCmd(ctx, args[1:])
		return nil
	}

	var nsOverride *string
	if cmd != "login" {
		var err error
		if nsOverride, args, err = namespaceFlag(args); err != nil {
			return err
		}
		cmd = ""
		if len(args) > 0 {
			cmd = args[0]
		}
	}

	refresh := false
	if cmd == "-r" || cmd == "--refresh" {
		refresh, args = true, args[1:]
		cmd = ""
		if len(args) > 0 {
			cmd = args[0]
		}
	}
	interactive := cmd == "" || !isCommand(cmd)
	a, err := newApp(false, nsOverride)
	if err != nil {
		return err
	}
	if !interactive {
		switch cmd {
		case "login":
		case "find", "search", "f", "get", "g", "versions", "open", "env", "exec", "index", "reindex", "refresh":
			err = a.ensureToken(ctx)
		default:
			err = a.settings.RequireToken()
		}
		if err != nil {
			return err
		}
	}
	switch cmd {
	case "login":
		return a.login(ctx, args[1:])
	case "find", "search", "f":
		return a.find(ctx, args[1:])
	case "get", "g":
		return a.get(ctx, args[1:])
	case "versions":
		return a.versions(ctx, args[1:])
	case "open":
		return a.open(ctx, args[1:])
	case "env":
		return a.env(ctx, args[1:])
	case "exec":
		return a.exec(ctx, args[1:])
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
	return a.interactive(ctx, strings.Join(args, " "), refresh)
}

type app struct {
	settings  *config.Settings
	client    *vault.Client
	store     *cache.Store
	maxAge    time.Duration
	workers   int
	pathsOnly bool
	mounts    []string
	clipClear time.Duration
	// namespace overrides the configured one (--ns, or a switch in the
	// TUI); it survives config reloads. nsSource says which.
	namespace *string
	nsSource  string
}

// newApp loads settings and builds the client. Only the TUI and `login`
// can start without a token. ns, when set, overrides the namespace.
func newApp(requireToken bool, ns *string) (*app, error) {
	s, err := config.Load()
	if err != nil {
		return nil, err
	}
	overrideNamespace(s, ns, "flag --ns")
	if requireToken {
		if err := s.RequireToken(); err != nil {
			return nil, err
		}
	}
	c, err := vault.New(s.Vault)
	if err != nil {
		return nil, err
	}
	a := &app{namespace: ns, nsSource: "flag --ns"}
	a.apply(s, c)
	return a, nil
}

func overrideNamespace(s *config.Settings, ns *string, source string) {
	if ns != nil {
		s.Vault.Namespace = *ns
		s.Source["namespace"] = source
	}
}

// nsFlags are the spellings of the global namespace flag.
var nsFlags = []string{"--ns", "-ns", "--namespace", "-namespace"}

// namespaceFlag removes --ns NS (or --ns=NS, --namespace, single dash)
// from args, wherever it appears, and returns its value: "/" or "" is the
// root namespace.
func namespaceFlag(args []string) (*string, []string, error) {
	var ns *string
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		name, val, hasVal := strings.Cut(a, "=")
		if !slices.Contains(nsFlags, name) {
			out = append(out, a)
			continue
		}
		if !hasVal {
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf(`%s needs a namespace ("/" for the root)`, name)
			}
			i++
			val = args[i]
		}
		v := strings.Trim(val, "/")
		ns = &v
	}
	return ns, out, nil
}

func (a *app) apply(s *config.Settings, c *vault.Client) {
	a.settings = s
	a.client = c
	a.store = &cache.Store{Dir: s.CacheDir, Client: c}
	a.maxAge = s.MaxAge
	a.workers = s.Workers
	a.pathsOnly = s.PathsOnly
	a.mounts = s.Mounts
	a.clipClear = s.ClipClear
}

// Client, Settings, LoadCache, Build, Login and Reload implement
// tui.Backend.
func (a *app) Client() *vault.Client      { return a.client }
func (a *app) Settings() *config.Settings { return a.settings }

func (a *app) LoadCache(ctx context.Context) ([]index.Entry, cache.Header, error) {
	return a.store.Load(ctx)
}

func (a *app) Build(ctx context.Context, onProgress func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
	return a.buildIndex(ctx, onProgress)
}

// Login logs in, switches the client to the new token, and saves it to
// ~/.vault-token when configured to. It returns a short summary and a
// warning, either possibly empty.
func (a *app) Login(ctx context.Context, r auth.Request) (string, string, error) {
	res, err := auth.Login(ctx, a.client, r)
	if err != nil {
		return "", "", err
	}
	a.client.SetToken(res.Token, res.Namespace)
	summary := "logged in"
	if res.Namespace != "" {
		summary += " to namespace " + res.Namespace
	}
	if res.TTL > 0 {
		summary += ", token valid for " + res.TTL.Round(time.Minute).String()
	}
	var warn string
	if a.settings.Auth.SaveToken {
		w, err := auth.SaveToken(res.Token)
		if err != nil {
			warn = "could not save the token to ~/.vault-token: " + err.Error()
		} else {
			warn = w
		}
	}
	return summary, warn, nil
}

// Reload re-reads the config file and environment, keeping the current
// token (which may come from a login in this session).
func (a *app) Reload() error {
	s, err := config.Load()
	if err != nil {
		return err
	}
	tok := a.client.Token()
	ns, by, known := a.client.KnownTokenNamespace()
	if tok != "" {
		s.Vault.Token = tok
	}
	overrideNamespace(s, a.namespace, a.nsSource)
	c, err := vault.New(s.Vault)
	if err != nil {
		return err
	}
	if known && by == "login" {
		c.SetToken(tok, ns)
	}
	a.apply(s, c)
	return nil
}

// Namespaces lists the namespaces the token can use.
func (a *app) Namespaces(ctx context.Context) ([]string, error) {
	return a.client.ListNamespaces(ctx)
}

// SwitchNamespace makes ns the namespace for the rest of the session. The
// index is per namespace, so the caller loads or builds it next.
func (a *app) SwitchNamespace(ns string) {
	a.namespace, a.nsSource = &ns, "switched in the TUI"
	overrideNamespace(a.settings, &ns, a.nsSource)
	a.apply(a.settings, a.client.InNamespace(ns))
}

// Adopt keeps an index built with an earlier token after a new login by
// the same identity, saving it under the new token.
func (a *app) Adopt(ctx context.Context, entries []index.Entry, prev cache.Header) (cache.Header, bool, string, error) {
	return a.store.Adopt(ctx, entries, prev, a.maxAge)
}

// Recent and AddRecent keep the recently opened rows in the encrypted
// index (tui.Backend).
func (a *app) Recent() []string { return a.store.Recent() }

func (a *app) AddRecent(item string) { _ = a.store.AddRecent(item) }

// LoginRequest fills a login request from the configured defaults.
func LoginRequest(s config.AuthSettings) auth.Request {
	m, err := auth.ParseMethod(s.Method)
	if err != nil {
		m = auth.OIDC
	}
	return auth.Request{
		Method:       m,
		Mount:        s.Mount,
		Namespace:    s.Namespace,
		Username:     s.Username,
		Role:         s.Role,
		CallbackPort: s.CallbackPort,
	}
}

func (a *app) login(ctx context.Context, args []string) error {
	r := LoginRequest(a.settings.Auth)
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	method := fs.String("method", string(r.Method), "oidc, ldap, userpass or token")
	fs.StringVar(&r.Mount, "mount", r.Mount, "auth mount path (default: the method name)")
	ns := fs.String("namespace", nsFlag(r.Namespace), `namespace to log in to ("/" = root)`)
	fs.StringVar(&r.Username, "username", r.Username, "username (ldap, userpass)")
	fs.StringVar(&r.Role, "role", r.Role, "OIDC role")
	fs.IntVar(&r.CallbackPort, "callback-port", r.CallbackPort, "OIDC callback port (default 8250)")
	noSave := fs.Bool("no-save", false, "print the token instead of saving it to ~/.vault-token")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return fmt.Errorf("login: unexpected argument %q", pos[0])
	}
	m, err := auth.ParseMethod(*method)
	if err != nil {
		return err
	}
	r.Method = m
	r.Namespace = strings.Trim(*ns, "/")
	if *noSave {
		a.settings.Auth.SaveToken = false
	}
	if err := a.promptLogin(ctx, r); err != nil {
		return err
	}
	if !a.settings.Auth.SaveToken {
		// Nothing persisted: hand the token to the caller.
		fmt.Println(a.client.Token())
	}
	return nil
}

// promptLogin asks for what the method needs on the terminal, logs in and
// reports on stderr.
func (a *app) promptLogin(ctx context.Context, r auth.Request) error {
	var err error
	switch r.Method {
	case auth.LDAP, auth.Userpass:
		if r.Username == "" {
			if r.Username, err = prompt("Username: ", false); err != nil {
				return err
			}
		}
		if r.Password, err = prompt("Password: ", true); err != nil {
			return err
		}
	case auth.Token:
		if r.Token, err = prompt("Token: ", true); err != nil {
			return err
		}
	case auth.OIDC:
		r.OpenURL = func(u string) {
			fmt.Fprintf(os.Stderr, "Complete the login in your browser. If it did not open, visit:\n\n  %s\n\n", u)
			_ = auth.OpenBrowser(u)
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}
	summary, warn, err := a.Login(ctx, r)
	if err != nil {
		return err
	}
	where := ""
	if a.settings.Auth.SaveToken {
		where = ", token saved to ~/.vault-token"
	}
	fmt.Fprintf(os.Stderr, "%s%s\n", strings.ToUpper(summary[:1])+summary[1:], where)
	if warn != "" {
		fmt.Fprintln(os.Stderr, "warning:", warn)
	}
	return nil
}

// ensureToken makes sure a command has a token. With auth.auto_login on a
// terminal, a missing, expired or revoked token means logging in first;
// otherwise a missing token is an error and a dead one fails later with a
// hint to log in. Scripts (no terminal) never get a login prompt.
func (a *app) ensureToken(ctx context.Context) error {
	if !a.settings.Auth.AutoLogin || !term.IsTerminal(int(os.Stdin.Fd())) || !isatty.IsTerminal(os.Stderr.Fd()) {
		return a.settings.RequireToken()
	}
	why := "no Vault token found"
	if a.client.Token() != "" {
		_, err := a.client.LookupSelf(ctx)
		if !errors.Is(err, vault.ErrTokenInvalid) {
			return nil // valid, or another problem the command will report
		}
		why = "the token is expired or revoked"
	}
	fmt.Fprintf(os.Stderr, "%s; logging in (auth.auto_login)\n", why)
	return a.promptLogin(ctx, LoginRequest(a.settings.Auth))
}

func nsFlag(ns string) string {
	if ns == "" {
		return "/"
	}
	return ns
}

// prompt reads a line from the terminal (without echo when secret), or
// from stdin when it is not a terminal.
func prompt(label string, secret bool) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, label)
		if secret {
			b, err := term.ReadPassword(fd)
			fmt.Fprintln(os.Stderr)
			return strings.TrimSpace(string(b)), err
		}
	}
	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("reading %s%w", strings.ToLower(label), err)
	}
	return strings.TrimSpace(line), nil
}

var stdinReader = bufio.NewReader(os.Stdin)

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
		if err := os.WriteFile(p, config.Template(), 0o600); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "wrote", p)
		return nil
	}
	return fmt.Errorf("unknown config command %q (show, path, init)", sub)
}

func isCommand(cmd string) bool {
	switch cmd {
	case "find", "search", "f", "get", "g", "versions", "open", "env", "exec", "index", "reindex", "refresh", "status", "purge", "login":
		return true
	}
	return strings.HasPrefix(cmd, "-")
}

var errNoMounts = errors.New("no KV mounts visible to this token (set VAULTR_MOUNTS)")

func (a *app) kvMounts(ctx context.Context) ([]vault.Mount, error) {
	if len(a.mounts) == 0 {
		ms, err := a.client.KVMounts(ctx)
		if err != nil {
			return nil, err
		}
		if len(ms) == 0 {
			return nil, errNoMounts
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
		if errors.Is(err, vault.ErrTokenInvalid) {
			err = fmt.Errorf("%w; run `vaultr login`", vault.ErrTokenInvalid)
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

func (a *app) interactive(ctx context.Context, query string, refresh bool) error {
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		if err := a.settings.RequireToken(); err != nil {
			return err
		}
		return a.find(ctx, strings.Fields(query))
	}
	opt := tui.Options{Backend: a, Query: query}
	if a.client.Token() == "" {
		opt.Login = "No Vault token found."
		return tui.Run(opt)
	}
	if refresh {
		return tui.Run(opt) // no entries: the TUI rebuilds first
	}
	entries, h, err := a.store.Load(ctx)
	switch {
	case err == nil:
		opt.Entries, opt.Header = entries, h
	case errors.Is(err, cache.ErrStale):
		// The TUI builds the index (and asks to log in if the token is bad).
	default:
		// Server unreachable or similar: let the TUI show it and offer the
		// config editor rather than exiting.
	}
	return tui.Run(opt)
}

func (a *app) find(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output JSON lines")
	limit := fs.Int("n", 0, "maximum results (0 = all)")
	withValues := fs.Bool("values", false, "also fetch and print matching values (live from Vault)")
	var refresh bool
	fs.BoolVar(&refresh, "r", false, "refresh the index before searching (finds secrets added since)")
	fs.BoolVar(&refresh, "refresh", false, "same as -r")
	allNS := fs.Bool("all-ns", false, "search every namespace the token can use; the namespace is the first column")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	q := strings.Join(pos, " ")
	if strings.TrimSpace(q) == "" {
		return errors.New("find: missing query")
	}
	var entries []index.Entry
	var h cache.Header
	switch {
	case *allNS:
		entries, err = a.allNamespacesCLI(ctx, refresh)
	case refresh:
		entries, h, err = a.build(ctx, false)
	default:
		entries, h, err = a.load(ctx)
	}
	if err != nil {
		return err
	}
	rows := search.New(entries).Search(q, *limit)
	enc := json.NewEncoder(os.Stdout)
	out := tableWriter()
	defer out.Flush()
	secrets := map[string]map[string]string{}
	for _, r := range rows {
		var val *string
		if *withValues && r.Key != "" {
			id := r.Entry.Namespace + "\x00" + r.Entry.Path // the same path can exist in two namespaces
			vals, ok := secrets[id]
			if !ok {
				c := a.client
				if *allNS {
					c = c.InNamespace(r.Entry.Namespace)
				}
				data, err := c.ReadSecret(ctx, vault.Mount{Path: r.Entry.Mount, KVVersion: r.Entry.KV}, r.Entry.Rel())
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: %s: %v\n", r.Entry.Path, err)
				}
				vals = vault.Stringify(data)
				secrets[id] = vals
			}
			if v, ok := vals[r.Key]; ok {
				val = &v
			}
		}
		var ns *string
		if *allNS {
			l := nsLabel(r.Entry.Namespace)
			ns = &l
		}
		if *asJSON {
			_ = enc.Encode(struct {
				Namespace *string `json:"namespace,omitempty"`
				Path      string  `json:"path"`
				Key       string  `json:"key,omitempty"`
				Value     *string `json:"value,omitempty"`
			}{ns, r.Entry.Path, r.Key, val})
			continue
		}
		line := r.Entry.Path
		if ns != nil {
			line = *ns + "\t" + line
		}
		if r.Key != "" {
			line += "\t" + r.Key
		}
		if val != nil {
			line += "\t" + *val
		}
		fmt.Fprintln(out, line)
	}
	if len(rows) == 0 {
		if !refresh && isatty.IsTerminal(os.Stderr.Fd()) {
			fmt.Fprintf(os.Stderr, "no match in the index built %s ago; added it recently? retry with: vaultr find -r %s\n",
				time.Since(h.Created).Round(time.Second), q)
		}
		return errNoMatch
	}
	return nil
}

func (a *app) get(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output JSON")
	version := fs.Int("version", 0, "read this version (KV v2; see vaultr versions)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return errors.New("usage: vaultr get [--version N] PATH [KEY]")
	}
	path := strings.Trim(pos[0], "/")
	m, rel, err := a.mountAndRel(ctx, path)
	if err != nil {
		return err
	}
	var data map[string]any
	if *version > 0 {
		data, err = a.client.ReadSecretVersion(ctx, m, rel, *version)
		if errors.Is(err, vault.ErrNotFound) {
			err = fmt.Errorf("%s has no readable version %d (deleted, destroyed or never written; see vaultr versions)", path, *version)
		}
	} else {
		data, err = a.client.ReadSecret(ctx, m, rel)
	}
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
	out := tableWriter()
	defer out.Flush()
	for _, k := range keys {
		fmt.Fprintf(out, "%s\t%s\n", k, vals[k])
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

// tableWriter aligns tab-separated columns on a terminal and leaves them
// as plain tabs when the output is piped, for scripts.
func tableWriter() interface {
	io.Writer
	Flush() error
} {
	if isatty.IsTerminal(os.Stdout.Fd()) {
		return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	}
	return nopFlusher{os.Stdout}
}

type nopFlusher struct{ io.Writer }

func (nopFlusher) Flush() error { return nil }

func completionCmd(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "":
		return fmt.Errorf("usage: vaultr completion install [SHELL], or vaultr completion SHELL to print the script (shells: %s)",
			strings.Join(complete.ShellNames(), ", "))
	case "list":
		for _, n := range complete.ShellNames() {
			fmt.Println(n)
		}
		return nil
	case "install":
		shell := ""
		if len(args) > 1 {
			shell = args[1]
		} else {
			var err error
			if shell, err = complete.DetectShell(); err != nil {
				return err
			}
		}
		msg, err := complete.Install(shell)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, msg)
		return nil
	}
	script, err := complete.Script(sub)
	if err != nil {
		return err
	}
	fmt.Print(script)
	return nil
}

// completeCmd prints completion candidates for the words typed after
// "vaultr" (the last one being completed), one per line. It is called by
// the shell scripts on every tab, so it is quiet: any problem (no token,
// server unreachable) just means fewer or no candidates.
func completeCmd(ctx context.Context, words []string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for i := range words {
		words[i] = complete.Unescape(words[i])
	}
	// A --ns already typed applies to the paths being completed.
	var ns *string
	if len(words) > 0 {
		ns, _, _ = namespaceFlag(words[:len(words)-1]) // error: its value is being completed
	}
	var sources []complete.Source
	if a, err := newApp(false, ns); err == nil && a.client.Token() != "" {
		// The cache first (instant, and has key names); then Vault itself
		// for secrets added since, or when there is no valid cache.
		if entries, _, err := a.store.Load(ctx); err == nil {
			sources = append(sources, complete.IndexSource{Entries: entries})
		}
		sources = append(sources, &complete.LiveSource{Client: a.client, Mounts: a.mounts})
	}
	for _, c := range complete.Candidates(ctx, sources, words) {
		fmt.Println(c)
	}
}
