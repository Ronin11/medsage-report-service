package reports

import (
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
		return fmt.Errorf("upsert daily_adherence: %w", err)
	}

	slog.Debug("Adherence event projected",
		"device_id", deviceID,
		"day", day,
		"column", column,
	)
	return nil
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
