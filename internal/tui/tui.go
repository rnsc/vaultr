// Package tui is the interactive search interface.
package tui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/search"
	"github.com/rnsc/vaultr/internal/vault"
)

// BuildFunc rebuilds and saves the index.
type BuildFunc func(ctx context.Context, onProgress func(index.Progress)) ([]index.Entry, cache.Header, string, error)

// Options for Run.
type Options struct {
	Client    *vault.Client
	Entries   []index.Entry // nil means build first
	Header    cache.Header
	Build     BuildFunc
	Query     string
	ClipClear time.Duration
}

// Run starts the TUI.
func Run(opt Options) error {
	m := newModel(opt)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

var (
	accent   = lipgloss.AdaptiveColor{Light: "#7c3aed", Dark: "#a78bfa"}
	subtle   = lipgloss.AdaptiveColor{Light: "#6b7280", Dark: "#9ca3af"}
	keyColor = lipgloss.AdaptiveColor{Light: "#0f766e", Dark: "#5eead4"}
	warnCol  = lipgloss.AdaptiveColor{Light: "#b45309", Dark: "#fbbf24"}
	errCol   = lipgloss.AdaptiveColor{Light: "#b91c1c", Dark: "#f87171"}

	sTitle   = lipgloss.NewStyle().Bold(true).Foreground(accent)
	sSubtle  = lipgloss.NewStyle().Foreground(subtle)
	sKey     = lipgloss.NewStyle().Foreground(keyColor)
	sMatch   = lipgloss.NewStyle().Bold(true).Underline(true)
	sSel     = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "#ede9fe", Dark: "#3b3056"})
	sWarn    = lipgloss.NewStyle().Foreground(warnCol)
	sErr     = lipgloss.NewStyle().Foreground(errCol)
	sPointer = lipgloss.NewStyle().Foreground(accent).Bold(true)
)

type mode int

const (
	modeList mode = iota
	modeDetail
	modeBuilding
)

type model struct {
	opt    Options
	input  textinput.Model
	spin   spinner.Model
	width  int
	height int
	mode   mode

	ix      *search.Index
	header  cache.Header
	results []search.Row
	cursor  int
	offset  int

	progress   index.Progress
	progressCh chan tea.Msg
	cancel     context.CancelFunc

	detail   detailState
	initCmd  tea.Cmd
	flash    string
	flashErr bool
}

type detailState struct {
	row     search.Row
	keys    []string
	values  map[string]string
	cursor  int
	reveal  bool
	loading bool
	err     error
}

type (
	progressMsg index.Progress
	builtMsg    struct {
		entries []index.Entry
		header  cache.Header
		warning string
		err     error
	}
	secretMsg struct {
		path   string
		values map[string]string
		err    error
	}
	copyValueMsg struct {
		label string
		value string
		err   error
	}
	clipClearMsg struct{ value string }
	flashMsg     struct {
		text string
		err  bool
	}
)

func newModel(opt Options) model {
	ti := textinput.New()
	ti.Prompt = "❯ "
	ti.PromptStyle = sPointer
	ti.Placeholder = "search paths and keys (k:key p:path)"
	ti.SetValue(opt.Query)
	ti.Focus()
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = sPointer
	m := model{opt: opt, input: ti, spin: sp, header: opt.Header}
	if opt.Entries != nil {
		m.setEntries(opt.Entries)
	} else {
		m.initCmd = m.startBuild()
	}
	return m
}

func (m *model) setEntries(e []index.Entry) {
	m.ix = search.New(e)
	m.refresh()
}

func (m *model) refresh() {
	if m.ix == nil {
		return
	}
	m.results = m.ix.Search(m.input.Value(), 0)
	m.cursor, m.offset = 0, 0
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.initCmd)
}

func (m *model) startBuild() tea.Cmd {
	m.mode = modeBuilding
	m.progress = index.Progress{}
	ch := make(chan tea.Msg, 16)
	m.progressCh = ch
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	build := m.opt.Build
	go func() {
		entries, h, warn, err := build(ctx, func(p index.Progress) {
			select {
			case ch <- progressMsg(p):
			default:
			}
		})
		ch <- builtMsg{entries, h, warn, err}
		close(ch)
	}()
	return tea.Batch(m.spin.Tick, waitFor(ch))
}

