// Package ui provides display utilities for the hyphctl CLI.
package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type doneMsg struct{ err error }

type spinnerModel struct {
	spinner  spinner.Model
	message  string
	quitting bool
	err      error
}

func newSpinnerModel(message string) spinnerModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(12))
	return spinnerModel{spinner: s, message: message}
}

func (m spinnerModel) Init() tea.Cmd { return m.spinner.Tick }

func (m spinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case doneMsg:
		m.quitting = true
		m.err = msg.err
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m spinnerModel) View() string {
	if m.quitting {
		if m.err != nil {
			return ErrorStyle.Render(fmt.Sprintf("✗ %s failed", m.message)) + "\n"
		}
		return SuccessStyle.Render(fmt.Sprintf("✓ %s completed", m.message)) + "\n"
	}
	return fmt.Sprintf("%s %s", m.spinner.View(), m.message)
}

// ShowSpinner runs fn in a goroutine while displaying a spinner labelled message.
// Returns the error from fn (if any).
func ShowSpinner(message string, fn func() error) error {
	m := newSpinnerModel(message)
	p := tea.NewProgram(m)

	var fnErr error
	go func() {
		fnErr = fn()
		p.Send(doneMsg{err: fnErr})
	}()

	if _, err := p.Run(); err != nil {
		return err
	}
	return fnErr
}
