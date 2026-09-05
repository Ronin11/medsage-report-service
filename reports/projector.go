package reports

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"context"
	"fmt"
	"log/slog"
	"time"

	eventsv1 "medsage/proto/medsage/events/v1"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Projector applies incoming DeviceEvents to daily rollup tables.
//
// At-least-once delivery means a counter increment can occur twice on
// crash-before-ack. The nightly reconciliation job (see Reconciler) overwrites
// each prior UTC day from the events table, healing any drift.
type Projector struct {
	pool *pgxpool.Pool
}

func NewProjector(pool *pgxpool.Pool) *Projector {
	return &Projector{pool: pool}
}

// Handle is the EventHandler entry point used by the NATS subscriber.
func (p *Projector) Handle(ctx context.Context, evt *eventsv1.DeviceEvent) error {
	switch evt.EventType {
	case eventsv1.EventType_EVENT_TYPE_MEDICATION_DISPENSED,
		eventsv1.EventType_EVENT_TYPE_MEDICATION_MISSED,
		eventsv1.EventType_EVENT_TYPE_MEDICATION_CONFIRMED:
		return p.applyAdherence(ctx, evt)
	}
	return nil
}

func (p *Projector) applyAdherence(ctx context.Context, evt *eventsv1.DeviceEvent) error {
	deviceID, err := uuid.Parse(evt.DeviceId)
	if err != nil {
		return fmt.Errorf("invalid device_id %q: %w", evt.DeviceId, err)
	}

	column := adherenceColumn(evt.EventType)
	if column == "" {
		return nil
	}

	// Bucket by server-arrival UTC day. Reconciliation later corrects against
	// events.created_at if these ever disagree at a midnight boundary.
	day := time.Now().UTC().Format("2006-01-02")

	query := fmt.Sprintf(`
		INSERT INTO daily_adherence (device_id, day, %[1]s, updated_at)
		VALUES ($1, $2, 1, NOW())
		ON CONFLICT (device_id, day) DO UPDATE
		SET %[1]s = daily_adherence.%[1]s + 1,
		    updated_at = NOW()
	`, column)

	if _, err := p.pool.Exec(ctx, query, deviceID, day); err != nil {
		// daily_adherence.device_id references devices. A device publishing
		// under an id that was never registered — a bench unit re-provisioned
		// under a new UUID still emits under its old one — therefore fails the
		// foreign key. Returning the error would leave the event to be
		// redelivered forever, so it is dropped from the rollup and logged
		// loudly: it cannot be attributed to anyone, but silently discarding
		// medication events is not acceptable either. The reconciler skips the
		// same rows for the same reason.
		if isUnknownDeviceFK(err) {
			slog.Warn("Adherence event from an unregistered device; not projected",
				"device_id", deviceID,
				"day", day,
				"column", column,
				"hint", "register the device, or reconcile the id it publishes under",
			)
			return nil
		}
		return fmt.Errorf("upsert daily_adherence: %w", err)
	}

	slog.Debug("Adherence event projected",
		"device_id", deviceID,
		"day", day,
		"column", column,
	)
	return nil
}

// isUnknownDeviceFK reports whether an error is the daily_adherence foreign key
// rejecting an unknown device, as opposed to any other database failure — which
// must still surface.
func isUnknownDeviceFK(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23503" && pgErr.ConstraintName == "daily_adherence_device_id_fkey"
}

func adherenceColumn(t eventsv1.EventType) string {
	switch t {
	case eventsv1.EventType_EVENT_TYPE_MEDICATION_DISPENSED:
		return "dispensed"
	case eventsv1.EventType_EVENT_TYPE_MEDICATION_MISSED:
		return "missed"
	case eventsv1.EventType_EVENT_TYPE_MEDICATION_CONFIRMED:
		return "confirmed"
	}
	return ""
}
