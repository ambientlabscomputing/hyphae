// Package cli contains the root Cobra command and Execute entry point for hyphctl.
package cli

import (
	"context"

	"github.com/ambientlabscomputing/adminclicore/skeleton"
	"github.com/ambientlabscomputing/hyphae/cmd/hyphctl/commands"
)

// Execute builds the root command and runs it.
func Execute(ctx context.Context, version string) error {
	root := skeleton.NewRootCommand(skeleton.AppConfig{
		Name:          "hyphctl",
		Short:         "hyphctl -- admin CLI for hyphae",
		Version:       version,
		DefaultSocket: "/tmp/hyphae_admin.sock",
	})

	root.AddCommand(
		commands.HealthCmd(),
		commands.LeasesCmd(),
		commands.ConnectionsCmd(),
	)

	return root.ExecuteContext(ctx)
}
