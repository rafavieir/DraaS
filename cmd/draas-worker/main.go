package main

import (
	"context"
	"github.com/draas-platform/draas/internal/platform"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cfg, err := platform.LoadConfig()
	if err != nil {
		slog.Error("configuration", "error", err)
		os.Exit(1)
	}
	startup, stop := context.WithTimeout(ctx, 45*time.Second)
	app, err := platform.Open(startup, cfg)
	stop()
	if err != nil {
		slog.Error("startup", "error", err)
		os.Exit(1)
	}
	defer app.Close()
	go app.OutboxLoop(ctx)
	if err = app.RunWorker(ctx); err != nil {
		slog.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}
