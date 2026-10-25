package theme

import "github.com/charmbracelet/lipgloss"

// Theme contains BMP's semantic colors. Themes are compiled into the app for v1.
type Theme struct {
	Name       string
	Background lipgloss.Color
	Foreground lipgloss.Color
	Muted      lipgloss.Color
	Border     lipgloss.Color
	Cyan       lipgloss.Color
	Magenta    lipgloss.Color
	Yellow     lipgloss.Color
	Green      lipgloss.Color
	Red        lipgloss.Color
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
}

// Synthwave is a high-contrast theme: hot pink and sunset orange on black, after 1980s neon.
var Synthwave = Theme{
	Name:       "Synthwave",
	Background: lipgloss.Color("#000000"),
	Foreground: lipgloss.Color("#FFE9FA"),
	Muted:      lipgloss.Color("#8A6A8F"),
	Border:     lipgloss.Color("#3E2448"),
	Cyan:       lipgloss.Color("#2DE2E6"),
	Magenta:    lipgloss.Color("#FF2BD6"),
	Yellow:     lipgloss.Color("#FF9E2C"),
	Green:      lipgloss.Color("#4DFF9E"),
	Red:        lipgloss.Color("#FF3864"),
}

// Ember is a high-contrast theme: fire red and amber on black.
var Ember = Theme{
	Name:       "Ember",
	Background: lipgloss.Color("#000000"),
	Foreground: lipgloss.Color("#FFEBDC"),
	Muted:      lipgloss.Color("#8C6A58"),
	Border:     lipgloss.Color("#4A2A1A"),
	Cyan:       lipgloss.Color("#FFB000"),
	Magenta:    lipgloss.Color("#FF4D1A"),
	Yellow:     lipgloss.Color("#FFE066"),
	Green:      lipgloss.Color("#B8FF3A"),
	Red:        lipgloss.Color("#FF2E4E"),
}

// Glacier is a high-contrast theme: ice blue and white on black.
var Glacier = Theme{
	Name:       "Glacier",
	Background: lipgloss.Color("#000000"),
	Foreground: lipgloss.Color("#EAFBFF"),
	Muted:      lipgloss.Color("#62808E"),
	Border:     lipgloss.Color("#1F3A48"),
	Cyan:       lipgloss.Color("#00E5FF"),
	Magenta:    lipgloss.Color("#8C9EFF"),
	Yellow:     lipgloss.Color("#FFF27A"),
	Green:      lipgloss.Color("#2BFFB0"),
	Red:        lipgloss.Color("#FF4F6D"),
}

// Acid is a high-contrast theme: lime and electric purple on black.
var Acid = Theme{
	Name:       "Acid",
	Background: lipgloss.Color("#000000"),
	Foreground: lipgloss.Color("#F2FFE4"),
	Muted:      lipgloss.Color("#6E8058"),
	Border:     lipgloss.Color("#2F4420"),
	Cyan:       lipgloss.Color("#C6FF00"),
	Magenta:    lipgloss.Color("#B026FF"),
	Yellow:     lipgloss.Color("#FFF200"),
	Green:      lipgloss.Color("#39FF14"),
	Red:        lipgloss.Color("#FF2079"),
}

// Gold is a high-contrast theme: gold and warm white on black.
var Gold = Theme{
	Name:       "Gold",
	Background: lipgloss.Color("#000000"),
	Foreground: lipgloss.Color("#FFF6E0"),
	Muted:      lipgloss.Color("#8C7A55"),
	Border:     lipgloss.Color("#4A3D1F"),
	Cyan:       lipgloss.Color("#FFD700"),
	Magenta:    lipgloss.Color("#FFB347"),
	Yellow:     lipgloss.Color("#FFF1A8"),
	Green:      lipgloss.Color("#C6F432"),
	Red:        lipgloss.Color("#FF5A36"),
}
