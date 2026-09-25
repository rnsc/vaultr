package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/search"
)

func TestHelpWrapsInsteadOfLosingItems(t *testing.T) {
	m := newTest(t, Options{Entries: testEntries})
	for _, width := range []int{200, 60, 30} {
		m = update(t, m, tea.WindowSizeMsg{Width: width, Height: 20})
		view := plain(m.View())
		for _, item := range strings.Split(listHelp, " · ") {
			if !strings.Contains(view, item) {
				t.Errorf("width %d: help item %q missing:\n%s", width, item, view)
			}
		}
		lines := strings.Split(view, "\n")
		if len(lines) != 20 {
			t.Errorf("width %d: %d lines, want the window's 20", width, len(lines))
		}
		for _, l := range lines {
			if lipgloss.Width(l) > width {
				t.Errorf("width %d: line wider than the window: %q", width, l)
			}
		}
	}
	// The list gives up the lines the help needs.
	m = update(t, m, tea.WindowSizeMsg{Width: 30, Height: 20})
	if n := helpLines(listHelp, 30); n < 3 || m.listHeight() != 20-2-n {
		t.Errorf("help lines %d, list height %d", n, m.listHeight())
	}
}

func TestLongRowsKeepTheKey(t *testing.T) {
	m := newTest(t, Options{Entries: testEntries})
	m = typeText(t, m, "k:webhook_secret")
	m = update(t, m, tea.WindowSizeMsg{Width: 44, Height: 12})
	view := plain(m.View())
	if !strings.Contains(view, "secret/…") || !strings.Contains(view, "stripe  ⟶ webhook_secret") {
		t.Errorf("long row:\n%s", view)
	}
}

func TestTail(t *testing.T) {
	for _, c := range []struct {
		s    string
		w    int
		want string
	}{
		{"prod/payments/stripe", 10, "…ts/stripe"},
		{"naïve/clé", 5, "…/clé"},
		{"abc", 1, "…"},
	} {
		if got := tail(c.s, c.w); got != c.want {
			t.Errorf("tail(%q, %d) = %q, want %q", c.s, c.w, got, c.want)
		}
	}
}

func TestVeryNarrowRowsKeepTheKey(t *testing.T) {
	e := testEntries[0] // secret/prod/payments/stripe
	e.Mount, e.Path = "a-very-long-mount-name/", "a-very-long-mount-name/prod/payments/stripe"
	row := renderRow(searchRow(&e, "webhook_secret"), nil, 38, false)
	if got := plain(row); !strings.HasSuffix(got, "stripe  ⟶ webhook_secret") || lipgloss.Width(got) > 38 {
		t.Errorf("%q (%d wide)", got, lipgloss.Width(got))
	}
}

func searchRow(e *index.Entry, key string) search.Row { return search.Row{Entry: e, Key: key} }
