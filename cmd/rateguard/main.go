// Command rateguard runs the RateGuard rate-limiting service.
//
// With no arguments it serves HTTP until SIGINT or SIGTERM. The migrate
// subcommand applies or reverts the schema migrations embedded in the
// binary:
//
//	rateguard migrate up         apply every pending migration
//	rateguard migrate down [N]   revert the last N migrations (default 1)
//	rateguard migrate version    print the current schema version
//
// main is the composition root: it loads configuration, constructs
// dependencies and wires them together. It holds no business logic.
package main

import (
	"fmt"
	"log/slog"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("rateguard exited", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return serve()
	}
	if args[0] == "migrate" {
		cmd, err := parseMigrateArgs(args[1:])
		if err != nil {
			return err
		}
		return runMigrate(cmd)
	}
	return fmt.Errorf("unknown command %q: run with no arguments to serve, or use %q", args[0], migrateUsage)
}
