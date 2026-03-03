// Package cli contains the root Cobra command and Execute entry point for hyphctl.
package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	adminclient "github.com/ambientlabscomputing/hyphae/cmd/hyphctl/client"
	"github.com/ambientlabscomputing/hyphae/cmd/hyphctl/commands"
	"github.com/ambientlabscomputing/hyphae/cmd/hyphctl/ui"
)

var (
	socketPath string
	timeout    time.Duration
	outputFmt  string
)

func buildRoot(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "hyphctl",
		Short:         "hyphctl -- admin CLI for hyphae",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			format := ui.OutputFormat(outputFmt)
			switch format {
			case ui.FormatTable, ui.FormatJSON, ui.FormatYAML, ui.FormatWide:
			default:
				format = ui.FormatTable
			}
			ctx := cmd.Context()
			ctx = ui.NewPrinterInContext(ctx, format)
			c, err := adminclient.NewAdminClient(socketPath, timeout)
			if err != nil {
				return fmt.Errorf("connect to admin socket %s: %w", socketPath, err)
			}
			ctx = adminclient.WithClient(ctx, c)
			cmd.SetContext(ctx)
			return nil
		},
		PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
			if c := adminclient.GetClient(cmd.Context()); c != nil {
				return c.Close()
			}
			return nil
		},
	}

	root.PersistentFlags().StringVar(&socketPath, "socket", "/tmp/hyphae_admin.sock", "Path to hyphae admin socket")
	root.PersistentFlags().DurationVar(&timeout, "timeout", 10*time.Second, "gRPC call timeout")
	root.PersistentFlags().StringVarP(&outputFmt, "output", "o", "table", "Output format: table|json|yaml|wide")

	root.AddCommand(
		commands.HealthCmd(),
		commands.LeasesCmd(),
		commands.ConnectionsCmd(),
	)

	return root
}

func Execute(ctx context.Context, version string) error {
	return buildRoot(version).ExecuteContext(ctx)
}
