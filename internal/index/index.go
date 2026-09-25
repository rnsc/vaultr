// Package index crawls KV mounts and records secret paths and key names
// (never values).
package index

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rnsc/vaultr/internal/vault"
)

// Entry is one secret: its full logical path and the names of its keys.
type Entry struct {
	Path  string   `json:"p"` // e.g. "secret/team/db", as used by `vault kv get`
	Mount string   `json:"m"` // e.g. "secret/"
	KV    int      `json:"v"`
	Keys  []string `json:"k,omitempty"`
	// Namespace is set when searching several namespaces at once ("" is
	// the root). Each namespace has its own cache, so it isn't stored.
	Namespace string `json:"-"`
}

// Rel returns the path relative to its mount.
func (e Entry) Rel() string { return strings.TrimPrefix(e.Path, e.Mount) }

// Progress is reported while crawling.
type Progress struct {
	Secrets int64
	Lists   int64
	Denied  int64
}

// Options control a crawl.
type Options struct {
	Mounts     []vault.Mount
	Workers    int
	PathsOnly  bool           // skip reading secrets, index paths only
	OnProgress func(Progress) // never called concurrently
}

// Result of a crawl.
type Result struct {
	Entries []Entry
	Denied  int64
	Errors  []error
}

// Build walks every mount concurrently.
func Build(ctx context.Context, c *vault.Client, opt Options) (*Result, error) {
	if opt.Workers <= 0 {
		opt.Workers = 32
	}
	b := &builder{c: c, opt: opt, sem: make(chan struct{}, opt.Workers)}
	for _, m := range opt.Mounts {
		b.wg.Add(1)
		go b.list(ctx, m, "")
	}
	b.wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(b.entries, func(i, j int) bool { return b.entries[i].Path < b.entries[j].Path })
	b.report()
	return &Result{Entries: b.entries, Denied: b.prog.denied.Load(), Errors: b.errs}, nil
}

type builder struct {
	c   *vault.Client
	opt Options
	sem chan struct{}
	wg  sync.WaitGroup

	mu      sync.Mutex
	entries []Entry
	errs    []error

	prog     struct{ secrets, lists, denied atomic.Int64 }
	reportMu sync.Mutex
}

// report calls OnProgress, never concurrently and with monotonic counts.
func (b *builder) report() {
	if b.opt.OnProgress != nil {
		b.reportMu.Lock()
		defer b.reportMu.Unlock()
		b.opt.OnProgress(Progress{
			Secrets: b.prog.secrets.Load(),
			Lists:   b.prog.lists.Load(),
			Denied:  b.prog.denied.Load(),
		})
	}
}

func (b *builder) fail(err error) {
	if errors.Is(err, vault.ErrForbidden) || errors.Is(err, vault.ErrNotFound) {
		if errors.Is(err, vault.ErrForbidden) {
			b.prog.denied.Add(1)
		}
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	b.mu.Lock()
	if len(b.errs) < 50 {
		b.errs = append(b.errs, err)
	}
	b.mu.Unlock()
}

func (b *builder) acquire(ctx context.Context) bool {
	select {
	case b.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (b *builder) release() { <-b.sem }

func (b *builder) list(ctx context.Context, m vault.Mount, rel string) {
	defer b.wg.Done()
	if !b.acquire(ctx) {
		return
	}
	keys, err := b.c.ListSecrets(ctx, m, rel)
	b.release()
	b.prog.lists.Add(1)
	if err != nil {
		b.fail(err)
		return
	}
	for _, k := range keys {
		child := rel + k
		if strings.HasSuffix(k, "/") {
			b.wg.Add(1)
			go b.list(ctx, m, child)
			continue
		}
		b.wg.Add(1)
		go b.read(ctx, m, child)
	}
	b.report()
}

func (b *builder) read(ctx context.Context, m vault.Mount, rel string) {
	defer b.wg.Done()
	e := Entry{Path: m.Path + rel, Mount: m.Path, KV: m.KVVersion}
	if !b.opt.PathsOnly {
		if !b.acquire(ctx) {
			return
		}
		data, err := b.c.ReadSecret(ctx, m, rel)
		b.release()
		if err != nil {
			b.fail(err)
		}
		for k := range data {
			e.Keys = append(e.Keys, k)
		}
		sort.Strings(e.Keys)
		// Values are dropped here and never leave this function.
	}
	b.mu.Lock()
	b.entries = append(b.entries, e)
	b.mu.Unlock()
	if n := b.prog.secrets.Add(1); n%25 == 0 {
		b.report()
	}
}
