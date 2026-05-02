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
