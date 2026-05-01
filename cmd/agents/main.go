package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/eloylp/agents/internal/daemon"
	"github.com/eloylp/agents/internal/logging"
	"github.com/eloylp/agents/internal/setup"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// "setup" is the only subcommand that runs without a config; handle it
	// before touching the database.
	if len(os.Args) > 1 && os.Args[1] == "setup" {
		dryRun := len(os.Args) > 2 && os.Args[2] == "--dry-run"
		return setup.Run(setup.NewCommandRunner(), dryRun, os.Stdin, os.Stdout, os.Stderr)
	}

	_ = godotenv.Load()

	dbPath := flag.String("db", "agents.db", "path to SQLite database file")
	importPath := flag.String("import", "", "YAML config file to import into the database")
	flag.Parse()

	cfg, st, err := daemon.LoadConfig(ctx, *dbPath, *importPath, os.Stderr)
	if err != nil {
		return err
	}
	defer st.Close()

	logger := logging.NewLogger(cfg.Daemon.Log)

	d, err := daemon.New(cfg, st, logger)
	if err != nil {
		return err
	}
	return d.Run(ctx)
}
