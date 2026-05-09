package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"medsage/authkit"
	"medsage/report-service/api"
	natsbus "medsage/report-service/nats"
	"medsage/report-service/reports"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("Starting Medsage Report Service")

	config := loadConfig()

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

	store := reports.NewStore(pool)
	projector := reports.NewProjector(pool)
	reconciler := reports.NewReconciler(pool)

	publisher, err := natsbus.ConnectPublisher(config.NATSURL)
	if err != nil {
		slog.Warn("Commands publisher unavailable at startup; scheduler disabled until reconnect", "error", err)
	}
	defer func() {
		if publisher != nil {
			publisher.Close()
		}
	}()

	verifier := authkit.NewVerifier(config.KeycloakURL, config.KeycloakRealm)
	authMW := verifier.Middleware(authkit.MiddlewareOptions{
		DevTrustHeader: devTrustHeader(config.ExecEnv),
	})

	apiServer := api.NewServer(":8080", store, pool, authMW)
	go func() {
		if err := apiServer.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server error", "error", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go runReconciler(ctx, reconciler)
	go runProjector(ctx, config.NATSURL, projector)
	go runScheduler(ctx, pool, store, config.NATSURL, publisher)

	slog.Info("Report service started successfully")

	<-ctx.Done()
	slog.Info("Shutting down report service")

	apiServer.Shutdown(context.Background())
	slog.Info("Report service stopped")
}

func runReconciler(ctx context.Context, r *reports.Reconciler) {
	r.Run(ctx)
}

// runScheduler runs the recurring report delivery loop. If the initial NATS
// publisher connection failed at startup, it retries until one is available
// and then hands it to the scheduler.
func runScheduler(ctx context.Context, pool *pgxpool.Pool, store *reports.Store, natsURL string, pub *natsbus.Publisher) {
	for pub == nil {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		p, err := natsbus.ConnectPublisher(natsURL)
		if err != nil {
			slog.Warn("Scheduler waiting on NATS publisher", "error", err)
			continue
		}
		pub = p
	}

	scheduler := reports.NewScheduler(pool, store, pub)
	scheduler.Run(ctx)
}

func runProjector(ctx context.Context, natsURL string, p *reports.Projector) {
	subjects := []string{"medsage.events.medication.>"}

	for {
		sub, err := natsbus.Connect(natsURL, subjects)
		if err != nil {
			slog.Warn("NATS not available, retrying in 5s", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		slog.Info("NATS event projector started")
		if err := sub.Start(p.Handle); err != nil {
			slog.Error("NATS subscriber error", "error", err)
		}
		sub.Close()

		select {
		case <-ctx.Done():
			return
		default:
			slog.Warn("NATS subscriber stopped, reconnecting in 5s")
			time.Sleep(5 * time.Second)
		}
	}
}

type Config struct {
	DatabaseURL   string
	NATSURL       string
	KeycloakURL   string
	KeycloakRealm string
	ExecEnv       string
}

func loadConfig() Config {
	return Config{
		DatabaseURL:   getEnv("DATABASE_URL", "postgres://admin:pa55word@postgres:5432/db"),
		NATSURL:       getEnv("NATS_URL", "nats://nats:4222"),
		KeycloakURL:   getEnv("KEYCLOAK_URL", "http://keycloak:8080"),
		KeycloakRealm: getEnv("KEYCLOAK_REALM_NAME", "medsage"),
		ExecEnv:       getEnv("EXEC_ENV", ""),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// devTrustHeader returns the header name that the auth middleware should
// trust for unauthenticated dev iteration. Empty in any non-DEV environment.
func devTrustHeader(execEnv string) string {
	if execEnv == "DEV" {
		return "X-User-Id"
	}
	return ""
}
