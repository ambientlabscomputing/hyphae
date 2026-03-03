// Package ui provides display utilities for the hyphctl CLI.
package ui

import "github.com/charmbracelet/lipgloss"

// Colour palette (ANSI 256-colour indices — same convention as serverctl).
var (
	SuccessStyle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(10))
	ErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(9))
	WarningStyle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(11))
	InfoStyle    = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(12))
	HeaderStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.ANSIColor(12))
	CellStyle    = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(15))

	StatusOnline   = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(10)).Bold(true)
	StatusOffline  = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(9)).Bold(true)
	StatusDegraded = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(11)).Bold(true)
	StatusPending  = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(14)).Bold(true)
)

// ColorStatus applies the appropriate status colour.
func ColorStatus(s string) string {
	switch s {
	case "ok", "online", "active", "healthy", "success", "completed", "bound":
		return StatusOnline.Render(s)
	case "offline", "inactive", "unhealthy", "error", "failed", "revoked":
		return StatusOffline.Render(s)
	case "degraded", "warning", "partial":
		return StatusDegraded.Render(s)
	default:
		return StatusPending.Render(s)
	}
}
