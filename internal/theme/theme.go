package theme

import "github.com/charmbracelet/lipgloss"

// Theme contains BMP's semantic colors. Themes are compiled into the app for v1.
type Theme struct {
	Name          string
	Background    lipgloss.Color
	Foreground    lipgloss.Color
	Muted         lipgloss.Color
	Border        lipgloss.Color
	Cyan          lipgloss.Color
	Magenta       lipgloss.Color
	Yellow        lipgloss.Color
	Green         lipgloss.Color
	Red           lipgloss.Color
	ClipboardFade [6]lipgloss.Color
}

var Cyberpunk = Theme{
	Name:       "Cyberpunk",
	Background: lipgloss.Color("#000000"),
	Foreground: lipgloss.Color("#D8E8FF"),
	Muted:      lipgloss.Color("#65728A"),
	Border:     lipgloss.Color("#34415C"),
	Cyan:       lipgloss.Color("#36F1FF"),
	Magenta:    lipgloss.Color("#FF4FD8"),
	Yellow:     lipgloss.Color("#FFD166"),
	Green:      lipgloss.Color("#7CFF6B"),
	Red:        lipgloss.Color("#FF5470"),
	ClipboardFade: [6]lipgloss.Color{
		lipgloss.Color("#D8E8FF"),
		lipgloss.Color("#C0D1E8"),
		lipgloss.Color("#A9B9D0"),
		lipgloss.Color("#91A1B8"),
		lipgloss.Color("#7989A0"),
		lipgloss.Color("#65728A"),
	},
}

var Phosphor = Theme{
	Name:       "Phosphor",
	Background: lipgloss.Color("#000000"),
	Foreground: lipgloss.Color("#D8FFE2"),
	Muted:      lipgloss.Color("#527A5C"),
	Border:     lipgloss.Color("#285C35"),
	Cyan:       lipgloss.Color("#54FFD6"),
	Magenta:    lipgloss.Color("#B0FF62"),
	Yellow:     lipgloss.Color("#E2FF6B"),
	Green:      lipgloss.Color("#39FF70"),
	Red:        lipgloss.Color("#FF667D"),
	ClipboardFade: [6]lipgloss.Color{
		lipgloss.Color("#D8FFE2"),
		lipgloss.Color("#BDDFC5"),
		lipgloss.Color("#A2C0A9"),
		lipgloss.Color("#87A18D"),
		lipgloss.Color("#6C8271"),
		lipgloss.Color("#527A5C"),
	},
}

// Amethyst is a rich purple theme with soft lavender accents.
var Amethyst = Theme{
	Name:       "Amethyst",
	Background: lipgloss.Color("#100B1A"),
	Foreground: lipgloss.Color("#F3E8FF"),
	Muted:      lipgloss.Color("#9277AA"),
	Border:     lipgloss.Color("#4D3768"),
	Cyan:       lipgloss.Color("#B8A1FF"),
	Magenta:    lipgloss.Color("#D678FF"),
	Yellow:     lipgloss.Color("#F0C6FF"),
	Green:      lipgloss.Color("#A7E8D0"),
	Red:        lipgloss.Color("#FF7FA5"),
	ClipboardFade: [6]lipgloss.Color{
		lipgloss.Color("#F3E8FF"),
		lipgloss.Color("#DFCEED"),
		lipgloss.Color("#CBB4DB"),
		lipgloss.Color("#B79AC9"),
		lipgloss.Color("#A380B7"),
		lipgloss.Color("#9277AA"),
	},
}

// Nord is a low-glare arctic theme with a deep blue background.
var Nord = Theme{
	Name:       "Nord",
	Background: lipgloss.Color("#0B1020"),
	Foreground: lipgloss.Color("#D8DEE9"),
	Muted:      lipgloss.Color("#65738B"),
	Border:     lipgloss.Color("#334155"),
	Cyan:       lipgloss.Color("#88C0D0"),
	Magenta:    lipgloss.Color("#B48EAD"),
	Yellow:     lipgloss.Color("#EBCB8B"),
	Green:      lipgloss.Color("#A3BE8C"),
	Red:        lipgloss.Color("#BF616A"),
	ClipboardFade: [6]lipgloss.Color{
		lipgloss.Color("#D8DEE9"),
		lipgloss.Color("#C1C9D6"),
		lipgloss.Color("#AAB4C3"),
		lipgloss.Color("#93A0B0"),
		lipgloss.Color("#7C8B9D"),
		lipgloss.Color("#65738B"),
	},
}
