package ui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"bmp/internal/theme"
)

// Hint describes one context-sensitive keyboard action.
type Hint struct {
	Key   string
	Label string
}

// Panel renders a bordered dashboard pane at an exact outer size. The title is
// embedded in the top border to preserve one additional row for content.
// Interactive panes can supply compact keyboard hints pinned to the bottom,
// with status right-aligned on the same row when it fits.
func Panel(title, content string, width, height int, focused bool, colors theme.Theme, status string, hints ...Hint) string {
	if width < 4 || height < 3 {
		return ""
	}

	borderColor := colors.Border
	titleColor := colors.Muted
	if focused {
		borderColor = colors.Cyan
		titleColor = colors.Magenta
	}

	innerHeight := height - 2
	contentHeight := innerHeight
	if len(hints) > 0 {
		contentHeight--
	}
	content = limitLines(content, max(0, contentHeight))

	bodyLines := make([]string, 0, innerHeight)
	if contentHeight > 0 {
		if content != "" {
			bodyLines = append(bodyLines, strings.Split(content, "\n")...)
		}
		for len(bodyLines) < contentHeight {
			bodyLines = append(bodyLines, "")
		}
	}
	if len(hints) > 0 {
		// Hints that do not fit are dropped from the end rather than wrapped.
		footer := Shortcuts(colors, hints...)
		for count := len(hints) - 1; count > 0 && lipgloss.Width(footer) > width-2; count-- {
			footer = Shortcuts(colors, hints[:count]...)
		}
		if gap := width - 2 - lipgloss.Width(footer) - lipgloss.Width(status); status != "" && gap >= 2 {
			footer += lipgloss.NewStyle().Background(colors.Background).Render(strings.Repeat(" ", gap)) +
				lipgloss.NewStyle().Foreground(colors.Muted).Background(colors.Background).Render(status)
		}
		bodyLines = append(bodyLines, footer)
	}
	body := strings.Join(bodyLines, "\n")

	rendered := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Foreground(colors.Foreground).
		Background(colors.Background).
		Width(width - 2).
		Height(innerHeight).
		Render(body)
	lines := strings.Split(rendered, "\n")
	if len(lines) > 0 {
		lines[0] = panelTitleBorder(title, width, borderColor, titleColor, colors.Background)
	}
	return strings.Join(lines, "\n")
}

func panelTitleBorder(title string, width int, borderColor, titleColor, background lipgloss.Color) string {
	title = ansi.Strip(title)
	title = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, title)
	maximumTitleWidth := max(0, width-5)
	title = ansi.Truncate(title, maximumTitleWidth, "…")
	label := ""
	if title != "" {
		label = " " + title + " "
	}
	fill := max(0, width-3-lipgloss.Width(label))
	borderStyle := lipgloss.NewStyle().Foreground(borderColor).Background(background)
	titleStyle := lipgloss.NewStyle().Foreground(titleColor).Background(background).Bold(true)
	return borderStyle.Render("╭─") + titleStyle.Render(label) + borderStyle.Render(strings.Repeat("─", fill)+"╮")
}

// Shortcuts renders key/action pairs with emphasized keys.
func Shortcuts(colors theme.Theme, hints ...Hint) string {
	parts := make([]string, 0, len(hints))
	for _, hint := range hints {
		key := lipgloss.NewStyle().Foreground(colors.Magenta).Background(colors.Background).Bold(true).Render(hint.Key)
		if hint.Label == "" {
			parts = append(parts, key)
			continue
		}
		label := lipgloss.NewStyle().Foreground(colors.Muted).Background(colors.Background).Render(hint.Label)
		parts = append(parts, key+" "+label)
	}
	return strings.Join(parts, "  ")
}

func limitLines(value string, count int) string {
	if count <= 0 {
		return ""
	}
	lines := strings.Split(value, "\n")
	if len(lines) > count {
		lines = lines[:count]
	}
	return strings.Join(lines, "\n")
}
