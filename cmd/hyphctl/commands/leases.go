package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ambientlabscomputing/adminclicore/flags"
	"github.com/ambientlabscomputing/adminclicore/ui"
	"github.com/ambientlabscomputing/hyphae/internal/proto/admin"
)

// LeasesCmd returns the leases command group.
func LeasesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "leases",
		Short: "Manage tunnel leases",
	}
	cmd.AddCommand(leasesListCmd(), leasesGetCmd(), leasesRevokeCmd())
	return cmd
}

func leasesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all leases",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := getDeps(cmd)
			if err != nil {
				return err
			}
			resp, err := d.client.Leases().List(d.ctx, &admin.Empty{})
			if err != nil {
				return fmt.Errorf("%s", ui.HandleGRPCError(err, "is hyphae running with admin socket enabled?"))
			}
			switch d.printer.Format() {
			case ui.FormatJSON, ui.FormatYAML:
				return d.printer.PrintData(resp)
			default:
				t := ui.NewTableBuilder(d.printer.Format()).
					WithTitle("Leases").
					WithHeaders("ID", "Hostname", "Org ID", "Server ID", "Status", "Created At", "Bound At")
				for _, l := range resp.Leases {
					t.AddRow(l.LeaseId, l.Hostname, l.OrgId, l.ServerId, ui.ColorStatus(l.Status), l.CreatedAt, l.BoundAt)
				}
				d.printer.Println(t.Render())
			}
			return nil
		},
	}
}

func leasesGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <lease-id>",
		Short: "Get a specific lease",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := getDeps(cmd)
			if err != nil {
				return err
			}
			resp, err := d.client.Leases().Get(d.ctx, &admin.GetLeaseRequest{LeaseId: args[0]})
			if err != nil {
				return fmt.Errorf("%s", ui.HandleGRPCError(err, "is hyphae running with admin socket enabled?"))
			}
			switch d.printer.Format() {
			case ui.FormatJSON, ui.FormatYAML:
				return d.printer.PrintData(resp)
			default:
				d.printer.PrintKeyValue("Lease ID", resp.LeaseId)
				d.printer.PrintKeyValue("Hostname", resp.Hostname)
				d.printer.PrintKeyValue("Org ID", resp.OrgId)
				d.printer.PrintKeyValue("Server ID", resp.ServerId)
				d.printer.PrintKeyValue("Status", ui.ColorStatus(resp.Status))
				d.printer.PrintKeyValue("Created At", resp.CreatedAt)
				d.printer.PrintKeyValue("Bound At", resp.BoundAt)
				d.printer.PrintKeyValue("Expires At", resp.ExpiresAt)
			}
			return nil
		},
	}
}

func leasesRevokeCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "revoke <lease-id>",
		Short: "Revoke a lease and close its tunnel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := getDeps(cmd)
			if err != nil {
				return err
			}

			ok, err := flags.ConfirmOrSkip(d.printer, yes, fmt.Sprintf("Revoke lease %s?", args[0]))
			if err != nil {
				return err
			}
			if !ok {
				d.printer.PrintWarning("Aborted")
				return nil
			}

			_, err = d.client.Leases().Revoke(d.ctx, &admin.RevokeLeaseRequest{LeaseId: args[0]})
			if err != nil {
				return fmt.Errorf("%s", ui.HandleGRPCError(err, "is hyphae running with admin socket enabled?"))
			}
			d.printer.PrintSuccess("Lease " + args[0] + " revoked")
			return nil
		},
	}
	flags.RegisterYesFlag(cmd, &yes)
	return cmd
}
