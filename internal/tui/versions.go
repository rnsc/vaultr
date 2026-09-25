package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// KV v2 versions in the secret view: a line under the title says which
// version is shown and when it was written (Vault doesn't record by whom),
// and [ / ] step to older and newer versions.

// shownVersion is the number of the version on screen.
func (d detailState) shownVersion() int {
	if d.version > 0 || d.meta == nil {
		return d.version
	}
	return d.meta.Current
}

// stepVersion moves to the next older (or newer) kept version.
func (m *model) stepVersion(older bool) tea.Cmd {
	d := &m.detail
	if d.meta == nil || d.loading {
		return nil
	}
	shown := d.shownVersion()
	vs := d.meta.Versions
	target := 0
	if older {
		for i := len(vs) - 1; i >= 0; i-- {
			if vs[i].N < shown {
				target = vs[i].N
				break
			}
		}
		if target == 0 {
			return m.setFlash("this is the oldest version kept", false)
		}
	} else {
		for _, v := range vs {
			if v.N > shown {
				target = v.N
				break
			}
		}
		if target == 0 {
			return m.setFlash("this is the current version", false)
		}
	}
	if target == d.meta.Current {
		target = 0
	}
	d.version, d.loading, d.err = target, true, nil
	return m.fetch(d.row.Entry, target)
}

// versionLine describes the version shown, e.g. "version 3 of 3 (current)
// · written 2h ago"; warn marks an older or unreadable version.
func (d detailState) versionLine() (text string, warn bool) {
	if d.meta == nil || d.meta.Current == 0 {
		return "", false
	}
	n := d.shownVersion()
	v, ok := d.meta.Version(n)
	text = fmt.Sprintf("version %d of %d", n, d.meta.Current)
	switch {
	case !ok:
		return text + " (not kept any more)", true
	case v.Destroyed:
		return text + " (destroyed)", true
	case !v.Deleted.IsZero():
		return text + " (deleted " + ago(v.Deleted) + ")", true
	case n == d.meta.Current:
		text += " (current)"
	default:
		warn = true
		text += " (older)"
	}
	return text + " · written " + ago(v.Created), warn
}

// ago says how long ago t was: "5m ago", "3h ago", then the date.
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return "on " + t.Local().Format("2006-01-02")
}

// versionHelp is the help for [ and ] when there is more than one version.
func (d detailState) versionHelp() string {
	if d.meta == nil || len(d.meta.Versions) < 2 {
		return ""
	}
	return " · [ ] versions"
}
