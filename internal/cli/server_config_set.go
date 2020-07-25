package cli

import (
	"strings"

	"github.com/hashicorp/waypoint/internal/pkg/flag"
	pb "github.com/hashicorp/waypoint/internal/server/gen"
	"github.com/hashicorp/waypoint/sdk/terminal"
	"github.com/posener/complete"
)

type ServerConfigSetCommand struct {
	*baseCommand

	flagAdvertiseAddr pb.ServerConfig_AdvertiseAddr
}

func (c *ServerConfigSetCommand) Run(args []string) int {
	// Initialize. If we fail, we just exit since Init handles the UI.
	if err := c.Init(
		WithArgs(args),
		WithFlags(c.Flags()),
	); err != nil {
		return 1
	}

	cfg := &pb.ServerConfig{
		AdvertiseAddrs: []*pb.ServerConfig_AdvertiseAddr{
			&c.flagAdvertiseAddr,
		},
	}

	client := c.project.Client()
	_, err := client.SetServerConfig(c.Ctx, &pb.SetServerConfigRequest{
		Config: cfg,
	})
	if err != nil {
		c.ui.Output(err.Error(), terminal.WithErrorStyle())
		return 1
	}

	c.ui.Output("Server configuration set!", terminal.WithSuccessStyle())
	return 0
}

func (c *ServerConfigSetCommand) Flags() *flag.Sets {
	return c.flagSet(0, func(set *flag.Sets) {
		f := set.NewSet("Command Options")
		f.StringVar(&flag.StringVar{
			Name:   "advertise-addr",
			Target: &c.flagAdvertiseAddr.Addr,
			Usage: "Address to advertise for the server. This is used by the entrypoints\n" +
				"binaries to communicate back to the server. If this is blank, then\n" +
				"the entrypoints will not communicate to the server. Features such as\n" +
				"logs, exec, etc. will not work.",
		})
		f.BoolVar(&flag.BoolVar{
			Name:   "advertise-insecure",
			Target: &c.flagAdvertiseAddr.Insecure,
			Usage:  "If true, the advertised address should be connected to without TLS.",
		})
	})
}

func (c *ServerConfigSetCommand) AutocompleteArgs() complete.Predictor {
	return complete.PredictNothing
}

func (c *ServerConfigSetCommand) AutocompleteFlags() complete.Flags {
	return c.Flags().Completions()
}

func (c *ServerConfigSetCommand) Synopsis() string {
	return "Set the server online configuration."
}

func (c *ServerConfigSetCommand) Help() string {
	helpText := `
Usage: waypoint server config-set [options]

  Set the online configuration for a running Waypoint server.

  The configuration that can be set here is different from the configuration
  given via the startup file. This configuration is persisted in the server
  database.

`

	return strings.TrimSpace(helpText)
}