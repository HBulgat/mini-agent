package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/HBulgat/mini-agent/internal/session/store"
)

// newMigrateCmd implements `mini-agent migrate`.
//
// Per D15 every process startup will eventually run migrate.Up()
// implicitly (when the bootstrap composer wires up the agent in T1.8);
// this subcommand exists so operators can run it standalone for
// troubleshooting and so the help text discoverably documents the
// behavior.
func newMigrateCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Apply pending SQLite schema migrations",
		Long: `Run all pending migrations against the SQLite database (default
~/.mini-agent/data.db). The same migrations also run automatically on
every process startup; you only need this subcommand for explicit,
isolated runs (e.g. during deployment or to confirm the schema is
current after pulling a new build).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := flags.cfg
			if cfg == nil {
				return fmt.Errorf("migrate: configuration not loaded")
			}

			path := cfg.Storage.DatabasePath
			if path == "" {
				return fmt.Errorf("migrate: storage.database_path is empty")
			}

			out := cmd.OutOrStdout()
			if err := store.EnsureDir(path); err != nil {
				return fmt.Errorf("migrate: ensure parent dir of %s: %w", path, err)
			}

			db, err := store.Open(path)
			if err != nil {
				return err
			}
			defer func() { _ = db.Close() }()

			pre, err := store.Migrate(db)
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "mini-agent migrate: %s — schema version was %d (now current)\n",
				path, pre)
			return nil
		},
	}
}
