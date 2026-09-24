package complete

import (
	"context"
	"sort"
	"strings"

	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/vault"
)

// IndexSource answers from the cached index (no network).
type IndexSource struct {
	Entries []index.Entry
}

// Children implements Source.
func (s IndexSource) Children(_ context.Context, dir string) ([]string, bool) {
	if s.Entries == nil {
		return nil, false
	}
	seen := map[string]bool{}
	for _, e := range s.Entries {
		if !strings.HasPrefix(e.Path, dir) {
			continue
		}
		rest := e.Path[len(dir):]
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			rest = rest[:i+1] // a folder
		}
		if rest != "" {
			seen[rest] = true
		}
	}
	return keysOf(seen), true
}

// Keys implements Source.
func (s IndexSource) Keys(_ context.Context, path string) ([]string, bool) {
	for _, e := range s.Entries {
		if e.Path == path {
			return e.Keys, true
		}
	}
	return nil, false
}

// LiveSource lists Vault directly, one folder per call. It is the fallback
// when there is no cache, or the cache predates the secret.
type LiveSource struct {
	Client *vault.Client
	Mounts []string // configured mounts ("secret/"); empty = discover

	mounts []vault.Mount
}

func (s *LiveSource) kvMounts(ctx context.Context) []vault.Mount {
	if s.mounts != nil {
		return s.mounts
	}
	if len(s.Mounts) > 0 {
		for _, p := range s.Mounts {
			if m, err := s.Client.MountFor(ctx, p); err == nil {
				s.mounts = append(s.mounts, m)
			}
		}
	} else if ms, err := s.Client.KVMounts(ctx); err == nil {
		s.mounts = ms
	}
	if s.mounts == nil {
		s.mounts = []vault.Mount{}
	}
	return s.mounts
}

// mountOf finds the KV mount a logical path belongs to.
func (s *LiveSource) mountOf(ctx context.Context, path string) (vault.Mount, bool) {
	var best vault.Mount
	for _, m := range s.kvMounts(ctx) {
		if strings.HasPrefix(path, m.Path) && len(m.Path) > len(best.Path) {
			best = m
		}
	}
	if best.Path != "" {
		return best, true
	}
	m, err := s.Client.MountFor(ctx, path)
	return m, err == nil
}

// Children implements Source.
func (s *LiveSource) Children(ctx context.Context, dir string) ([]string, bool) {
	if dir == "" {
		var out []string
		for _, m := range s.kvMounts(ctx) {
			out = append(out, m.Path)
		}
		sort.Strings(out)
		return out, len(out) > 0
	}
	m, ok := s.mountOf(ctx, dir)
	if !ok || !strings.HasPrefix(dir, m.Path) {
		return nil, false
	}
	keys, err := s.Client.ListSecrets(ctx, m, strings.TrimPrefix(dir, m.Path))
	return keys, err == nil
}

// Keys implements Source.
func (s *LiveSource) Keys(ctx context.Context, path string) ([]string, bool) {
	m, ok := s.mountOf(ctx, path)
	if !ok {
		return nil, false
	}
	data, err := s.Client.ReadSecret(ctx, m, strings.TrimPrefix(path, m.Path))
	if err != nil {
		return nil, false
	}
	seen := map[string]bool{}
	for k := range data {
		seen[k] = true
	}
	return keysOf(seen), true
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
