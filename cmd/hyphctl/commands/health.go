// Package commands contains all hyphctl subcommand implementations.
package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ambientlabscomputing/hyphae/cmd/hyphctl/client"
	"github.com/ambientlabscomputing/hyphae/cmd/hyphctl/ui"
	"github.com/ambientlabscomputing/hyphae/internal/proto/admin"
)

// deps bundles per-command dependencies retrieved from cobra context.
type deps struct {
	client  *client.AdminClient
	printer *ui.Printer
	ctx     context.Context
}

func getDeps(cmd *cobra.Command) (*deps, error) {
	ctx := cmd.Context()
	c := client.GetClient(ctx)
	if c == nil {
		return nil, fmt.Errorf("admin client not available — is hyphae running?")
	}
	p := ui.GetPrinter(ctx)
	if p == nil {
		p = ui.NewPrinter(ui.FormatTable)
	}
	return &deps{client: c, printer: p, ctx: ctx}, nil
}

// HealthCmd returns the health command group.
func HealthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Health checks and diagnostics",
	}
	cmd.AddCommand(healthCheckCmd(), healthDetailCmd())
	return cmd
}

func healthCheckCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check",
		Short: "Quick status check",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := getDeps(cmd)
			if err != nil {
				return err
			}
			resp, err := d.client.Health().Check(d.ctx, &admin.Empty{})
			if err != nil {
				return fmt.Errorf("%s", ui.HandleGRPCError(err))
			}
			switch d.printer.Format() {
			case ui.FormatJSON, ui.FormatYAML:
				return d.printer.PrintData(resp)
			default:
				d.printer.PrintKeyValue("Status", ui.ColorStatus(resp.Status))
				d.printer.PrintKeyValue("Leases", fmt.Sprintf("%d", resp.LeaseCount))
				d.printer.PrintKeyValue("Connections", fmt.Sprintf("%d", resp.ConnectionCount))
			}
			return nil
		},
	}
}

func healthDetailCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "detail",
		Short: "Detailed diagnostics",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := getDeps(cmd)
			if err != nil {
				return err
			}
			resp, err := d.client.Health().Detail(d.ctx, &admin.Empty{})
			if err != nil {
				return fmt.Errorf("%s", ui.HandleGRPCError(err))
			}
			switch d.printer.Format() {
			case ui.FormatJSON, ui.FormatYAML:
				return d.printer.PrintData(resp)
			default:
				d.printer.PrintKeyValue("Status", ui.ColorStatus(resp.Status))
				d.printer.PrintKeyValue("Leases", fmt.Sprintf("%d", resp.LeaseCount))
				d.printer.PrintKeyValue("Connections", fmt.Sprintf("%d", resp.ConnectionCount))
				d.printer.PrintKeyValue("Uptime", resp.Uptime)
				d.printer.PrintKeyValue("Version", resp.Version)
			}
			return nil
		},
	}
}
