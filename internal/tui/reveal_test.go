package tui

import (
	"strings"
	"testing"
	"time"
)

func TestRevealedValuesHideAgain(t *testing.T) {
	fb := newFakeBackend(t)
	fb.settings.RevealTimeout = 30 * time.Second
	m := newTest(t, Options{Backend: fb, Entries: testEntries})
	m = typeText(t, m, "prod db")
	m = press(t, m, "enter", "r")
	if !m.detail.reveal || !strings.Contains(m.View(), "hunter2") {
		t.Fatal("not revealed")
	}
	first := m.detail.revealSeq

	// Hide and reveal again: the first timer must not hide the second reveal.
	m = press(t, m, "r", "r")
	m = update(t, m, hideMsg{first})
	if !m.detail.reveal {
		t.Error("an old timer hid a newer reveal")
	}
	m = update(t, m, hideMsg{m.detail.revealSeq})
	if m.detail.reveal || strings.Contains(m.View(), "hunter2") {
		t.Error("still revealed after the timeout")
	}

	// reveal_timeout = 0: no timer.
	fb.settings.RevealTimeout = 0
	seq := m.detail.revealSeq
	m = press(t, m, "r")
	if !m.detail.reveal || m.detail.revealSeq != seq {
		t.Errorf("with 0, reveal %v seq %d -> %d", m.detail.reveal, seq, m.detail.revealSeq)
	}

	// Leaving the secret view drops its values.
	m = press(t, m, "esc")
	if m.detail.values != nil || m.detail.keys != nil {
		t.Error("values kept after leaving the secret view")
	}
}
