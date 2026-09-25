// Package tui is the interactive search interface.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/rnsc/vaultr/internal/auth"
	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/config"
	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/search"
	"github.com/rnsc/vaultr/internal/vault"
)

// Backend is what the TUI needs from the application.
type Backend interface {
	Client() *vault.Client
	Settings() *config.Settings
	// LoadCache returns the cached index; errors wrapping cache.ErrStale
	// mean it must be rebuilt.
	LoadCache(ctx context.Context) ([]index.Entry, cache.Header, error)
	// Build crawls Vault and saves the index. The string is a warning.
	Build(ctx context.Context, onProgress func(index.Progress)) ([]index.Entry, cache.Header, string, error)
	// Login switches to a new token; it returns a summary and a warning.
	Login(ctx context.Context, r auth.Request) (string, string, error)
	// Reload re-reads the config file, keeping the current token.
	Reload() error
	// Namespaces lists the namespaces the token can use ("" = root).
	Namespaces(ctx context.Context) ([]string, error)
	// SwitchNamespace makes ns the namespace for the rest of the session.
	SwitchNamespace(ns string)
	// Adopt saves entries, indexed with an earlier token, under the current
	// token when both belong to the same identity; false means rebuild.
	Adopt(ctx context.Context, entries []index.Entry, prev cache.Header) (cache.Header, bool, string, error)
	// AllNamespaces loads (or, with rebuild, crawls) every namespace the
	// token can use; entries carry their namespace. The string is a warning.
	AllNamespaces(ctx context.Context, rebuild bool, onProgress func(index.Progress)) ([]index.Entry, cache.Header, string, error)
	// Recent lists the recently opened rows ("path" or "path#key"),
	// newest first; AddRecent records one.
	Recent() []string
	AddRecent(item string)
}

// Options for Run.
type Options struct {
	Backend Backend
	Entries []index.Entry // nil means build first
	Header  cache.Header
	Query   string
	// Login, when set, opens the login screen first with this reason.
	Login string
}

// inputCursorMode is the text cursor style; tests make it static so no
// blink timers are pending.
var inputCursorMode = cursor.CursorBlink

// Clipboard access, swappable in tests.
var (
	writeClipboard = clipboard.WriteAll
	readClipboard  = clipboard.ReadAll
)

// Run starts the TUI.
func Run(opt Options) error {
	final, err := tea.NewProgram(newModel(opt), tea.WithAltScreen()).Run()
	if err != nil {
		return err
	}
	return final.(model).fatal
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
	modeLogin
	modeConfig
	modeNamespace
)

type model struct {
	opt    Options
	input  textinput.Model
	spin   spinner.Model
	width  int
	height int
	mode   mode

	ix      *search.Index
	entries []index.Entry // what ix was built from, kept to re-save after a login
	header  cache.Header
	results []search.Row
	cursor  int
	offset  int
	// recentN is how many rows at the top of results are recent ones
	// (shown when the search is empty).
	recentN int

	progress   index.Progress
	progressCh chan tea.Msg
	cancel     context.CancelFunc

	detail detailState
	login  loginState
	cfg    configState
	ns     nsState
	// allNS searches every namespace at once (the switcher's first entry).
	allNS  bool
	banner string // persistent problem shown above the status line
	// notice is a message (from a login or config save) shown together
	// with the result of the reindex that follows it.
	notice    string
	noticeErr bool
	initCmd   tea.Cmd
	fatal     error
	flash     string
	flashErr  bool

	// pending is the action a dead token interrupted; it runs again after
	// the next successful login.
	pending *pendingAction
	// autoTried stops auth.auto_login from retrying a login that failed.
	autoTried bool
	// token is what the last lookup said about the token (expiry).
	token    vault.TokenInfo
	tokenOK  bool
	tickLive bool // a once-a-minute redraw is scheduled
}

