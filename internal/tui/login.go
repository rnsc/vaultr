package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/rnsc/vaultr/internal/auth"
	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/index"
)

// openBrowser is swappable in tests.
var openBrowser = auth.OpenBrowser

type loginState struct {
	reason string
	method int // index in auth.Methods
	inputs map[string]*textinput.Model
	focus  int // index in fields()
	busy   bool
	url    string // OIDC authorization URL, once known
	err    error
	cancel context.CancelFunc
	ch     chan tea.Msg
}

type (
	loginURLMsg  string
	loginDoneMsg struct {
		summary, warning string
		err              error
	}
	cacheMsg struct {
		entries []index.Entry
		header  cache.Header
		err     error
	}
)

// fields lists the login form fields for the selected method.
func (l *loginState) fields() []string {
	f := []string{"method"}
	switch auth.Methods[l.method] {
	case auth.OIDC:
		f = append(f, "role")
	case auth.LDAP, auth.Userpass:
		f = append(f, "username", "password")
	case auth.Token:
		f = append(f, "token")
	}
	return append(f, "namespace", "mount")
}

var loginLabels = map[string]string{
	"method": "Method", "role": "Role", "username": "Username", "password": "Password",
	"token": "Token", "namespace": "Namespace", "mount": "Mount",
}

var loginPlaceholders = map[string]string{
	"role":      "default role",
	"namespace": "/ (root)",
	"mount":     "same as method",
}

func (m *model) openLogin(reason string) tea.Cmd {
	s := m.backend().Settings().Auth
	l := loginState{reason: reason, inputs: map[string]*textinput.Model{}}
	for i, meth := range auth.Methods {
		if string(meth) == s.Method {
			l.method = i
		}
	}
	values := map[string]string{"role": s.Role, "username": s.Username, "namespace": s.Namespace, "mount": s.Mount}
	if values["namespace"] == "" {
		values["namespace"] = "/"
	}
	for _, k := range []string{"role", "username", "password", "token", "namespace", "mount"} {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = loginPlaceholders[k]
		ti.SetValue(values[k])
		ti.CharLimit = 4096
		ti.Cursor.SetMode(inputCursorMode)
		if k == "password" || k == "token" {
			ti.EchoMode = textinput.EchoPassword
			ti.EchoCharacter = '•'
		}
		l.inputs[k] = &ti
	}
	// Start on the first credential that still needs typing.
	fields := l.fields()
	l.focus = 0
	for i, f := range fields[1:] {
		if in := l.inputs[f]; in != nil && in.Value() == "" && f != "role" && f != "mount" {
			l.focus = i + 1
			break
		}
	}
	if l.focus == 0 && len(fields) > 1 && auth.Methods[l.method] != auth.OIDC {
		l.focus = 1
	}
	m.login = l
	m.mode = modeLogin
	m.focusLogin()
	// auth.auto_login: when vaultr (not the user) opened this screen, an
	// OIDC login needs nothing typed, so start it. Other methods wait for
	// the password, with the cursor already there. A failed attempt isn't
	// retried by itself.
	if reason != "" && s.AutoLogin && s.Method == string(auth.OIDC) && !m.autoTried {
		m.autoTried = true
		return tea.Batch(textinput.Blink, m.submitLogin())
	}
	return textinput.Blink
}

func (m *model) focusLogin() {
	fields := m.login.fields()
	if m.login.focus >= len(fields) {
		m.login.focus = len(fields) - 1
	}
	for k, in := range m.login.inputs {
		if k == fields[m.login.focus] {
			in.Focus()
		} else {
			in.Blur()
		}
	}
}

func (m model) updateLogin(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	l := &m.login
	if l.busy {
		switch msg.String() {
		case "ctrl+c":
			l.cancel()
			return m, tea.Quit
		case "esc":
			l.cancel()
		}
		return m, nil
	}
	fields := l.fields()
	cur := fields[l.focus]
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mode = modeList
		m.pending = nil
		if m.ix == nil && m.banner == "" {
			m.banner = "Not logged in."
		}
		return m, nil
	case "tab", "down":
		l.focus = (l.focus + 1) % len(fields)
		m.focusLogin()
		return m, nil
	case "shift+tab", "up":
		l.focus = (l.focus - 1 + len(fields)) % len(fields)
		m.focusLogin()
		return m, nil
	case "enter":
		return m, m.submitLogin()
	}
	if cur == "method" {
		switch msg.String() {
		case "left", "h":
			l.method = (l.method - 1 + len(auth.Methods)) % len(auth.Methods)
		case "right", "l", " ":
			l.method = (l.method + 1) % len(auth.Methods)
		}
		l.err = nil
		return m, nil
	}
	in := l.inputs[cur]
	updated, cmd := in.Update(msg)
	*in = updated
	return m, cmd
}

