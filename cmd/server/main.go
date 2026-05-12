package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"visual-assistant/internal/assistant"
	"visual-assistant/internal/httpapi"
	"visual-assistant/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		log.Fatal(err)
	}
}

func run(logger *slog.Logger) error {
	cfg := loadConfig()

	db, err := sql.Open("pgx", cfg.databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	pgStore := store.NewPostgres(db)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.startupTimeout)
	defer cancel()
	if err := waitForStore(ctx, pgStore); err != nil {
		return err
	}

	handler := httpapi.NewServer(pgStore, assistant.NewMock(), logger).Handler()
	server := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}

	// Docker sends SIGTERM during `docker compose down`; os.Interrupt handles
	// local Ctrl+C. In both cases, give in-flight requests time to finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("server_shutdown_failed", "error", err)
		}
	}()

	logger.Info("server_listening", "addr", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type config struct {
	port           string
	databaseURL    string
	startupTimeout time.Duration
}

func loadConfig() config {
	return config{
		port:           envOrDefault("PORT", "8080"),
		databaseURL:    envOrDefault("DATABASE_URL", "postgres://visual:visual@localhost:5432/visual_assistant?sslmode=disable"),
		startupTimeout: 45 * time.Second,
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func waitForStore(ctx context.Context, pgStore *store.Postgres) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	var lastErr error
	for {
		if err := pgStore.Ping(ctx); err != nil {
			lastErr = err
		} else if err := pgStore.Migrate(ctx); err != nil {
			lastErr = err
		} else {
			return nil
		}

		select {
		case <-ctx.Done():
			return lastErr
		case <-ticker.C:
		}
	}
}
