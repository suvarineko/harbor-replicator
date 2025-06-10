package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"harbor-replicator/pkg/config"
	"harbor-replicator/pkg/logger"
)

var (
	configPath = flag.String("config", "configs/config.yaml", "Path to configuration file")
	version    = flag.Bool("version", false, "Show version information")
)

func main() {
	flag.Parse()

	if *version {
		fmt.Println("Harbor Replicator v1.0.0")
		return
	}

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	log, err := logger.NewLogger(&cfg.Logging)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync()

	log.Info("Starting Harbor Replicator", 
		"version", "1.0.0",
		"config", *configPath,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Info("Received shutdown signal")
		cancel()
	}()

	log.Info("Harbor Replicator started successfully")

	<-ctx.Done()
	log.Info("Harbor Replicator shutdown complete")
}