package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/Rimaz17/RateGuard/internal/config"
	"github.com/Rimaz17/RateGuard/internal/storage/postgres"
)

const migrateUsage = "migrate up | migrate down [N] | migrate version"

// migrateCmd is a parsed migrate subcommand.
type migrateCmd struct {
	action string // "up", "down" or "version"
	steps  int    // for "down" only
}

// parseMigrateArgs validates the arguments before anything connects to the
// database, so a typo never reaches the schema.
func parseMigrateArgs(args []string) (migrateCmd, error) {
	if len(args) == 0 {
		return migrateCmd{}, fmt.Errorf("missing migrate action: %s", migrateUsage)
	}
	switch action := args[0]; action {
	case "up", "version":
		if len(args) != 1 {
			return migrateCmd{}, fmt.Errorf("migrate %s takes no arguments: %s", action, migrateUsage)
		}
		return migrateCmd{action: action}, nil
	case "down":
		switch len(args) {
		case 1:
			return migrateCmd{action: action, steps: 1}, nil
		case 2:
			steps, err := strconv.Atoi(args[1])
			if err != nil || steps < 1 {
				return migrateCmd{}, fmt.Errorf("migrate down: N must be a positive integer, got %q", args[1])
			}
			return migrateCmd{action: action, steps: steps}, nil
		default:
			return migrateCmd{}, fmt.Errorf("migrate down takes at most one argument: %s", migrateUsage)
		}
	default:
		return migrateCmd{}, fmt.Errorf("unknown migrate action %q: %s", action, migrateUsage)
	}
}

// runMigrate applies cmd and logs the resulting schema version. It needs
// only the database settings, so the migrate job's environment can be
// minimal.
func runMigrate(cmd migrateCmd) (err error) {
	cfg, err := config.LoadDatabase()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mg, err := postgres.NewMigrator(ctx, cfg.URL.Value(), logger)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := mg.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing migrator: %w", cerr))
		}
	}()

	switch cmd.action {
	case "up":
		err = mg.Up(ctx)
	case "down":
		err = mg.Down(ctx, cmd.steps)
	}
	if err != nil {
		return err
	}

	version, dirty, err := mg.Version()
	if err != nil {
		return err
	}
	logger.Info("schema version", slog.Uint64("version", uint64(version)), slog.Bool("dirty", dirty))
	return nil
}