type detailState struct {
	row     search.Row
	keys    []string
	values  map[string]string
	cursor  int
	reveal  bool
	loading bool
	err     error
	// version is the version shown, 0 for the current one; meta is the
	// secret's versions (KV v2, when the token may read them).
	version int
	meta    *vault.SecretMeta
	// revealSeq identifies the latest reveal, so only its timer hides.
	revealSeq int
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
		path    string
		version int
		meta    *vault.SecretMeta
		values  map[string]string
		err     error
	}
	copyValueMsg struct {
		row   search.Row
		label string
		value string
		err   error
	}
	clipClearMsg struct{ value string }
	hideMsg      struct{ seq int }
	flashMsg     struct{ text string }
)

func newModel(opt Options) model {
	ti := textinput.New()
	ti.Prompt = "❯ "
	ti.PromptStyle = sPointer
	ti.Placeholder = "search paths and keys (k:key p:path)"
	ti.SetValue(opt.Query)
	ti.Cursor.SetMode(inputCursorMode)
	ti.Focus()
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = sPointer
	m := model{opt: opt, input: ti, spin: sp, header: opt.Header}
	switch {
	case opt.Login != "":
		m.initCmd = m.openLogin(opt.Login)
	case opt.Entries != nil:
		m.setEntries(opt.Entries)
	default:
		m.initCmd = m.startBuild()
	}
	return m
}

func (m *model) setEntries(e []index.Entry) {
	m.entries = e
	m.ix = search.New(e)
	m.refresh()
}

func (m *model) refresh() {
	if m.ix == nil {
		return
	}
	m.results = m.ix.Search(m.input.Value(), 0)
	m.recentN = 0
	if m.input.Value() == "" && !m.allNS {
		m.results, m.recentN = withRecent(m.results, m.backend().Recent())
	}
	m.cursor, m.offset = 0, 0
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.initCmd, m.lookupToken())
}

func (m *model) backend() Backend { return m.opt.Backend }

// startBuild crawls Vault again (all namespaces in allNS mode).
func (m *model) startBuild() tea.Cmd { return m.startIndex(true) }

