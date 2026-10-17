package app

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// enforceThemeBackground keeps the selected background active after nested
// Lip Gloss, Bubbles, or Glamour styles reset their own ANSI attributes.
func enforceThemeBackground(rendered string, background lipgloss.Color) string {
	backgroundSequence := lipgloss.ColorProfile().Color(string(background)).Sequence(true)
	if backgroundSequence == "" {
		return rendered
	}
	sequence := "\x1b[" + backgroundSequence + "m"
	rendered = strings.ReplaceAll(rendered, "\x1b[0m", "\x1b[0m"+sequence)
	rendered = strings.ReplaceAll(rendered, "\x1b[m", "\x1b[m"+sequence)
	rendered = strings.ReplaceAll(rendered, "\x1b[49m", sequence)
	return sequence + rendered + "\x1b[0m"
}

func spread(left, right string, width int) string {
	space := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + strings.Repeat(" ", space) + right
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = sanitizeTerminalText(value, false)
	return ansi.Truncate(value, width, "…")
}

func sanitizeTerminalText(value string, multiline bool) string {
	value = ansi.Strip(value)
	if multiline {
		value = strings.ReplaceAll(value, "\r\n", "\n")
		value = strings.ReplaceAll(value, "\r", "\n")
	}
	return strings.Map(func(character rune) rune {
		if !unicode.IsControl(character) {
			return character
		}
		if multiline && (character == '\n' || character == '\t') {
			return character
		}
		return ' '
	}, value)
}
