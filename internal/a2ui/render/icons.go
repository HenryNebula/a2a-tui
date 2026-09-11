package render

// iconGlyphs maps the basic catalog's 59 Material icon enum names (v1.0 and
// v0.9.1 use the same list) to terminal-friendly unicode glyphs. The mapping
// is aesthetic, not normative: renderers are free to choose their own glyphs.
// Unknown names render as the hollow bullet "◦".
var iconGlyphs = map[string]string{
	"accountCircle":    "◉",
	"add":              "+",
	"arrowBack":        "←",
	"arrowForward":     "→",
	"attachFile":       "📎",
	"calendarToday":    "📅",
	"call":             "📞",
	"camera":           "📷",
	"check":            "✓",
	"close":            "✕",
	"delete":           "⌫",
	"download":         "⤓",
	"edit":             "✎",
	"event":            "📆",
	"error":            "✖",
	"fastForward":      "≫",
	"favorite":         "♥",
	"favoriteOff":      "♡",
	"folder":           "▤",
	"help":             "?",
	"home":             "⌂",
	"info":             "ℹ",
	"locationOn":       "◎",
	"lock":             "🔒",
	"lockOpen":         "🔓",
	"mail":             "✉",
	"menu":             "☰",
	"moreVert":         "⋮",
	"moreHoriz":        "⋯",
	"notifications":    "🔔",
	"notificationsOff": "🔕",
	"pause":            "‖",
	"payment":          "💳",
	"person":           "👤",
	"phone":            "☎",
	"photo":            "🖼",
	"play":             "▶",
	"print":            "⎙",
	"refresh":          "↻",
	"rewind":           "≪",
	"search":           "⌕",
	"send":             "➤",
	"settings":         "⚙",
	"share":            "↗",
	"shoppingCart":     "🛒",
	"skipNext":         "⏭",
	"skipPrevious":     "⏮",
	"star":             "★",
	"starHalf":         "◐",
	"starOff":          "☆",
	"stop":             "■",
	"upload":           "⤒",
	"visibility":       "👁",
	"visibilityOff":    "⊘",
	"volumeDown":       "🔉",
	"volumeMute":       "🔈",
	"volumeOff":        "🔇",
	"volumeUp":         "🔊",
	"warning":          "⚠",
}

// iconCount is the expected size of iconGlyphs (all catalog enum names).
const iconCount = 59

// Glyph returns the terminal glyph for an icon name ("◦" for unknown names).
func Glyph(name string) string {
	if g, ok := iconGlyphs[name]; ok {
		return g
	}
	return "◦"
}
