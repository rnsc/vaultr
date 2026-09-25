package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/mattn/go-isatty"

	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/vault"
)

// Searching every namespace the token can use: each namespace keeps its
// own cache (keyed by namespace, as always); these are loaded, the missing
// or stale ones built a few at a time, and the entries tagged with their
// namespace and merged.

// nsParallel is how many namespaces are built at once; the workers are
// shared between them, so the load on the server stays the same.
const nsParallel = 4

// inNamespace returns a copy of the app working in namespace ns.
func (a *app) inNamespace(ns string, workers int) *app {
	sub := *a
	sub.client = a.client.InNamespace(ns)
	sub.store = &cache.Store{Dir: a.settings.CacheDir, Client: sub.client}
	sub.workers = workers
	return &sub
}

// AllNamespaces loads or builds the index of every namespace the token can
// use (rebuild forces a crawl) and returns the entries, each tagged with
// its namespace, a header whose expiry is the earliest of them, and a
// warning listing namespaces that failed. It implements tui.Backend.
func (a *app) AllNamespaces(ctx context.Context, rebuild bool, onProgress func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
	if _, err := a.client.LookupSelf(ctx); err != nil {
		if errors.Is(err, vault.ErrTokenInvalid) {
			err = fmt.Errorf("%w; run `vaultr login`", vault.ErrTokenInvalid)
		}
		return nil, cache.Header{}, "", err
	}
	list, err := a.client.ListNamespaces(ctx)
	if err != nil {
		return nil, cache.Header{}, "", err
	}
	par := min(nsParallel, len(list))
	workers := max(4, a.workers/max(par, 1))

	type result struct {
		entries []index.Entry
		header  cache.Header
		note    string
		err     error
	}
	results := make([]result, len(list))
	var mu sync.Mutex
	progress := map[string]index.Progress{}
	report := func(ns string, p index.Progress) {
		if onProgress == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		progress[ns] = p
		var sum index.Progress
		for _, q := range progress {
			sum.Secrets, sum.Lists, sum.Denied = sum.Secrets+q.Secrets, sum.Lists+q.Lists, sum.Denied+q.Denied
		}
		onProgress(sum) // under mu: never concurrently
	}
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	for i, ns := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sub := a.inNamespace(ns, workers)
			r := &results[i]
			if !rebuild {
				if r.entries, r.header, r.err = sub.store.Load(ctx); r.err == nil {
					return
				}
				if !errors.Is(r.err, cache.ErrStale) {
					return
				}
			}
			r.entries, r.header, r.note, r.err = sub.buildIndex(ctx, func(p index.Progress) { report(ns, p) })
			if errors.Is(r.err, errNoMounts) {
				r.err = nil // nothing to search there
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, cache.Header{}, "", err
	}

	var all []index.Entry
	var h cache.Header
	var failed []string
	for i, r := range results {
		ns := list[i]
		if r.err != nil {
			if errors.Is(r.err, vault.ErrTokenInvalid) {
				return nil, cache.Header{}, "", r.err
			}
			failed = append(failed, nsLabel(ns)+": "+r.err.Error())
			continue
		}
		if r.note != "" {
			failed = append(failed, nsLabel(ns)+": "+r.note)
		}
		for _, e := range r.entries {
			e.Namespace = ns
			all = append(all, e)
		}
		h = earliest(h, r.header)
	}
	if len(failed) == len(list) && len(list) > 0 {
		return nil, cache.Header{}, "", fmt.Errorf("every namespace failed; first: %s", failed[0])
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Namespace != all[j].Namespace {
			return all[i].Namespace < all[j].Namespace
		}
		return all[i].Path < all[j].Path
	})
	var warn string
	if len(failed) > 0 {
		warn = fmt.Sprintf("%d of %d namespaces had problems, first: %s", len(failed), len(list), failed[0])
	}
	return all, h, warn, nil
}

// earliest combines cache headers: the earliest creation and expiry, and
// bound only if all are.
func earliest(a, b cache.Header) cache.Header {
	if b.Expires.IsZero() {
		return a // an empty namespace: no cache
	}
	if a.Expires.IsZero() {
		return b
	}
	if b.Expires.Before(a.Expires) {
		a.Expires = b.Expires
	}
	if b.Created.Before(a.Created) {
		a.Created = b.Created
	}
	if !b.Bound() {
		a.KeyRef = ""
	}
	return a
}

// nsLabel names a namespace for people: "/" is the root.
func nsLabel(ns string) string {
	if ns == "" {
		return "/"
	}
	return ns
}

// allNamespacesCLI is AllNamespaces with progress and notes on stderr.
func (a *app) allNamespacesCLI(ctx context.Context, rebuild bool) ([]index.Entry, error) {
	tty := isatty.IsTerminal(os.Stderr.Fd())
	entries, _, warn, err := a.AllNamespaces(ctx, rebuild, func(p index.Progress) {
		if tty {
			fmt.Fprintf(os.Stderr, "\r\033[Kindexing namespaces: %d secrets, %d folders", p.Secrets, p.Lists)
		}
	})
	if tty {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
	if err != nil {
		return nil, err
	}
	if warn != "" {
		fmt.Fprintln(os.Stderr, "warning:", strings.TrimSpace(warn))
	}
	return entries, nil
}
