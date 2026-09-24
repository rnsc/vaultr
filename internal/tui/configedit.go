package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/rnsc/vaultr/internal/config"
)

type configState struct {
	path   string
	exists bool
	values map[string]string
	cursor int
	offset int
	input  textinput.Model
	err    error
	note   string // e.g. the existing file could not be parsed
}

// envOverride returns the environment variable overriding field f, if set.
func envOverride(f config.Field) string {
	vars := []string{f.Env}
	if f.Key == "address" {
		vars = append(vars, "VAULT_URL")
	}
	for _, v := range vars {
		if v != "" && os.Getenv(v) != "" {
			return v
		}
	}
	return ""
}

func (m *model) openConfig() tea.Cmd {
	path := m.backend().Settings().Path
	if path == "" {
		path = config.DefaultPath()
	}
	f, exists, err := config.ReadFile(path)
	c := configState{path: path, exists: exists, values: f.Values()}
	if err != nil {
		c.note = "The existing file could not be read (" + err.Error() + "); saving replaces it."
		c.values = config.File{}.Values()
	}
	c.input = textinput.New()
	c.input.Prompt = ""
	c.input.CharLimit = 1024
	c.input.Cursor.SetMode(inputCursorMode)
	m.cfg = c
	m.mode = modeConfig
	m.loadConfigField()
	return textinput.Blink
}

func (m *model) cfgField() config.Field { return config.Fields[m.cfg.cursor] }

// loadConfigField puts the focused field's value into the text input.
func (m *model) loadConfigField() {
	f := m.cfgField()
	m.cfg.input.SetValue(m.cfg.values[f.Key])
	m.cfg.input.Placeholder = ""
	if ex := strings.Trim(f.Example, `"[]`); ex != "" {
		m.cfg.input.Placeholder = "e.g. " + ex
	}
	m.cfg.input.CursorEnd()
	if f.Kind == config.KindBool || f.Kind == config.KindChoice {
		m.cfg.input.Blur()
	} else {
		m.cfg.input.Focus()
	}
}

// storeConfigField saves the text input back into the values.
func (m *model) storeConfigField() {
	f := m.cfgField()
	if f.Kind != config.KindBool && f.Kind != config.KindChoice {
		m.cfg.values[f.Key] = m.cfg.input.Value()
	}
}

func (m *model) moveConfig(n int) {
	m.storeConfigField()
	m.cfg.cursor = (m.cfg.cursor + n + len(config.Fields)) % len(config.Fields)
	m.cfg.err = nil
	m.loadConfigField()
}

func (m model) updateConfig(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := &m.cfg
	f := m.cfgField()
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mode = modeList
		return m, m.setFlash("config not saved", false)
	case "down", "tab":
		m.moveConfig(1)
		return m, nil
	case "up", "shift+tab":
		m.moveConfig(-1)
		return m, nil
	case "enter":
		if f.Kind == config.KindBool || f.Kind == config.KindChoice {
			m.cycle(f, 1)
			return m, nil
		}
		m.moveConfig(1)
		return m, nil
	case "ctrl+s":
		return m.saveConfig()
	}
	switch f.Kind {
	case config.KindBool, config.KindChoice:
		switch msg.String() {
		case "left", "h":
			m.cycle(f, -1)
		case "right", "l", " ":
			m.cycle(f, 1)
		}
		return m, nil
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)
	c.err = nil
	return m, cmd
}

func (m *model) cycle(f config.Field, dir int) {
	opts := f.Choices
	if f.Kind == config.KindBool {
		opts = []string{"true", "false"}
	}
	cur := m.cfg.values[f.Key]
	if cur == "" && f.Kind == config.KindBool {
		cur = f.Default
	}
	i := 0
	for j, o := range opts {
		if o == cur {
			i = j
		}
	}
	m.cfg.values[f.Key] = opts[(i+dir+len(opts))%len(opts)]
}

func (m model) saveConfig() (tea.Model, tea.Cmd) {
	m.storeConfigField()
	f, err := config.FromValues(m.cfg.values)
	if err != nil {
		m.cfg.err = err
		return m, nil
	}
	if err := config.WriteFile(m.cfg.path, f); err != nil {
		m.cfg.err = err
		return m, nil
	}
	if err := m.backend().Reload(); err != nil {
		m.cfg.err = fmt.Errorf("saved, but the settings don't load: %w", err)
		m.cfg.exists = true
		return m, nil
	}
	m.mode = modeList
	m.notice = "saved " + m.cfg.path
	return m, m.reindex()
}

func (m model) viewConfig() string {
	c := m.cfg
	var b strings.Builder
	state := ""
	if !c.exists {
		state = sWarn.Render("  (new file)")
	}
	b.WriteString(sTitle.Render("Settings") + sSubtle.Render("  "+c.path) + state + "\n")
	if c.note != "" {
		b.WriteString(" " + sWarn.Render(wordWrap(c.note, m.width-2)) + "\n")
	}
	b.WriteString("\n")

	footer := 5 // blank, help text (2), status, keys
	avail := max(1, m.height-strings.Count(b.String(), "\n")-footer)
	if c.cursor < c.offset {
		c.offset = c.cursor
	}
	if c.cursor >= c.offset+avail {
		c.offset = c.cursor - avail + 1
	}
	end := min(len(config.Fields), c.offset+avail)
	for i := c.offset; i < end; i++ {
		f := config.Fields[i]
		label := pad(f.Key, 20)
		var value string
		switch {
		case i == c.cursor && f.Kind != config.KindBool && f.Kind != config.KindChoice:
			value = c.input.View()
		case f.Kind == config.KindChoice:
			var opts []string
			for _, o := range f.Choices {
				name := o
				if name == "" {
					name = "unset"
				}
				if o == c.values[f.Key] {
					opts = append(opts, sTitle.Render("["+name+"]"))
				} else {
					opts = append(opts, sSubtle.Render(name))
				}
			}
			value = strings.Join(opts, " ")
		case f.Kind == config.KindBool:
			v := c.values[f.Key]
			if v == "" {
				v = f.Default
			}
			value = v
		default:
			value = c.values[f.Key]
			if value == "" {
				value = sSubtle.Render("-")
			}
		}
		if env := envOverride(f); env != "" {
			value += sWarn.Render("  (overridden by " + env + ")")
		}
		line := label + " " + value
		if i == c.cursor {
			b.WriteString(sPointer.Render("▌ ") + sKey.Render(label) + " " + value + "\n")
		} else {
			b.WriteString("  " + sSubtle.Render(label) + " " + line[len(label)+1:] + "\n")
		}
	}
	for n := end - c.offset; n < avail; n++ {
		b.WriteString("\n")
	}
	b.WriteString("\n")
	help := wordWrap(m.cfgFieldHelp(), m.width-2)
	hl := strings.Split(help, "\n")
	for i := 0; i < 2; i++ {
		line := ""
		if i < len(hl) {
			line = hl[i]
		}
		b.WriteString(" " + sSubtle.Render(line) + "\n")
	}
	if c.err != nil {
		b.WriteString(truncate(sErr.Render(" "+strings.ReplaceAll(c.err.Error(), "\n", "; ")), m.width) + "\n")
	} else {
		b.WriteString("\n")
	}
	b.WriteString(sSubtle.Render(truncate("↑↓ field · type to edit · ←→/space toggle · ^s save · esc cancel · ^c quit", m.width)))
	return b.String()
}

func (m model) cfgFieldHelp() string {
	f := config.Fields[m.cfg.cursor]
	h := f.Help
	if f.Kind == config.KindList {
		h += " Comma separated."
	}
	return h
}
