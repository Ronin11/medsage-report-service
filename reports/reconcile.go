package reports

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// reconcileHourUTC is the hour at which the daily reconciliation job runs.
// Set to 02:00 UTC so any in-flight events from the previous UTC day have
// landed in the events table before we recompute.
const reconcileHourUTC = 2

// Reconciler rebuilds daily rollup rows from the canonical events table,
// healing any drift introduced by at-least-once streaming delivery.
type Reconciler struct {
	pool *pgxpool.Pool
}

func NewReconciler(pool *pgxpool.Pool) *Reconciler {
	return &Reconciler{pool: pool}
}

// Run blocks until ctx is cancelled, recomputing the prior UTC day at
// reconcileHourUTC each day. It also runs once on startup to backfill any
// missed window from a previous shutdown.
func (r *Reconciler) Run(ctx context.Context) {
	if err := r.reconcileYesterday(ctx); err != nil {
		slog.Error("Initial reconcile failed", "error", err)
	}

	for {
		next := nextReconcileAt(time.Now().UTC())
		wait := time.Until(next)
		slog.Info("Next reconcile scheduled", "at", next.Format(time.RFC3339), "in", wait.String())

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		if err := r.reconcileYesterday(ctx); err != nil {
			slog.Error("Reconcile failed", "error", err)
		}
	}
}

func (r *Reconciler) reconcileYesterday(ctx context.Context) error {
	day := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	return r.ReconcileDay(ctx, day)
}

// ReconcileDay recomputes daily_adherence for the given UTC day from events.
// Exposed for backfills.
func (r *Reconciler) ReconcileDay(ctx context.Context, day string) error {
	start := time.Now()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM daily_adherence WHERE day = $1`, day); err != nil {
		return fmt.Errorf("delete day: %w", err)
	}

	res, err := tx.Exec(ctx, `
		INSERT INTO daily_adherence (device_id, day, dispensed, missed, confirmed, updated_at)
		SELECT
			stream_id,
			$1::date,
			COUNT(*) FILTER (WHERE event_type = 'medication_dispensed'),
			COUNT(*) FILTER (WHERE event_type = 'medication_missed'),
			COUNT(*) FILTER (WHERE event_type = 'medication_confirmed'),
			NOW()
		FROM events
		WHERE event_type IN ('medication_dispensed', 'medication_missed', 'medication_confirmed')
		  AND (created_at AT TIME ZONE 'UTC')::date = $1::date
		GROUP BY stream_id
	`, day)
	if err != nil {
		return fmt.Errorf("insert recomputed: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	slog.Info("Reconciled daily_adherence",
		"day", day,
		"rows", res.RowsAffected(),
		"duration", time.Since(start).String(),
	)
	return nil
}

// nextReconcileAt returns the next reconcileHourUTC instant strictly after now.
func nextReconcileAt(now time.Time) time.Time {
	candidate := time.Date(now.Year(), now.Month(), now.Day(), reconcileHourUTC, 0, 0, 0, time.UTC)
	if !candidate.After(now) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}
