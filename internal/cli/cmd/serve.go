package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// serveFlags holds flags that only make sense for the Web UI backend.
// We keep them in a separate struct so the root flag set stays small and
// REPL users don't see web-only options.
type serveFlags struct {
	host string
	port int
}

// newServeCmd implements `mini-agent serve`. Real gin wiring lands in
// T5.4; this is the skeleton that documents the contract and exposes
// --host / --port so the help text is final.
func newServeCmd(_ *rootFlags) *cobra.Command {
	sf := &serveFlags{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Web UI backend (gin REST + SSE)",
		Long: `Start an HTTP server that exposes the Web UI's REST + SSE API.
The Web UI implements the same uio.Sink / uio.Prompter contract as the
CLI, so behavior matches the REPL.

Default bind address: 127.0.0.1:7777 — change with --host / --port or via
the ` + "`web:`" + ` block in config.yaml.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(),
				"mini-agent serve skeleton — gin server pending T5.4 (would bind %s:%d).\n",
				sf.host, sf.port)
			return fmt.Errorf("serve not yet implemented (T5.4)")
		},
	}

	cmd.Flags().StringVar(&sf.host, "host", "127.0.0.1",
		"bind host for the Web UI backend")
	cmd.Flags().IntVar(&sf.port, "port", 7777,
		"bind port for the Web UI backend")
	return cmd
}