// startIndex builds the index; in allNS mode, rebuild false loads the
// namespaces that have a valid cache and builds only the others.
func (m *model) startIndex(rebuild bool) tea.Cmd {
	m.mode = modeBuilding
	m.banner = ""
	m.progress = index.Progress{}
	ch := make(chan tea.Msg, 16)
	m.progressCh = ch
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	build := m.backend().Build
	if m.allNS {
		all := m.backend().AllNamespaces
		build = func(ctx context.Context, p func(index.Progress)) ([]index.Entry, cache.Header, string, error) {
			return all(ctx, rebuild, p)
		}
	}
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
			if errors.Is(msg.err, vault.ErrTokenInvalid) {
				if m.ix != nil { // a refresh: redo it after the login
					return m, m.loginToResume(pendingAction{kind: pendingBuild})
				}
				return m, m.openLogin("Your token is missing, expired or revoked.")
			}
			m.mode = modeList
			if errors.Is(msg.err, context.Canceled) {
				return m, nil
			}
			if m.ix == nil {
				m.banner = "Indexing failed: " + msg.err.Error()
				if m.notice != "" {
					m.banner = m.notice + ". " + m.banner
					m.notice = ""
				}
				return m, nil
			}
			m.noticeErr = true
			return m, m.flashWithNotice("rebuild failed: "+msg.err.Error(), true)
		}
		m.header = msg.header
		m.setEntries(msg.entries)
		m.mode = modeList
		text, isErr := fmt.Sprintf("indexed %d secrets", len(msg.entries)), false
		if m.allNS { // loaded from the caches where they were valid
			text = fmt.Sprintf("%d secrets across all namespaces", len(msg.entries))
		}
		if msg.warning != "" {
			text, isErr = text+"; "+msg.warning, true
		}
		return m, m.flashWithNotice(text, isErr)
	case spinner.TickMsg:
		if m.mode != modeBuilding && (m.mode != modeNamespace || !m.ns.loading) {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case secretMsg:
		if m.mode != modeDetail || msg.path != m.detail.row.Entry.Path || msg.version != m.detail.version {
			return m, nil
		}
		if msg.meta != nil {
			m.detail.meta = msg.meta
		}
		if errors.Is(msg.err, vault.ErrTokenInvalid) {
			return m, m.loginToResume(pendingAction{kind: pendingOpen, row: m.detail.row})
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
		if errors.Is(msg.err, vault.ErrTokenInvalid) {
			return m, m.loginToResume(pendingAction{kind: pendingCopy, row: msg.row})
		}
		if msg.err != nil {
			return m, m.setFlash(msg.err.Error(), true)
		}
		return m, m.copy(msg.label, msg.value)
	case hideMsg:
		if m.mode == modeDetail && m.detail.reveal && msg.seq == m.detail.revealSeq {
			m.detail.reveal = false
		}
		return m, nil
	case clipClearMsg:
		if cur, err := readClipboard(); err == nil && cur == msg.value {
			_ = writeClipboard("")
		}
		return m, nil
	case flashMsg:
		if msg.text == m.flash {
			m.flash = ""
		}
		return m, nil
	case loginURLMsg:
		m.login.url = string(msg)
		return m, waitFor(m.login.ch)
	case loginDoneMsg:
		return m.loginDone(msg)
	case cacheMsg:
		return m.cacheLoaded(msg)
	case adoptMsg:
		return m.adopted(msg)
	case tokenMsg:
		return m.tokenLooked(msg)
	case minuteMsg:
		if !m.tokenOK || m.token.ExpireTime.IsZero() {
			m.tickLive = false
			return m, nil
		}
		return m, everyMinute() // redraw so the token countdown moves
	case namespacesMsg:
		return m.namespacesLoaded(msg)
	case tea.KeyMsg:
		switch m.mode {
		case modeLogin:
			return m.updateLogin(msg)
		case modeConfig:
			return m.updateConfig(msg)
		case modeNamespace:
			return m.updateNamespace(msg)
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
	case "ctrl+c":
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	case "esc":
		if m.cancel != nil {
			m.cancel()
		}
		m.mode = modeList
		if m.ix == nil {
			m.banner = "Indexing cancelled."
		}
	}
	return m, nil
}

// reindex loads the cache for the current settings and token, or builds
// the index when there is none.
func (m *model) reindex() tea.Cmd {
	if m.backend().Client().Token() == "" {
		return m.openLogin("No Vault token found.")
	}
	if m.allNS {
		return m.startIndex(false)
	}
	b := m.backend()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		e, h, err := b.LoadCache(ctx)
		return cacheMsg{entries: e, header: h, err: err}
	}
}

func (m model) cacheLoaded(msg cacheMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		// Stale or unreadable: rebuild (which also detects a bad token).
		return m, m.startBuild()
	}
	m.header = msg.header
	m.banner = ""
	m.setEntries(msg.entries)
	m.mode = modeList
	return m, m.flashWithNotice("", false)
}

// indexAge describes when the index was built, e.g. " (built 12m ago)".
func (m model) indexAge() string {
	if m.header.Created.IsZero() {
		return ""
	}
	return " (built " + time.Since(m.header.Created).Round(time.Minute).String() + " ago)"
}

// flashWithNotice shows text preceded by any pending notice.
func (m *model) flashWithNotice(text string, isErr bool) tea.Cmd {
	if m.notice != "" {
		if text != "" {
			text = text + " · " + m.notice // the short part first, so it survives truncation
		} else {
			text = m.notice
		}
		isErr = isErr || m.noticeErr
		m.notice, m.noticeErr = "", false
	}
	if text == "" {
		return nil
	}
	return m.setFlash(text, isErr)
}