func waitFor(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = msg.Width - 4
		return m, nil
	case progressMsg:
		m.progress = index.Progress(msg)
		return m, waitFor(m.progressCh)
	case builtMsg:
		m.cancel = nil
		if msg.err != nil {
			if m.ix == nil {
				return m, tea.Sequence(tea.Println("vaultr: index build failed: "+msg.err.Error()), tea.Quit)
			}
			m.mode = modeList
			return m, m.setFlash("rebuild failed: "+msg.err.Error(), true)
		}
		m.header = msg.header
		m.setEntries(msg.entries)
		m.mode = modeList
		text := fmt.Sprintf("indexed %d secrets", len(msg.entries))
		if msg.warning != "" {
			return m, m.setFlash(text+"; "+msg.warning, true)
		}
		return m, m.setFlash(text, false)
	case spinner.TickMsg:
		if m.mode != modeBuilding {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case secretMsg:
		if m.mode != modeDetail || msg.path != m.detail.row.Entry.Path {
			return m, nil
		}
		m.detail.loading = false
		m.detail.err = msg.err
		m.detail.values = msg.values
		m.detail.keys = sortedKeys(msg.values)
		m.detail.cursor = 0
		if k := m.detail.row.Key; k != "" {
			for i, kk := range m.detail.keys {
				if kk == k {
					m.detail.cursor = i
				}
			}
		}
		return m, nil
	case copyValueMsg:
		if msg.err != nil {
			return m, m.setFlash(msg.err.Error(), true)
		}
		return m, m.copy(msg.label, msg.value)
	case clipClearMsg:
		if cur, err := clipboard.ReadAll(); err == nil && cur == msg.value {
			_ = clipboard.WriteAll("")
		}
		return m, nil
	case flashMsg:
		if msg.text == m.flash {
			m.flash = ""
		}
		return m, nil
	case tea.KeyMsg:
		switch m.mode {
		case modeBuilding:
			return m.updateBuilding(msg)
		case modeDetail:
			return m.updateDetail(msg)
		default:
			return m.updateList(msg)
		}
	}
	return m, nil
}

func (m model) updateBuilding(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc":
		if m.cancel != nil {
			m.cancel()
		}
		if m.ix == nil {
			return m, tea.Quit
		}
		m.mode = modeList
	}
	return m, nil
}

func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc":
		return m, tea.Quit
	case "up", "ctrl+p", "ctrl+k":
		m.move(-1)
		return m, nil
	case "down", "ctrl+n", "ctrl+j":
		m.move(1)
		return m, nil
	case "pgup":
		m.move(-m.listHeight())
		return m, nil
	case "pgdown":
		m.move(m.listHeight())
		return m, nil
	case "enter":
		if row, ok := m.selected(); ok {
			m.mode = modeDetail
			m.detail = detailState{row: row, loading: true}
			return m, m.fetch(row.Entry)
		}
		return m, nil
	case "ctrl+y":
		if row, ok := m.selected(); ok {
			if row.Key == "" {
				return m, m.setFlash("this row has no key; open it with enter", true)
			}
			return m, m.fetchAndCopy(row)
		}
		return m, nil
	case "ctrl+o":
		if row, ok := m.selected(); ok {
			return m, m.copy("path", row.Entry.Path)
		}
		return m, nil
	case "ctrl+r":
		return m, m.startBuild()
	}
	var cmd tea.Cmd
	prev := m.input.Value()
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != prev {
		m.refresh()
	}
	return m, cmd
}

