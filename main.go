package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"compumed/report-service/api"
	"compumed/report-service/reports"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	// Initialize structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("Starting Compumed Report Service")

	// Load configuration
	config := loadConfig()

	// Initialize database pool
	pool, err := pgxpool.New(context.Background(), config.DatabaseURL)
	if err != nil {
		slog.Error("Failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err != nil {
		slog.Error("Failed to ping database", "error", err)
		os.Exit(1)
	}

	// Initialize report store
	store := reports.NewStore(pool)

	// Start HTTP API server
	apiServer := api.NewServer(":8080", store)
	go func() {
		if err := apiServer.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server error", "error", err)
		}
	}()

	slog.Info("Report service started successfully")

	// Wait for shutdown signal
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	slog.Info("Shutting down report service")

	apiServer.Shutdown(context.Background())
	slog.Info("Report service stopped")
}

type Config struct {
	DatabaseURL string
}

func loadConfig() Config {
	return Config{
		DatabaseURL: getEnv("DATABASE_URL", "postgres://admin:pa55word@postgres:5432/db"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
