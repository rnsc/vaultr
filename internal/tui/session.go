package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/search"
	"github.com/rnsc/vaultr/internal/vault"
)

// Keeping a session going across token expiry: the status line counts
// down to the token's expiry; an action a dead token interrupted runs
// again after the next login; and the index in memory is saved again
// under the new token when the same person logged in, instead of being
// crawled again.

type pendingKind int

const (
	pendingOpen  pendingKind = iota // open the secret view
	pendingCopy                     // copy the row's value
	pendingBuild                    // rebuild the index (^r, enter on no match)
)

type pendingAction struct {
	kind pendingKind
	row  search.Row
}

type (
	tokenMsg struct {
		info vault.TokenInfo
		ok   bool
	}
	minuteMsg struct{}
	adoptMsg  struct {
		header  cache.Header
		ok      bool
		warning string
		err     error
		resumed bool // an interrupted action was finished after the login
	}
)

// loginToResume opens the login screen and remembers what to do after.
func (m *model) loginToResume(p pendingAction) tea.Cmd {
	m.pending = &p
	return m.openLogin("Your token expired or was revoked. Log in to continue where you were.")
}

// afterLogin runs once a login succeeded: finish the interrupted action,
// then keep or rebuild the index.
func (m *model) afterLogin() tea.Cmd {
	m.autoTried = false
	cmds := []tea.Cmd{m.lookupToken()}
	if p := m.pending; p != nil {
		m.pending = nil
		switch p.kind {
		case pendingOpen:
			m.mode = modeDetail
			m.detail = detailState{row: p.row, loading: true}
			cmds = append(cmds, m.fetch(p.row.Entry), m.flashWithNotice("", false))
		case pendingCopy:
			m.notice = "" // the copy's own message says what happened
			cmds = append(cmds, m.fetchAndCopy(p.row))
		case pendingBuild:
			return tea.Batch(append(cmds, m.startBuild())...)
		}
		switch {
		case m.allNS:
			// Each namespace's cache belonged to the old token; the status
			// line offers ^r to rebuild them.
			m.header = cache.Header{}
		case m.ix != nil:
			cmds = append(cmds, m.adopt(m.entries, m.header, true))
		}
		return tea.Batch(cmds...)
	}
	if m.ix != nil && !m.allNS {
		return tea.Batch(append(cmds, m.adopt(m.entries, m.header, false))...)
	}
	return tea.Batch(append(cmds, m.reindex())...)
}

func (m *model) adopt(entries []index.Entry, prev cache.Header, resumed bool) tea.Cmd {
	b := m.backend()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		h, ok, warn, err := b.Adopt(ctx, entries, prev)
		return adoptMsg{h, ok, warn, err, resumed}
	}
}

func (m model) adopted(msg adoptMsg) (tea.Model, tea.Cmd) {
	if msg.ok {
		m.header = msg.header
		if msg.warning != "" {
			return m, m.flashWithNotice("kept the index; "+msg.warning, true)
		}
		if msg.resumed {
			return m, nil // the finished action's message stays up
		}
		return m, m.flashWithNotice("", false)
	}
	// Someone else logged in (or the token has no identity): the index in
	// memory is not saved for them. Rebuild it; but not right after
	// finishing an interrupted action, which the user is looking at: the
	// status line offers ^r instead.
	m.header = cache.Header{}
	if msg.resumed {
		return m, nil
	}
	return m, m.reindex()
}

// lookupToken asks Vault about the token, for the expiry countdown.
func (m *model) lookupToken() tea.Cmd {
	c := m.backend().Client()
	if c.Token() == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := c.LookupSelf(ctx); err != nil {
			return tokenMsg{}
		}
		info, ok := c.LastTokenInfo()
		return tokenMsg{info, ok}
	}
}

func (m model) tokenLooked(msg tokenMsg) (tea.Model, tea.Cmd) {
	m.token, m.tokenOK = msg.info, msg.ok
	if msg.ok && !msg.info.ExpireTime.IsZero() && !m.tickLive {
		m.tickLive = true
		return m, everyMinute()
	}
	return m, nil
}

func everyMinute() tea.Cmd {
	return tea.Tick(time.Minute, func(time.Time) tea.Msg { return minuteMsg{} })
}

// tokenStatus describes the token's remaining life for the status line;
// warn means it deserves attention.
func (m model) tokenStatus() (text string, warn bool) {
	if !m.tokenOK || m.token.ExpireTime.IsZero() {
		return "", false
	}
	left := time.Until(m.token.ExpireTime)
	switch {
	case left <= 0:
		return "token expired · ^l to log in", true
	case left < 10*time.Minute:
		text = "token expires in " + shortDuration(left)
		if !m.token.Renewable {
			text += " · ^l to log in again"
		}
		return text, true
	}
	return "token " + shortDuration(left), false
}

// shortDuration renders d in minutes: "1h5m", "12m", "<1m".
func shortDuration(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	s := d.Truncate(time.Minute).String() // "1h5m0s"
	s = strings.TrimSuffix(s, "0s")
	return strings.Replace(s, "h0m", "h", 1)
}