func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc":
		// Clear the search first; only ctrl+c on an empty search quits.
		if m.input.Value() != "" {
			m.input.SetValue("")
			m.refresh()
			return m, nil
		}
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	case "ctrl+l":
		return m, m.openLogin("")
	case "ctrl+e":
		return m, m.openConfig()
	case "ctrl+n":
		return m, m.openNamespaces()
	case "up", "ctrl+k":
		m.move(-1)
		return m, nil
	case "down", "ctrl+j":
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
			return m, tea.Batch(m.fetch(row.Entry, 0), m.addRecent(row))
		}
		if m.ix != nil && m.input.Value() != "" {
			// Nothing matches: maybe it was added after the index was built.
			return m, m.startBuild()
		}
		return m, nil
	case "ctrl+y":
		if row, ok := m.selected(); ok {
			if row.Key == "" {
				return m, m.setFlash("this row has no key; open it with enter", true)
			}
			return m, tea.Batch(m.fetchAndCopy(row), m.addRecent(row))
		}
		return m, nil
	case "ctrl+o":
		if row, ok := m.selected(); ok {
			return m, m.copy("path", row.Entry.Path)
		}
		return m, nil
	case "ctrl+r":
		if m.backend().Client().Token() == "" {
			return m, m.openLogin("No Vault token found.")
		}
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
		// Hide again after reveal_timeout, so values don't stay on screen
		// (and in the terminal's memory) in a session left open.
		if t := m.backend().Settings().RevealTimeout; d.reveal && t > 0 {
			d.revealSeq++
			seq := d.revealSeq
			return m, tea.Tick(t, func(time.Time) tea.Msg { return hideMsg{seq} })
		}
	case "enter", "c", "ctrl+y":
		if len(d.keys) > 0 {
			k := d.keys[d.cursor]
			return m, m.copy(k, d.values[k])
		}
	case "y", "ctrl+o":
		return m, m.copy("path", d.row.Entry.Path)
	case "R":
		d.loading = true
		return m, m.fetch(d.row.Entry, d.version)
	case "[", "]":
		return m, m.stepVersion(msg.String() == "[")
	case "o":
		e := d.row.Entry
		u := m.clientFor(e).UIURL(vault.Mount{Path: e.Mount, KVVersion: e.KV}, e.Rel())
		if err := openBrowser(u); err != nil {
			return m, m.setFlash("could not open a browser: "+u, true)
		}
		return m, m.setFlash("opened in the browser: "+u, false)
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

// listHelp is the list's help line.
const listHelp = "↑↓ move · enter open · ^y copy value · ^o copy path · ^r refresh · ^n namespace · ^l login · ^e config · esc clear · ^c quit"

func (m model) listHeight() int {
	h := m.height - 2 - helpLines(listHelp, m.width) // input, status, help
	if h < 1 {
		h = 1
	}
	return h
}

// clientFor is the client reading e: in allNS mode, one in e's namespace.
func (m model) clientFor(e *index.Entry) *vault.Client {
	c := m.opt.Backend.Client()
	if m.allNS {
		return c.InNamespace(e.Namespace)
	}
	return c
}

// fetch reads a secret (version 0: the current one) and, for KV v2, its
// versions; a token that may read the data but not the metadata just
// doesn't see the versions.
func (m model) fetch(e *index.Entry, version int) tea.Cmd {
	c := m.clientFor(e)
	path := e.Path
	mount := vault.Mount{Path: e.Mount, KVVersion: e.KV}
	rel := e.Rel()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var data map[string]any
		var err error
		if version > 0 {
			data, err = c.ReadSecretVersion(ctx, mount, rel, version)
		} else {
			data, err = c.ReadSecret(ctx, mount, rel)
		}
		msg := secretMsg{path: path, version: version, values: vault.Stringify(data), err: tokenDead(ctx, c, err)}
		if mount.KVVersion == 2 {
			if meta, merr := c.SecretMetadata(ctx, mount, rel); merr == nil {
				msg.meta = &meta
			}
		}
		return msg
	}
}

