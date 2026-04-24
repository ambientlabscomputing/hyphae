package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ambientlabscomputing/adminclicore/ui"
	"github.com/ambientlabscomputing/hyphae/internal/proto/admin"
)

func ConnectionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connections",
		Short: "Inspect live tunnel connections",
	}
	cmd.AddCommand(connectionsListCmd())
	return cmd
}

func connectionsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List active tunnel connections",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := getDeps(cmd)
			if err != nil {
				return err
			}
			resp, err := d.client.Connections().List(d.ctx, &admin.Empty{})
			if err != nil {
				return fmt.Errorf("%s", ui.HandleGRPCError(err, "is hyphae running with admin socket enabled?"))
			}
			switch d.printer.Format() {
			case ui.FormatJSON, ui.FormatYAML:
				return d.printer.PrintData(resp)
			default:
				t := ui.NewTableBuilder(d.printer.Format()).
					WithTitle("Connections").
					WithHeaders("Conn ID", "Lease ID", "Server ID", "Remote Addr", "Connected At")
				for _, c := range resp.Connections {
					t.AddRow(c.ConnId, c.LeaseId, c.ServerId, c.RemoteAddr, c.ConnectedAt)
				}
				d.printer.Println(t.Render())
			}
			return nil
		},
	}
}
