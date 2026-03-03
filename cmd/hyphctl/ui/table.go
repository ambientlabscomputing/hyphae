// Package ui provides display utilities for the hyphctl CLI.
package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

type TableBuilder struct {
	title   string
	headers []string
	rows    [][]string
}

func NewTableBuilder() *TableBuilder { return &TableBuilder{} }

func (b *TableBuilder) WithTitle(title string) *TableBuilder { b.title = title; return b }

func (b *TableBuilder) WithHeaders(headers ...string) *TableBuilder {
	b.headers = headers
	return b
}

func (b *TableBuilder) AddRow(values ...interface{}) *TableBuilder {
	row := make([]string, len(values))
	for i, v := range values {
		row[i] = fmt.Sprintf("%v", v)
	}
	b.rows = append(b.rows, row)
	return b
}

func (b *TableBuilder) Render() string {
	var sb strings.Builder
	if b.title != "" {
		sb.WriteString(HeaderStyle.Render(b.title))
		sb.WriteString("\n")
	}
	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8))).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return HeaderStyle
			}
			return CellStyle
		}).
		Headers(b.headers...).
		Rows(b.rows...)
	sb.WriteString(t.Render())
	return sb.String()
}

func KeyValueTable(data map[string]string) string {
	b := NewTableBuilder().WithHeaders("Key", "Value")
	for k, v := range data {
		b.AddRow(k, v)
	}
	return b.Render()
}
