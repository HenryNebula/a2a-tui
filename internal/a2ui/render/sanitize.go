package render

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// clean strips ANSI escape sequences and C0/C1 control characters from an
// untrusted (agent-supplied, dynamically evaluated) string before it is
// rendered: OSC clipboard writes, window-title changes, cursor movement and
// other terminal control must never reach the host terminal. Newlines are
// preserved (multi-line text components wrap on them), tabs expand to two
// spaces, and \r normalizes to \n.
//
// This deliberately mirrors clean() in internal/a2ui/widget/view.go and
// SanitizeText in internal/chat (chat keeps multi-line semantics too). The
// helpers are intentionally duplicated: each package guards an independent
// trust boundary and stays import-cycle-free. Keep their behavior in sync.
func clean(s string) string {
	s = ansi.Strip(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("  ")
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			// drop remaining C0 controls, DEL, and C1 controls
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