func (m model) fetchAndCopy(row search.Row) tea.Cmd {
	c := m.clientFor(row.Entry)
	mount := vault.Mount{Path: row.Entry.Mount, KVVersion: row.Entry.KV}
	rel, key := row.Entry.Rel(), row.Key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		data, err := c.ReadSecret(ctx, mount, rel)
		if err != nil {
			return copyValueMsg{row: row, err: tokenDead(ctx, c, err)}
		}
		v, ok := vault.Stringify(data)[key]
		if !ok {
			return copyValueMsg{err: fmt.Errorf("key %q no longer exists (ctrl+r to reindex)", key)}
		}
		return copyValueMsg{label: key, value: v}
	}
}

func (m *model) copy(label, value string) tea.Cmd {
	err := writeClipboard(value)
	native := err == nil
	if err != nil {
		// No native clipboard (headless, SSH): fall back to OSC 52.
		termenv.NewOutput(os.Stderr).Copy(value)
	}
	cmds := []tea.Cmd{m.setFlash("copied "+label+" to clipboard", false)}
	if clear := m.backend().Settings().ClipClear; native && clear > 0 && label != "path" {
		cmds = append(cmds, tea.Tick(clear, func(time.Time) tea.Msg { return clipClearMsg{value} }))
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
	case modeLogin:
		return m.viewLogin()
	case modeConfig:
		return m.viewConfig()
	case modeNamespace:
		return m.viewNamespace()
	}
	return m.viewList()
}

func (m model) viewBuilding() string {
	p := m.progress
	var b strings.Builder
	b.WriteString(sTitle.Render("vaultr") + sSubtle.Render("  "+m.opt.Backend.Client().Addr) + "\n\n")
	fmt.Fprintf(&b, " %s indexing: %d secrets, %d folders", m.spin.View(), p.Secrets, p.Lists)
	if p.Denied > 0 {
		b.WriteString(sWarn.Render(fmt.Sprintf(", %d denied", p.Denied)))
	}
	b.WriteString("\n\n" + sSubtle.Render(" esc cancel · ^c quit"))
	return b.String()
}

func (m model) viewList() string {
	var b strings.Builder
	b.WriteString(m.input.View() + "\n")
	h := m.listHeight()
	if m.banner != "" {
		for _, l := range strings.Split(wordWrap(m.banner+" Press ^e to edit the config, ^l to log in, ^r to retry.", m.width-2), "\n") {
			b.WriteString(" " + sWarn.Render(l) + "\n")
			h--
		}
	}
	terms := highlightTerms(m.input.Value())
	end := min(m.offset+h, len(m.results))
	lines := 0
	for i := m.offset; i < end; i++ {
		r := m.results[i]
		var line string
		if i < m.recentN {
			line = renderRow(r, terms, m.width-2-len("  recent"), m.allNS) + sSubtle.Render("  recent")
		} else {
			line = renderRow(r, terms, m.width-2, m.allNS)
		}
		if i == m.cursor {
			line = sPointer.Render("▌") + sSel.Width(m.width-1).Render(line)
		} else {
			line = " " + line
		}
		b.WriteString(line + "\n")
		lines++
	}
	if lines == 0 && m.ix != nil && m.input.Value() != "" && h > 1 {
		hint := wordWrap("No match. Added it recently? Press enter or ^r to refresh the index"+m.indexAge()+".", m.width-2)
		for _, l := range append([]string{""}, strings.Split(hint, "\n")...) {
			if lines < h {
				b.WriteString(" " + sSubtle.Render(l) + "\n")
				lines++
			}
		}
	}
	for ; lines < h; lines++ {
		b.WriteString("\n")
	}
	b.WriteString(m.statusLine() + "\n")
	helpText, _ := helpBlock(listHelp, m.width)
	b.WriteString(helpText)
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
	if m.allNS {
		s = "all namespaces · " + s
	} else if ns := m.opt.Backend.Client().Namespace; ns != "" {
		s = "ns " + ns + " · " + s
	}
	switch {
	case !m.header.Expires.IsZero():
		left := time.Until(m.header.Expires).Round(time.Minute)
		s += fmt.Sprintf(" · cache expires in %s", left)
		if !m.header.Bound() {
			s += " (unbound)"
		}
	case m.ix != nil:
		s += " · index not saved for this login (^r rebuilds it)"
	}
	tok, warn := m.tokenStatus()
	if tok == "" {
		return sSubtle.Render(truncate(s, m.width))
	}
	// The token part comes first when it needs attention, so truncation
	// on narrow terminals never hides it.
	if warn {
		return truncate(sWarn.Render(tok)+sSubtle.Render(" · "+s), m.width)
	}
	return sSubtle.Render(truncate(s+" · "+tok, m.width))
}

