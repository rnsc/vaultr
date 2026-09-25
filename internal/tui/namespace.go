package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/vault"
)

// The namespace switcher lists the namespaces the token can use, filters
// them as you type, and switches to the chosen one: its index is loaded
// from the cache, or built.

type nsState struct {
	input   textinput.Model
	all     []string // full paths, "" = root
	results []string
	cursor  int
	offset  int
	loading bool
	err     error
}

type namespacesMsg struct {
	list []string
	err  error
}

func (m *model) openNamespaces() tea.Cmd {
	if m.backend().Client().Token() == "" {
		return m.openLogin("No Vault token found.")
	}
	in := textinput.New()
	in.Prompt = "❯ "
	in.PromptStyle = sPointer
	in.Placeholder = "filter namespaces"
	in.Cursor.SetMode(inputCursorMode)
	in.Focus()
	m.ns = nsState{input: in, loading: true}
	m.mode = modeNamespace
	b := m.backend()
	return tea.Batch(textinput.Blink, m.spin.Tick, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		list, err := b.Namespaces(ctx)
		return namespacesMsg{list, err}
	})
}

func (m model) namespacesLoaded(msg namespacesMsg) (tea.Model, tea.Cmd) {
	if m.mode != modeNamespace {
		return m, nil
	}
	if errors.Is(msg.err, vault.ErrTokenInvalid) {
		return m, m.openLogin("Your token is missing, expired or revoked.")
	}
	m.ns.loading, m.ns.err, m.ns.all = false, msg.err, msg.list
	if len(msg.list) > 1 {
		m.ns.all = append([]string{allEntry}, msg.list...)
	}
	m.filterNamespaces()
	// Start on the current namespace.
	cur := m.backend().Client().Namespace
	if m.allNS {
		cur = allEntry
	}
	for i, ns := range m.ns.results {
		if ns == cur {
			m.ns.cursor = i
			m.moveNamespace(0)
		}
	}
	return m, nil
}

func (m *model) filterNamespaces() {
	terms := strings.Fields(strings.ToLower(m.ns.input.Value()))
	var out []string
	for _, ns := range m.ns.all {
		name := strings.ToLower(nsLabel(ns))
		ok := true
		for _, t := range terms {
			if !strings.Contains(name, t) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, ns)
		}
	}
	m.ns.results = out
	m.ns.cursor, m.ns.offset = 0, 0
}

func (m *model) moveNamespace(n int) {
	s := &m.ns
	s.cursor = max(0, min(s.cursor+n, len(s.results)-1))
	h := m.nsListHeight()
	if s.cursor < s.offset {
		s.offset = s.cursor
	}
	if s.cursor >= s.offset+h {
		s.offset = s.cursor - h + 1
	}
}

func (m model) nsListHeight() int { return max(1, m.height-5) } // title, blank, input, status, keys

func (m model) updateNamespace(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc":
		if m.ns.input.Value() != "" {
			m.ns.input.SetValue("")
			m.filterNamespaces()
			return m, nil
		}
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		m.mode = modeList
		return m, nil
	case "ctrl+n": // the key that opened it closes it
		m.mode = modeList
		return m, nil
	case "up", "ctrl+k":
		m.moveNamespace(-1)
		return m, nil
	case "down", "ctrl+j":
		m.moveNamespace(1)
		return m, nil
	case "pgup":
		m.moveNamespace(-m.nsListHeight())
		return m, nil
	case "pgdown":
		m.moveNamespace(m.nsListHeight())
		return m, nil
	case "enter":
		if m.ns.cursor >= len(m.ns.results) {
			return m, nil
		}
		ns := m.ns.results[m.ns.cursor]
		m.mode = modeList
		if ns == allEntry {
			if m.allNS {
				return m, nil
			}
			m.allNS = true
			m.ix, m.results, m.header = nil, nil, cache.Header{}
			m.notice, m.noticeErr = "searching all namespaces", false
			return m, m.startIndex(false) // cached namespaces load, the rest build
		}
		if ns == m.backend().Client().Namespace && !m.allNS {
			return m, nil
		}
		m.allNS = false
		m.backend().SwitchNamespace(ns)
		// Another namespace, another index: load it, or build it.
		m.ix, m.results, m.header = nil, nil, cache.Header{}
		m.notice, m.noticeErr = "switched to namespace "+nsLabel(ns), false
		return m, m.reindex()
	}
	var cmd tea.Cmd
	prev := m.ns.input.Value()
	m.ns.input, cmd = m.ns.input.Update(msg)
	if m.ns.input.Value() != prev {
		m.filterNamespaces()
	}
	return m, cmd
}

// nsLabel names a namespace for display: "/" is the root.
func nsLabel(ns string) string {
	switch ns {
	case "":
		return "/"
	case allEntry:
		return "all namespaces"
	}
	return ns
}

// allEntry is the switcher's first entry: search every namespace at once.
// It can't clash with a namespace name, which can't hold a NUL byte.
const allEntry = "\x00all"

func (m model) viewNamespace() string {
	s := m.ns
	var b strings.Builder
	b.WriteString(sTitle.Render("Namespaces") + sSubtle.Render("  "+m.backend().Client().Addr) + "\n\n")
	b.WriteString(s.input.View() + "\n")
	h := m.nsListHeight()
	lines := 0
	switch {
	case s.loading:
		b.WriteString(" " + m.spin.View() + sSubtle.Render(" listing namespaces…") + "\n")
		lines++
	case s.err != nil:
		for _, l := range strings.Split(wordWrap(s.err.Error(), m.width-2), "\n") {
			b.WriteString(" " + sErr.Render(l) + "\n")
			lines++
		}
	case len(s.results) == 0:
		b.WriteString(" " + sSubtle.Render("no namespace matches") + "\n")
		lines++
	default:
		cur := m.backend().Client().Namespace
		if m.allNS {
			cur = allEntry
		}
		terms := highlightTerms(s.input.Value())
		end := min(s.offset+h, len(s.results))
		for i := s.offset; i < end; i++ {
			ns := s.results[i]
			line := highlight(nsLabel(ns), terms, lipgloss.NewStyle())
			switch ns {
			case "":
				line += sSubtle.Render("  root")
			case allEntry:
				line += sSubtle.Render("  search every namespace below at once")
			}
			if ns == cur {
				line += sKey.Render("  (current)")
			}
			line = truncate(line, m.width-2)
			if i == s.cursor {
				line = sPointer.Render("▌") + sSel.Width(m.width-1).Render(line)
			} else {
				line = " " + line
			}
			b.WriteString(line + "\n")
			lines++
		}
	}
	for ; lines < h; lines++ {
		b.WriteString("\n")
	}
	b.WriteString(m.statusLine() + "\n")
	b.WriteString(sSubtle.Render(truncate("↑↓ move · enter switch · esc/^n back · ^c quit", m.width)))
	return b.String()
}
