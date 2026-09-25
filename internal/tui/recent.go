package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/rnsc/vaultr/internal/search"
)

// Recent paths: the rows opened or copied last are listed first when the
// search is empty. The backend keeps them inside the encrypted index, so
// they expire with it and never hold values.

// recentItem names a row for the recent list: its path, and key if any.
func recentItem(r search.Row) string {
	if r.Key == "" {
		return r.Entry.Path
	}
	return r.Entry.Path + "#" + r.Key
}

// withRecent moves the rows named in recent to the top of rows, in that
// order, and says how many there are. Items no longer in the index are
// skipped. A key name can contain "#", so rows are matched on the whole
// item rather than split.
func withRecent(rows []search.Row, recent []string) ([]search.Row, int) {
	if len(recent) == 0 {
		return rows, 0
	}
	at := make(map[string]int, len(rows))
	for i, r := range rows {
		if _, dup := at[recentItem(r)]; !dup {
			at[recentItem(r)] = i
		}
		if _, ok := at[r.Entry.Path]; !ok {
			at[r.Entry.Path] = i // a secret opened from a keyless row
		}
	}
	used := make(map[int]bool, len(recent))
	top := make([]search.Row, 0, len(recent))
	for _, item := range recent {
		if i, ok := at[item]; ok && !used[i] {
			used[i] = true
			top = append(top, rows[i])
		}
	}
	out := append(top, make([]search.Row, 0, len(rows)-len(top))...)
	for i, r := range rows {
		if !used[i] {
			out = append(out, r)
		}
	}
	return out, len(top)
}

// addRecent records row as opened, in the background.
func (m *model) addRecent(r search.Row) tea.Cmd {
	if m.allNS {
		return nil // each namespace has its own index
	}
	b, item := m.backend(), recentItem(r)
	return func() tea.Msg {
		b.AddRecent(item)
		return nil
	}
}