func (m model) updateDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.detail
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q", "left", "backspace":
		m.mode = modeList
		m.detail = detailState{}
	case "up", "k", "ctrl+p":
		if d.cursor > 0 {
			d.cursor--
		}
	case "down", "j", "ctrl+n":
		if d.cursor < len(d.keys)-1 {
			d.cursor++
		}
	case "r", " ":
		d.reveal = !d.reveal
	case "enter", "c", "ctrl+y":
		if len(d.keys) > 0 {
			k := d.keys[d.cursor]
			return m, m.copy(k, d.values[k])
		}
	case "y", "ctrl+o":
		return m, m.copy("path", d.row.Entry.Path)
	case "R":
		d.loading = true
		return m, m.fetch(d.row.Entry)
	}
	return m, nil
}

func (m *model) move(n int) {
	m.cursor += n
	if m.cursor >= len(m.results) {
		m.cursor = len(m.results) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
}

func (m model) selected() (search.Row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.results) {
		return search.Row{}, false
	}
	return m.results[m.cursor], true
}

func (m model) listHeight() int {
	h := m.height - 4 // input, separator, status, help
	if h < 1 {
		h = 1
	}
	return h
}

func (m model) fetch(e *index.Entry) tea.Cmd {
	c := m.opt.Client
	path := e.Path
	mount := vault.Mount{Path: e.Mount, KVVersion: e.KV}
	rel := e.Rel()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		data, err := c.ReadSecret(ctx, mount, rel)
		return secretMsg{path: path, values: vault.Stringify(data), err: err}
	}
}

func (m model) fetchAndCopy(row search.Row) tea.Cmd {
	c := m.opt.Client
	mount := vault.Mount{Path: row.Entry.Mount, KVVersion: row.Entry.KV}
	rel, key := row.Entry.Rel(), row.Key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		data, err := c.ReadSecret(ctx, mount, rel)
		if err != nil {
			return copyValueMsg{err: err}
		}
		v, ok := vault.Stringify(data)[key]
		if !ok {
			return copyValueMsg{err: fmt.Errorf("key %q no longer exists (ctrl+r to reindex)", key)}
		}
		return copyValueMsg{label: key, value: v}
	}
}

func (m *model) copy(label, value string) tea.Cmd {
	err := clipboard.WriteAll(value)
	native := err == nil
	if err != nil {
		// No native clipboard (headless, SSH): fall back to OSC 52.
		termenv.NewOutput(os.Stderr).Copy(value)
	}
	cmds := []tea.Cmd{m.setFlash("copied "+label+" to clipboard", false)}
	if native && m.opt.ClipClear > 0 && label != "path" {
		cmds = append(cmds, tea.Tick(m.opt.ClipClear, func(time.Time) tea.Msg { return clipClearMsg{value} }))
	}
	return tea.Batch(cmds...)
}

func (m *model) setFlash(text string, isErr bool) tea.Cmd {
	m.flash, m.flashErr = text, isErr
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return flashMsg{text: text} })
}

func (m model) View() string {
	if m.width == 0 {
		return ""
	}
	switch m.mode {
	case modeBuilding:
		return m.viewBuilding()
	case modeDetail:
		return m.viewDetail()
	}
	return m.viewList()
}

func (m model) viewBuilding() string {
	p := m.progress
	var b strings.Builder
	b.WriteString(sTitle.Render("vaultr") + sSubtle.Render("  "+m.opt.Client.Addr) + "\n\n")
	fmt.Fprintf(&b, " %s indexing: %d secrets, %d folders", m.spin.View(), p.Secrets, p.Lists)
	if p.Denied > 0 {
		b.WriteString(sWarn.Render(fmt.Sprintf(", %d denied", p.Denied)))
	}
	b.WriteString("\n\n" + sSubtle.Render(" esc to cancel"))
	return b.String()
}

func (m model) viewList() string {
	var b strings.Builder
	b.WriteString(m.input.View() + "\n")
	h := m.listHeight()
	terms := highlightTerms(m.input.Value())
	end := min(m.offset+h, len(m.results))
	lines := 0
	for i := m.offset; i < end; i++ {
		r := m.results[i]
		line := renderRow(r, terms, m.width-2)
		if i == m.cursor {
			line = sPointer.Render("▌") + sSel.Width(m.width-1).Render(line)
		} else {
			line = " " + line
		}
		b.WriteString(line + "\n")
		lines++
	}
	for ; lines < h; lines++ {
		b.WriteString("\n")
	}
	b.WriteString(m.statusLine() + "\n")
	b.WriteString(sSubtle.Render(truncate("↑↓ move · enter open · ^y copy value · ^o copy path · ^r reindex · esc quit", m.width)))
	return b.String()
}