func (m *model) submitLogin() tea.Cmd {
	l := &m.login
	val := func(k string) string { return strings.TrimSpace(l.inputs[k].Value()) }
	r := auth.Request{
		Method:       auth.Methods[l.method],
		Mount:        val("mount"),
		Namespace:    strings.Trim(val("namespace"), "/"),
		Username:     val("username"),
		Password:     l.inputs["password"].Value(),
		Token:        val("token"),
		Role:         val("role"),
		CallbackPort: m.backend().Settings().Auth.CallbackPort,
	}
	ch := make(chan tea.Msg, 2)
	r.OpenURL = func(u string) {
		ch <- loginURLMsg(u)
		_ = openBrowser(u)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	l.busy, l.err, l.url, l.cancel, l.ch = true, nil, "", cancel, ch
	b := m.backend()
	go func() {
		defer cancel()
		summary, warn, err := b.Login(ctx, r)
		ch <- loginDoneMsg{summary: summary, warning: warn, err: err}
	}()
	return tea.Batch(m.spin.Tick, waitFor(ch))
}

func (m model) loginDone(msg loginDoneMsg) (tea.Model, tea.Cmd) {
	l := &m.login
	l.busy, l.url = false, ""
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			l.err = nil
		} else {
			l.err = msg.err
		}
		l.inputs["password"].SetValue("")
		return m, nil
	}
	m.login = loginState{}
	m.mode = modeList
	m.banner = ""
	m.notice, m.noticeErr = msg.summary, false
	if msg.warning != "" {
		m.notice, m.noticeErr = msg.summary+"; "+msg.warning, true
	}
	cmd := m.afterLogin()
	return m, cmd
}

func (m model) viewLogin() string {
	l := m.login
	var b strings.Builder
	b.WriteString(sTitle.Render("Log in to Vault") + sSubtle.Render("  "+m.backend().Client().Addr) + "\n\n")
	if l.reason != "" {
		b.WriteString(" " + sWarn.Render(l.reason) + "\n\n")
	}
	fields := l.fields()
	for i, f := range fields {
		pointer := "  "
		label := sSubtle.Render(pad(loginLabels[f], 10))
		if i == l.focus && !l.busy {
			pointer = sPointer.Render("▌ ")
			label = sKey.Render(pad(loginLabels[f], 10))
		}
		var value string
		if f == "method" {
			var opts []string
			for j, meth := range auth.Methods {
				if j == l.method {
					opts = append(opts, sTitle.Render("["+string(meth)+"]"))
				} else {
					opts = append(opts, sSubtle.Render(string(meth)))
				}
			}
			value = strings.Join(opts, " ")
		} else {
			value = l.inputs[f].View()
		}
		b.WriteString(pointer + label + " " + value + "\n")
	}
	b.WriteString("\n")
	switch {
	case l.busy && l.url != "":
		b.WriteString(" " + m.spin.View() + " Complete the login in your browser. If it did not open, visit:\n\n")
		b.WriteString("   " + l.url + "\n")
	case l.busy:
		b.WriteString(" " + m.spin.View() + " Logging in…\n")
	case l.err != nil:
		b.WriteString(" " + sErr.Render(wordWrap(l.err.Error(), m.width-2)) + "\n")
	}
	lines := strings.Count(b.String(), "\n")
	for ; lines < m.height-1; lines++ {
		b.WriteString("\n")
	}
	help := "tab/↑↓ field · ←→ method · enter log in · esc back · ^c quit"
	if l.busy {
		help = "esc cancel · ^c quit"
	}
	b.WriteString(sSubtle.Render(truncate(help, m.width)))
	return b.String()
}