func (m model) viewDetail() string {
	d := m.detail
	var b strings.Builder
	title := sTitle.Render(d.row.Entry.Path)
	if m.allNS {
		title = sSubtle.Render(nsLabel(d.row.Entry.Namespace)+" · ") + title
	}
	b.WriteString(title + "\n")
	lines := 2
	if vl, warn := d.versionLine(); vl != "" {
		st := sSubtle
		if warn {
			st = sWarn
		}
		b.WriteString(st.Render(truncate(vl, m.width)) + "\n")
		lines++
	}
	b.WriteString("\n")
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
	helpText, helpN := helpBlock("↑↓ move · r reveal · c copy value · y copy path"+d.versionHelp()+" · o Vault UI · R reload · esc back", m.width)
	for ; lines < m.height-1-helpN; lines++ {
		b.WriteString("\n")
	}
	b.WriteString(m.statusLine() + "\n")
	b.WriteString(helpText)
	return b.String()
}

func renderRow(r search.Row, terms []string, width int, withNS bool) string {
	e := r.Entry
	// Too wide: the middle of the path gives way first, so the mount, the
	// end of the path and the key stay visible.
	mountText, rel := e.Mount, e.Rel()
	other := 0 // namespace and key
	if withNS {
		other += lipgloss.Width(nsLabel(e.Namespace)) + 3
	}
	if r.Key != "" {
		other += 4 + lipgloss.Width(r.Key)
	}
	room := width - other
	switch {
	case lipgloss.Width(mountText)+lipgloss.Width(rel) <= room:
	case room-lipgloss.Width(mountText) >= 6:
		rel = tail(rel, room-lipgloss.Width(mountText))
	case room >= 6: // very narrow: the mount gives way too
		mountText, rel = "", tail(mountText+rel, room)
	}
	mount := highlight(mountText, terms, sSubtle)
	rest := highlight(rel, terms, lipgloss.NewStyle())
	s := mount + rest
	if withNS {
		s = highlight(nsLabel(e.Namespace), terms, sKey) + sSubtle.Render(" · ") + s
	}
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

// tokenDead tells an expired or revoked token apart from a policy denial:
// Vault answers both with "permission denied", so on that error it asks
// whether the token is still valid. A dead token's error wraps
// vault.ErrTokenInvalid.
func tokenDead(ctx context.Context, c *vault.Client, err error) error {
	if err == nil || !errors.Is(err, vault.ErrForbidden) {
		return err
	}
	if _, lerr := c.LookupSelf(ctx); errors.Is(lerr, vault.ErrTokenInvalid) {
		return fmt.Errorf("%w (%w)", vault.ErrTokenInvalid, err)
	}
	return err
}

// wordWrap breaks s into lines of at most width runes at spaces.
func wordWrap(s string, width int) string {
	if width < 10 {
		return s
	}
	var out, line strings.Builder
	for _, w := range strings.Fields(s) {
		if line.Len() > 0 && lipgloss.Width(line.String())+1+lipgloss.Width(w) > width {
			out.WriteString(line.String() + "\n")
			line.Reset()
		}
		if line.Len() > 0 {
			line.WriteString(" ")
		}
		line.WriteString(w)
	}
	out.WriteString(line.String())
	return out.String()
}
