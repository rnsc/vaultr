package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Fitting a resized window: help lines wrap instead of losing their last
// items, and long rows are shortened in the middle so the key stays
// visible.

// helpBlock renders a " · "-separated help text, wrapped between items to
// width, and says how many lines it takes.
func helpBlock(text string, width int) (string, int) {
	var lines []string
	line := ""
	for _, item := range strings.Split(text, " · ") {
		switch {
		case line == "":
			line = item
		case width > 0 && lipgloss.Width(line+" · "+item) > width:
			lines = append(lines, line)
			line = item
		default:
			line += " · " + item
		}
	}
	lines = append(lines, line)
	for i, l := range lines {
		lines[i] = sSubtle.Render(truncate(l, width))
	}
	return strings.Join(lines, "\n"), len(lines)
}

// helpLines is how many lines helpBlock(text) takes at width.
func helpLines(text string, width int) int {
	_, n := helpBlock(text, width)
	return n
}

// tail keeps the end of s that fits in width cells, after a "…".
func tail(s string, width int) string {
	if width < 1 {
		return ""
	}
	r := []rune(s)
	w := 1 // the ellipsis
	i := len(r)
	for i > 0 {
		cw := lipgloss.Width(string(r[i-1]))
		if w+cw > width {
			break
		}
		w += cw
		i--
	}
	return "…" + string(r[i:])
}