func (m model) statusLine() string {
	if m.flash != "" {
		st := sKey
		if m.flashErr {
			st = sErr
		}
		return truncate(st.Render(m.flash), m.width)
	}
	total := 0
	if m.ix != nil {
		total = len(m.ix.Rows)
	}
	s := fmt.Sprintf("%d/%d", len(m.results), total)
	if !m.header.Expires.IsZero() {
		left := time.Until(m.header.Expires).Round(time.Minute)
		s += fmt.Sprintf(" · cache expires in %s", left)
		if !m.header.Bound() {
			s += " (unbound)"
		}
	}
	return sSubtle.Render(truncate(s, m.width))
}

func (m model) viewDetail() string {
	d := m.detail
	var b strings.Builder
	b.WriteString(sTitle.Render(d.row.Entry.Path) + "\n\n")
	lines := 2
	switch {
	case d.loading:
		b.WriteString(sSubtle.Render(" loading…") + "\n")
		lines++
	case d.err != nil:
		b.WriteString(sErr.Render(" "+d.err.Error()) + "\n")
		lines++
	case len(d.keys) == 0:
		b.WriteString(sSubtle.Render(" (no data)") + "\n")
		lines++
	default:
		w := 0
		for _, k := range d.keys {
			w = max(w, lipgloss.Width(k))
		}
		for i, k := range d.keys {
			v := d.values[k]
			if !d.reveal {
				v = "••••••••"
			} else {
				v = strings.ReplaceAll(v, "\n", "⏎")
			}
			line := sKey.Render(pad(k, w)) + "  " + v
			line = truncate(line, m.width-2)
			if i == d.cursor {
				line = sPointer.Render("▌") + sSel.Width(m.width-1).Render(line)
			} else {
				line = " " + line
			}
			b.WriteString(line + "\n")
			lines++
		}
	}
	for ; lines < m.height-2; lines++ {
		b.WriteString("\n")
	}
	b.WriteString(m.statusLine() + "\n")
	b.WriteString(sSubtle.Render(truncate("↑↓ move · r reveal · enter/c copy value · y copy path · R reload · esc back", m.width)))
	return b.String()
}

func renderRow(r search.Row, terms []string, width int) string {
	e := r.Entry
	mount := highlight(e.Mount, terms, sSubtle)
	rest := highlight(e.Rel(), terms, lipgloss.NewStyle())
	s := mount + rest
	if r.Key != "" {
		s += sSubtle.Render("  ⟶ ") + highlight(r.Key, terms, sKey)
	}
	return truncate(s, width)
}

func highlightTerms(q string) []string {
	var out []string
	for _, f := range strings.Fields(strings.ToLower(q)) {
		if i := strings.IndexByte(f, ':'); i >= 0 {
			switch f[:i] {
			case "k", "key", "p", "path":
				f = f[i+1:]
			}
		}
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// highlight styles s with base, emphasising occurrences of any term.
func highlight(s string, terms []string, base lipgloss.Style) string {
	if len(terms) == 0 || s == "" {
		return base.Render(s)
	}
	lower := strings.ToLower(s)
	mark := make([]bool, len(s))
	for _, t := range terms {
		for from := 0; ; {
			i := strings.Index(lower[from:], t)
			if i < 0 {
				break
			}
			for j := from + i; j < from+i+len(t) && j < len(mark); j++ {
				mark[j] = true
			}
			from += i + len(t)
		}
	}
	var b strings.Builder
	hl := base.Inherit(sMatch)
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && mark[j] == mark[i] {
			j++
		}
		if mark[i] {
			b.WriteString(hl.Render(s[i:j]))
		} else {
			b.WriteString(base.Render(s[i:j]))
		}
		i = j
	}
	return b.String()
}

func truncate(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

func pad(s string, w int) string {
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
