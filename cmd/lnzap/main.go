package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lnzap/pkg/config"
	"lnzap/pkg/lnurl"
	"lnzap/pkg/server"
	"lnzap/pkg/splitkit"
	"lnzap/pkg/zap"
)

func main() {
	configPath := flag.String("config", "", "path to YAML config (or LNZAP_CONFIG / LNZAP_CONFIG_YAML)")
	flag.Parse()

	var (
		cfg *config.Config
		err error
	)
	if *configPath != "" {
		cfg, err = config.Load(*configPath)
	} else {
		cfg, err = config.LoadFromEnv()
	}
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	privateKey, err := zap.ParsePrivateKey(os.Getenv("NOSTR_PRIVATE_KEY"))
	if err != nil {
		slog.Error("NOSTR_PRIVATE_KEY", "err", err)
		os.Exit(1)
	}

	client := lnurl.NewClient(cfg.CacheTTL.Std(), nil)
	watcher := splitkit.NewWatcher(cfg.SplitKitHost, cfg.EventGUIDs(), client.HTTP())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	watcher.Start(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(cfg, client, privateKey, watcher),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shut); err != nil {
			slog.Error("shutdown", "err", err)
		}
	}()

	slog.Info("listening", "addr", cfg.Listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}
