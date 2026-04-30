package ui

import "github.com/charmbracelet/lipgloss"

// Color palette.
var (
	ColorPrimary   = lipgloss.Color("#7C3AED") // purple
	ColorSecondary = lipgloss.Color("#6366F1") // indigo
	ColorAccent    = lipgloss.Color("#22D3EE") // cyan
	ColorSuccess   = lipgloss.Color("#34D399") // green
	ColorWarning   = lipgloss.Color("#FBBF24") // amber
	ColorError     = lipgloss.Color("#F87171") // red
	ColorMuted     = lipgloss.Color("#6B7280") // gray
	ColorText      = lipgloss.Color("#E5E7EB") // light gray
	ColorBg        = lipgloss.Color("#111827") // dark bg
)

// Common styles.
var (
	StyleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			MarginBottom(1)

	StyleSubtle = lipgloss.NewStyle().
			Foreground(ColorMuted)

	StyleAccent = lipgloss.NewStyle().
			Foreground(ColorAccent)

	StyleSuccess = lipgloss.NewStyle().
			Foreground(ColorSuccess)

	StyleError = lipgloss.NewStyle().
			Foreground(ColorError)

	StyleBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorMuted).
			Padding(0, 1)

	StyleStatusBar = lipgloss.NewStyle().
			Foreground(ColorText).
			Background(lipgloss.Color("#1F2937")).
			Padding(0, 1)

	StyleKey = lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)

	StyleKeyDesc = lipgloss.NewStyle().
			Foreground(ColorMuted)

	StyleSelected = lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true)
)
