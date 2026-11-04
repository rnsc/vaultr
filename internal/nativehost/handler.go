package nativehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/search"
	"github.com/rnsc/vaultr/internal/vault"
)

// Search limits: results per response by default and at most. Paths and
// key names are short, so even the maximum stays far below MaxOutgoing;
// Search trims further if not.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// valueRoom is what a get response needs besides the value itself.
const valueRoom = 1024

// Handler answers the extension's requests. It keeps the loaded indexes
// in memory (paths and key names only), like the TUI, so typing a query
// searches without touching the disk or the network.
type Handler struct {
	Backend Backend
	Version string // vaultr's
	// Idle ends Serve after this long without a request, so a forgotten
	// connection doesn't keep the token in memory. 0 disables it.
	Idle time.Duration

	mu     sync.Mutex
	loaded map[string]*loadedIndex
}

type loadedIndex struct {
	ix   *search.Index
	h    cache.Header
	warn string
}

// allKey is the loaded-index key for all namespaces at once; real
// namespace paths can't contain a NUL.
const allKey = "\x00all"

// Serve reads requests from r and writes responses to w until r is closed
// (nil error), the idle timeout passes (nil), or ctx ends. Requests run
// one at a time, in order; "cancel" stops the running one.
func (h *Handler) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	out := &writer{w: w}

	// The running request, and cancels for requests still queued (a
	// cancel can arrive before its request starts).
	var cur struct {
		sync.Mutex
		id       int64
		cancel   context.CancelFunc
		canceled map[int64]bool
	}
	cur.canceled = map[int64]bool{}
	reqs := make(chan Request, 64)
	readErr := make(chan error, 1)
	go func() {
		defer close(reqs)
		for {
			b, err := ReadMsg(r)
			if err != nil {
				if errors.Is(err, ErrTooLarge) {
					// The stream can't be resynchronized after this.
					out.send(Response{Error: &Error{Code: CodeTooLarge, Message: err.Error()}})
				}
				readErr <- err
				return
			}
			var req Request
			if err := json.Unmarshal(b, &req); err != nil {
				out.send(Response{Error: &Error{Code: CodeBadRequest, Message: "invalid JSON"}})
				continue
			}
			if req.Type == "cancel" {
				cur.Lock()
				if cur.cancel != nil && cur.id == req.Target {
					cur.cancel()
				} else if req.Target > cur.id && len(cur.canceled) < cap(reqs) { // IDs increase: a lower one is done
					cur.canceled[req.Target] = true
				}
				cur.Unlock()
				continue
			}
			select {
			case reqs <- req:
			case <-ctx.Done():
				return
			}
		}
	}()

	var idle <-chan time.Time
	var timer *time.Timer
	if h.Idle > 0 {
		timer = time.NewTimer(h.Idle)
		defer timer.Stop()
		idle = timer.C
	}
	for {
		select {
		case req, ok := <-reqs:
			if !ok {
				err := <-readErr
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			rctx, cancel := context.WithCancel(ctx)
			cur.Lock()
			cur.id, cur.cancel = req.ID, cancel
			if cur.canceled[req.ID] {
				delete(cur.canceled, req.ID)
				cancel()
			}
			cur.Unlock()
			id := req.ID
			resp := h.handle(rctx, req, func(ev any) { out.send(Response{ID: id, Event: ev}) })
			cur.Lock()
			cur.cancel = nil
			cur.Unlock()
			cancel()
			resp.ID = id
			out.send(resp)
			if timer != nil {
				timer.Reset(h.Idle)
			}
		case <-idle:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// writer sends whole messages, one at a time. A response too large for
// the browser becomes an error response instead.
type writer struct {
	mu sync.Mutex
	w  io.Writer
}

func (o *writer) send(resp Response) {
	b, err := Encode(resp)
	if err != nil {
		b, _ = Encode(Response{ID: resp.ID, Error: &Error{Code: CodeTooLarge, Message: "response over the browser's 1 MB message limit"}})
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	_ = writeRaw(o.w, b) // a failed write means the browser is gone: reading stops next
}

// Handle answers one request; events go to event. Serve calls it; it is
// exported for tests.
func (h *Handler) Handle(ctx context.Context, req Request, event func(any)) Response {
	resp := h.handle(ctx, req, event)
	resp.ID = req.ID
	return resp
}

func (h *Handler) handle(ctx context.Context, req Request, event func(any)) Response {
	if req.Type == "hello" {
		return h.hello(req)
	}
	if p := problem(h.Backend.Info()); p != nil {
		return Response{Error: p}
	}
	var res any
	var err error
	switch req.Type {
	case "status":
		res, err = h.Backend.Status(ctx)
	case "namespaces":
		res, err = h.namespaces(ctx)
	case "search":
		res, err = h.search(ctx, req, event)
	case "refresh":
		res, err = h.refresh(ctx, req, event)
	case "get":
		res, err = h.get(ctx, req)
	case "login":
		res, err = h.login(ctx, req, event)
	default:
		err = &Error{Code: CodeBadRequest, Message: fmt.Sprintf("unknown request type %q", req.Type)}
	}
	if err != nil {
		return Response{Error: errorOf(ctx, err)}
	}
	return Response{Result: res}
}

func (h *Handler) hello(req Request) Response {
	h.Backend.Reload()
	h.forget()
	info := h.Backend.Info()
	if req.Protocol != Protocol {
		fix := "update the extension: run `vaultr extension install`, then reload it"
		if req.Protocol > Protocol {
			fix = "update vaultr"
		}
		return Response{Error: &Error{Code: CodeProtocol, Message: fmt.Sprintf(
			"the extension speaks protocol %d and vaultr %s speaks %d: %s", req.Protocol, h.Version, Protocol, fix)}}
	}
	res := HelloResult{
		Protocol:    Protocol,
		Version:     h.Version,
		Addr:        info.Addr,
		Namespace:   info.Namespace,
		ConfigPath:  info.ConfigPath,
		ConfigFound: info.ConfigFound,
		AuthMethod:  info.AuthMethod,
		Problem:     problem(info),
	}
	if w := versionWarning(req.Version, h.Version); w != "" {
		res.Warnings = append(res.Warnings, w)
	}
	return Response{Result: res}
}

// versionWarning compares the extension's version with vaultr's. Builds
// without a release version ("dev", or the extension's 0.0.0) don't warn.
func versionWarning(ext, host string) string {
	e, v := releaseVersion(ext), releaseVersion(host)
	if e == "" || v == "" || e == v {
		return ""
	}
	return fmt.Sprintf("the extension is version %s and vaultr %s: run `vaultr extension install` and reload the extension", e, v)
}

// releaseVersion normalizes "v1.2.3" and "1.2.3-rc.1" to "1.2.3"; it
// returns "" for development builds.
func releaseVersion(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "-")
	if s == "" || s == "dev" || s == "0.0.0" {
		return ""
	}
	return s
}

// problem says why vaultr can't work as configured, or nil. The browser
// starts vaultr without the user's shell environment, so the common case
// is an address set only in VAULT_ADDR.
func problem(info Info) *Error {
	if info.ConfigError != "" {
		return &Error{Code: CodeConfig, Message: info.ConfigError}
	}
	if info.AddrSource != "default" {
		return nil
	}
	if !info.ConfigFound {
		return &Error{Code: CodeNoConfig, Message: fmt.Sprintf(
			"vaultr found no config file at %s. The browser starts vaultr without your shell's environment, so VAULT_ADDR and other VAULT_* variables are not set. Run `vaultr config init` and set address in that file.",
			info.ConfigPath)}
	}
	return &Error{Code: CodeNoConfig, Message: fmt.Sprintf(
		"%s sets no address. The browser starts vaultr without your shell's environment, so VAULT_ADDR is not set. Set address in that file.",
		info.ConfigPath)}
}

// forget drops the loaded indexes, after a login or a config reload.
func (h *Handler) forget() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loaded = nil
}

// namespace resolves a request's namespace.
func (h *Handler) namespace(req Request) string {
	if req.Namespace == nil {
		return h.Backend.Info().Namespace
	}
	return strings.Trim(*req.Namespace, "/")
}

func (h *Handler) namespaces(ctx context.Context) (NamespacesResult, error) {
	list, err := h.Backend.Namespaces(ctx)
	if err != nil {
		return NamespacesResult{}, err
	}
	if list == nil {
		list = []string{}
	}
	return NamespacesResult{Namespaces: list, Configured: h.Backend.Info().Namespace}, nil
}

// index returns the loaded index for ns (or all namespaces), loading or
// building it when missing, expired or rebuild is set.
func (h *Handler) index(ctx context.Context, ns string, all, rebuild bool, event func(any)) (*loadedIndex, error) {
	key := ns
	if all {
		key = allKey
	}
	h.mu.Lock()
	li := h.loaded[key]
	h.mu.Unlock()
	if li != nil && !rebuild && now().Before(li.h.Expires) {
		return li, nil
	}
	var last time.Time
	progress := func(p index.Progress) {
		if t := now(); t.Sub(last) >= 250*time.Millisecond {
			last = t
			event(ProgressEvent{Type: "progress", Secrets: p.Secrets, Lists: p.Lists})
		}
	}
	var entries []index.Entry
	var hdr cache.Header
	var warn string
	var err error
	if all {
		entries, hdr, warn, err = h.Backend.AllNamespaces(ctx, rebuild, progress)
	} else {
		entries, hdr, warn, err = h.Backend.Index(ctx, ns, rebuild, progress)
	}
	if err != nil {
		return nil, err
	}
	if !all {
		for i := range entries {
			entries[i].Namespace = "" // one namespace: match on the path alone
		}
	}
	li = &loadedIndex{ix: search.New(entries), h: hdr, warn: warn}
	h.mu.Lock()
	if h.loaded == nil {
		h.loaded = map[string]*loadedIndex{}
	}
	h.loaded[key] = li
	h.mu.Unlock()
	return li, nil
}

func (h *Handler) search(ctx context.Context, req Request, event func(any)) (SearchResult, error) {
	ns := h.namespace(req)
	li, err := h.index(ctx, ns, req.AllNamespaces, false, event)
	if err != nil {
		return SearchResult{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	rows := li.ix.Search(req.Query, 0)
	recent := 0
	if strings.TrimSpace(req.Query) == "" && !req.AllNamespaces {
		rows, recent = withRecent(rows, h.Backend.Recent(ns))
	}
	res := SearchResult{
		Total:   len(rows),
		Warning: li.warn,
		Built:   unix(li.h.Created),
		Expires: unix(li.h.Expires),
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	res.Items = make([]Item, len(rows))
	for i, r := range rows {
		item := Item{Namespace: ns, Path: r.Entry.Path, Key: r.Key}
		if req.AllNamespaces {
			item.Namespace = r.Entry.Namespace
		}
		res.Items[i] = item
	}
	res.Recent = min(recent, len(res.Items))
	// Paths can be long: drop results until the response fits.
	for {
		res.Truncated = len(res.Items) < res.Total
		b, err := Encode(Response{ID: req.ID, Result: res})
		if (err == nil && len(b) <= MaxOutgoing) || len(res.Items) == 0 {
			break
		}
		res.Items = res.Items[:len(res.Items)/2]
		res.Recent = min(res.Recent, len(res.Items))
	}
	return res, nil
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// withRecent moves the rows named in recent ("path#key", or "path" for a
// keyless row) to the front, newest first, and says how many there are.
func withRecent(rows []search.Row, recent []string) ([]search.Row, int) {
	if len(recent) == 0 {
		return rows, 0
	}
	at := make(map[string]int, len(rows))
	for i, r := range rows {
		item := r.Entry.Path
		if r.Key != "" {
			item += "#" + r.Key
		}
		if _, dup := at[item]; !dup {
			at[item] = i
		}
	}
	used := map[int]bool{}
	out := make([]search.Row, 0, len(rows))
	for _, item := range recent {
		if i, ok := at[item]; ok && !used[i] {
			used[i] = true
			out = append(out, rows[i])
		}
	}
	n := len(out)
	for i, r := range rows {
		if !used[i] {
			out = append(out, r)
		}
	}
	return out, n
}

func (h *Handler) refresh(ctx context.Context, req Request, event func(any)) (RefreshResult, error) {
	li, err := h.index(ctx, h.namespace(req), req.AllNamespaces, true, event)
	if err != nil {
		return RefreshResult{}, err
	}
	secrets := map[string]bool{}
	for _, r := range li.ix.Rows {
		secrets[r.Entry.Namespace+"\x00"+r.Entry.Path] = true
	}
	return RefreshResult{Secrets: len(secrets), Warning: li.warn}, nil
}

func (h *Handler) get(ctx context.Context, req Request) (GetResult, error) {
	path := strings.Trim(req.Path, "/")
	if path == "" || req.Key == "" {
		return GetResult{}, &Error{Code: CodeBadRequest, Message: "get needs a path and a key"}
	}
	ns := h.namespace(req)
	v, err := h.Backend.Value(ctx, ns, path, req.Key)
	if err != nil {
		return GetResult{}, err
	}
	if len(v) > MaxOutgoing-valueRoom {
		return GetResult{}, &Error{Code: CodeTooLarge, Message: fmt.Sprintf(
			"%s#%s is %d bytes, over the browser's 1 MB message limit", path, req.Key, len(v))}
	}
	h.Backend.AddRecent(ns, path+"#"+req.Key)
	return GetResult{Value: v}, nil
}

func (h *Handler) login(ctx context.Context, req Request, event func(any)) (LoginResult, error) {
	method := strings.ToLower(strings.TrimSpace(req.Method))
	if method == "" {
		method = h.Backend.Info().AuthMethod
	}
	switch method {
	case "", "oidc":
		method = "oidc"
	case "token":
		if strings.TrimSpace(req.Token) == "" {
			return LoginResult{}, &Error{Code: CodeBadRequest, Message: "token login needs a token"}
		}
	case "ldap", "userpass":
		// Option B of the design: directory passwords never go through
		// the browser.
		return LoginResult{}, &Error{Code: CodeUseTerminal, Message: fmt.Sprintf(
			"%s logins ask for a password, which the extension doesn't handle: run `vaultr login` in a terminal, then try again", method)}
	default:
		return LoginResult{}, &Error{Code: CodeBadRequest, Message: fmt.Sprintf("unknown login method %q", method)}
	}
	summary, warn, err := h.Backend.Login(ctx, method, strings.TrimSpace(req.Token), func(u string) {
		event(LoginURLEvent{Type: "login_url", URL: u})
	})
	if err != nil {
		return LoginResult{}, err
	}
	h.forget()
	return LoginResult{Summary: summary, Warning: warn}, nil
}

// errorOf turns an error into a response error. Messages come from
// vaultr's own errors and never include request fields such as a token.
func errorOf(ctx context.Context, err error) *Error {
	var e *Error
	switch {
	case errors.As(err, &e):
		return e
	case ctx.Err() != nil && errors.Is(err, context.Canceled):
		return &Error{Code: CodeCanceled, Message: "canceled"}
	case errors.Is(err, ErrNotLoggedIn), errors.Is(err, vault.ErrTokenInvalid):
		return &Error{Code: CodeNotLoggedIn, Message: err.Error()}
	case errors.Is(err, ErrUseTerminal):
		return &Error{Code: CodeUseTerminal, Message: err.Error()}
	case errors.Is(err, vault.ErrForbidden):
		return &Error{Code: CodeForbidden, Message: err.Error()}
	case errors.Is(err, vault.ErrNotFound):
		return &Error{Code: CodeNotFound, Message: err.Error()}
	case errors.Is(err, ErrTooLarge):
		return &Error{Code: CodeTooLarge, Message: err.Error()}
	}
	return &Error{Code: CodeOther, Message: err.Error()}
}
