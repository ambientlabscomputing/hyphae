// Package ui provides display utilities for the hyphctl CLI.
package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type OutputFormat string

const (
	FormatTable OutputFormat = "table"
	FormatJSON  OutputFormat = "json"
	FormatYAML  OutputFormat = "yaml"
	FormatWide  OutputFormat = "wide"
)

type printerKey struct{}

type Printer struct {
	format OutputFormat
	w      *os.File
}

func NewPrinter(format OutputFormat) *Printer { return &Printer{format: format, w: os.Stdout} }

func NewPrinterInContext(ctx context.Context, format OutputFormat) context.Context {
	return context.WithValue(ctx, printerKey{}, NewPrinter(format))
}

func GetPrinter(ctx context.Context) *Printer {
	p, _ := ctx.Value(printerKey{}).(*Printer)
	return p
}

func (p *Printer) Format() OutputFormat { return p.format }

func (p *Printer) Println(s string) { fmt.Fprintln(p.w, s) }

func (p *Printer) PrintSuccess(msg string) { fmt.Fprintln(p.w, SuccessStyle.Render("OK "+msg)) }
func (p *Printer) PrintError(msg string)   { fmt.Fprintln(p.w, ErrorStyle.Render("ERR "+msg)) }
func (p *Printer) PrintWarning(msg string) { fmt.Fprintln(p.w, WarningStyle.Render("WARN "+msg)) }
func (p *Printer) PrintInfo(msg string)    { fmt.Fprintln(p.w, InfoStyle.Render("INFO "+msg)) }

func (p *Printer) PrintKeyValue(key, value string) {
	fmt.Fprintf(p.w, "%s %s\n", HeaderStyle.Render(key+":"), value)
}

func (p *Printer) PrintData(v interface{}) error {
	switch p.format {
	case FormatJSON:
		return p.PrintJSON(v)
	case FormatYAML:
		return p.PrintYAML(v)
	default:
		return p.PrintJSON(v)
	}
}

func (p *Printer) PrintJSON(v interface{}) error {
	enc := json.NewEncoder(p.w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (p *Printer) PrintYAML(v interface{}) error {
	return yaml.NewEncoder(p.w).Encode(v)
}
