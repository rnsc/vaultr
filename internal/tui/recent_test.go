package tui

import (
	"reflect"
	"strings"
	"testing"
)

func TestRecentRowsComeFirst(t *testing.T) {
	useFakeClipboard(t)
	fb := newFakeBackend(t)
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	all := rowIDs(m)

	// Open one row and copy another: both are recorded, newest first.
	m = typeText(t, m, "k:username")
	m = press(t, m, "enter", "esc")
	m = press(t, m, "esc") // clear the search
	m = typeText(t, m, "k:api_key")
	m = press(t, m, "ctrl+y")
	if want := []string{"secret/prod/payments/stripe#api_key", "secret/prod/db#username"}; !reflect.DeepEqual(fb.recent, want) {
		t.Fatalf("recorded %q", fb.recent)
	}

	m = press(t, m, "esc") // empty search: recent rows on top
	got := rowIDs(m)
	if got[0] != "secret/prod/payments/stripe#api_key" || got[1] != "secret/prod/db#username" || m.recentN != 2 {
		t.Errorf("rows %q (recentN %d)", got[:3], m.recentN)
	}
	if len(got) != len(all) {
		t.Errorf("%d rows, want %d (moved, not added)", len(got), len(all))
	}
	lines := strings.Split(plain(m.View()), "\n")
	if !strings.Contains(lines[1], "recent") || !strings.Contains(lines[2], "recent") || strings.Contains(lines[3], "recent") {
		t.Errorf("marks:\n%s", strings.Join(lines[:5], "\n"))
	}

	// Typing a search shows the normal ranking.
	m = typeText(t, m, "prod")
	if m.recentN != 0 {
		t.Error("recent rows while searching")
	}
}

func TestRecentSkipsWhatIsGone(t *testing.T) {
	fb := newFakeBackend(t)
	fb.recent = []string{"secret/removed#k", "secret/keyless", "secret/prod/db"}
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	got := rowIDs(m)
	// A gone item is skipped; a path alone matches its first row.
	if m.recentN != 2 || got[0] != "secret/keyless#" || !strings.HasPrefix(got[1], "secret/prod/db#") {
		t.Errorf("rows %q recentN %d", got[:3], m.recentN)
	}
}
