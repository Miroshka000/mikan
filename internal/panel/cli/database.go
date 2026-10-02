package cli

import (
	"context"
	"errors"
	"fmt"

	"mikan/internal/panel/config"
	"mikan/internal/panel/store"
)

// databaseCmd answers the database commands of the 0.5 installer on this SQLite release,
// for a server whose installer is already newer than its panel: there is no PostgreSQL to
// migrate yet, and a backup is the consistent SQLite copy admin backup makes.
func databaseCmd(ctx context.Context, args []string) error {
	switch {
	case len(args) == 1 && args[0] == "migrate":
		fmt.Println("SQLite release: nothing to migrate")
		return nil
	case len(args) == 2 && args[0] == "backup":
		cfg, err := config.FromEnv()
		if err != nil {
			return err
		}
		st, err := store.Open(ctx, cfg.DataDir)
		if err != nil {
			return err
		}
		defer st.Close()
		if err := backup(ctx, st, args[1]); err != nil {
			return err
		}
		fmt.Println("Database copied to", args[1])
		return nil
	}
	return errors.New("usage: mikan database migrate | database backup FILE")
}
