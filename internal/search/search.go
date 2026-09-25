// Package search matches queries against indexed paths and key names.
package search

import (
	"sort"
	"strings"

	"github.com/rnsc/vaultr/internal/index"
)

// Row is one searchable item: a secret path, optionally narrowed to a key.
type Row struct {
	Entry *index.Entry
	Key   string // empty for a secret without keys (or a paths-only index)

	path, key string // lowercased
}

// Index holds precomputed rows.
type Index struct {
	Rows []Row
}

// New flattens entries into one row per (path, key).
func New(entries []index.Entry) *Index {
	ix := &Index{}
	for i := range entries {
		e := &entries[i]
		lp := strings.ToLower(e.Path)
		if e.Namespace != "" {
			// Searching several namespaces: the namespace counts as part of
			// the path, so "team-a db" narrows to team-a.
			lp = strings.ToLower(e.Namespace) + "/" + lp
		}
		if len(e.Keys) == 0 {
			ix.Rows = append(ix.Rows, Row{Entry: e, path: lp})
			continue
		}
		for _, k := range e.Keys {
			ix.Rows = append(ix.Rows, Row{Entry: e, Key: k, path: lp, key: strings.ToLower(k)})
		}
	}
	return ix
}

type term struct {
	s     string
	field byte // 0 any, 'p' path only, 'k' key only
}

func parse(q string) []term {
	var ts []term
	for _, f := range strings.Fields(strings.ToLower(q)) {
		t := term{s: f}
		switch {
		case strings.HasPrefix(f, "k:") || strings.HasPrefix(f, "key:"):
			t.field, t.s = 'k', f[strings.IndexByte(f, ':')+1:]
		case strings.HasPrefix(f, "p:") || strings.HasPrefix(f, "path:"):
			t.field, t.s = 'p', f[strings.IndexByte(f, ':')+1:]
		}
		if t.s != "" {
			ts = append(ts, t)
		}
	}
	return ts
}

// Search returns rows where every term matches the path or key name, best
// matches first. Terms are case-insensitive substrings; prefix a term with
// "k:" or "p:" to restrict it to key names or paths. limit <= 0 means all.
func (ix *Index) Search(q string, limit int) []Row {
	terms := parse(q)
	if len(terms) == 0 {
		if limit > 0 && limit < len(ix.Rows) {
			return ix.Rows[:limit]
		}
		return ix.Rows
	}
	type scored struct {
		i     int
		score int
	}
	var hits []scored
	for i := range ix.Rows {
		r := &ix.Rows[i]
		score, ok := 0, true
		for _, t := range terms {
			s := match(r, t)
			if s < 0 {
				ok = false
				break
			}
			score += s
		}
		if ok {
			hits = append(hits, scored{i, score})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].score != hits[b].score {
			return hits[a].score > hits[b].score
		}
		return len(ix.Rows[hits[a].i].path) < len(ix.Rows[hits[b].i].path)
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Row, len(hits))
	for i, h := range hits {
		out[i] = ix.Rows[h.i]
	}
	return out
}

// match returns a score, or -1 if the term does not match.
func match(r *Row, t term) int {
	best := -1
	if t.field != 'p' {
		switch {
		case r.key == "":
		case r.key == t.s:
			best = 100
		case strings.HasPrefix(r.key, t.s):
			best = 60
		case strings.Contains(r.key, t.s):
			best = 40
		}
	}
	if t.field != 'k' {
		if i := strings.LastIndex(r.path, t.s); i >= 0 {
			s := 10
			last := r.path[strings.LastIndexByte(r.path, '/')+1:]
			switch {
			case last == t.s:
				s = 50
			case i >= len(r.path)-len(last):
				s = 30 // in the final segment
			case i == 0 || r.path[i-1] == '/':
				s = 20 // segment prefix
			}
			if s > best {
				best = s
			}
		}
	}
	return best
}
