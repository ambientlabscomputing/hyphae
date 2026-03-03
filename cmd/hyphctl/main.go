// hyphctl is the admin CLI for hyphae.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ambientlabscomputing/hyphae/cmd/hyphctl/cli"
	"github.com/ambientlabscomputing/hyphae/cmd/hyphctl/ui"
)

// version is overridden at link time via -X main.version=<tag>.
var version = "dev"

func main() {
	ctx := context.Background()
	ctx = ui.NewPrinterInContext(ctx, ui.FormatTable)

	if err := cli.Execute(ctx, version); err != nil {
		p := ui.GetPrinter(ctx)
		if p != nil {
			p.PrintError(err.Error())
		} else {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		os.Exit(1)
	}
}
