package reports

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store provides read-only queries against the event store and audit log for reporting.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// --- Adherence Report ---

// AdherenceRow represents one day's adherence data for a device.
type AdherenceRow struct {
	Date      string `json:"date"`       // YYYY-MM-DD
	Dispensed int    `json:"dispensed"`
	Missed    int    `json:"missed"`
	Confirmed int    `json:"confirmed"`
}

// AdherenceReport is the full adherence summary for a device over a date range.
type AdherenceReport struct {
	DeviceID       uuid.UUID      `json:"device_id"`
	From           time.Time      `json:"from"`
	To             time.Time      `json:"to"`
	TotalDispensed int            `json:"total_dispensed"`
	TotalMissed    int            `json:"total_missed"`
	TotalConfirmed int            `json:"total_confirmed"`
	AdherenceRate  float64        `json:"adherence_rate"` // dispensed / (dispensed + missed)
	Daily          []AdherenceRow `json:"daily"`
}

func (s *Store) GetAdherenceReport(ctx context.Context, deviceID uuid.UUID, from, to time.Time) (*AdherenceReport, error) {
	query := `
		SELECT
			(created_at AT TIME ZONE 'UTC')::date AS day,
			COUNT(*) FILTER (WHERE event_type = 'medication_dispensed') AS dispensed,
			COUNT(*) FILTER (WHERE event_type = 'medication_missed')    AS missed,
			COUNT(*) FILTER (WHERE event_type = 'medication_confirmed') AS confirmed
		FROM events
		WHERE stream_id = $1
		  AND event_type IN ('medication_dispensed', 'medication_missed', 'medication_confirmed')
		  AND created_at >= $2
		  AND created_at < $3
		GROUP BY day
		ORDER BY day
	`

	rows, err := s.pool.Query(ctx, query, deviceID, from, to)
	if err != nil {
		return nil, fmt.Errorf("adherence query: %w", err)
	}
	defer rows.Close()

	report := &AdherenceReport{
		DeviceID: deviceID,
		From:     from,
		To:       to,
	}

	for rows.Next() {
		var row AdherenceRow
		var day time.Time
		if err := rows.Scan(&day, &row.Dispensed, &row.Missed, &row.Confirmed); err != nil {
			return nil, fmt.Errorf("scan adherence row: %w", err)
		}
		row.Date = day.Format("2006-01-02")
		report.TotalDispensed += row.Dispensed
		report.TotalMissed += row.Missed
		report.TotalConfirmed += row.Confirmed
		report.Daily = append(report.Daily, row)
	}

	total := report.TotalDispensed + report.TotalMissed
	if total > 0 {
		report.AdherenceRate = float64(report.TotalDispensed) / float64(total)
	}

	if report.Daily == nil {
		report.Daily = []AdherenceRow{}
	}

	return report, nil
}

// --- Device Activity Report ---

// ActivityRow represents one day's activity counts for a device.
type ActivityRow struct {
	Date         string `json:"date"`
	StatusEvents int    `json:"status_events"`
	Alerts       int    `json:"alerts"`
	TotalEvents  int    `json:"total_events"`
}

// ActivityReport summarises device activity over a date range.
type ActivityReport struct {
	DeviceID    uuid.UUID     `json:"device_id"`
	From        time.Time     `json:"from"`
	To          time.Time     `json:"to"`
	TotalEvents int           `json:"total_events"`
	TotalAlerts int           `json:"total_alerts"`
	Daily       []ActivityRow `json:"daily"`
}

func (s *Store) GetActivityReport(ctx context.Context, deviceID uuid.UUID, from, to time.Time) (*ActivityReport, error) {
	query := `
		SELECT
			(created_at AT TIME ZONE 'UTC')::date AS day,
			COUNT(*) FILTER (WHERE event_type = 'device_status') AS status_events,
			COUNT(*) FILTER (WHERE event_type = 'alert')         AS alerts,
			COUNT(*)                                              AS total
		FROM events
		WHERE stream_id = $1
		  AND created_at >= $2
		  AND created_at < $3
		GROUP BY day
		ORDER BY day
	`

	rows, err := s.pool.Query(ctx, query, deviceID, from, to)
	if err != nil {
		return nil, fmt.Errorf("activity query: %w", err)
	}
	defer rows.Close()

	report := &ActivityReport{
		DeviceID: deviceID,
		From:     from,
		To:       to,
	}

	for rows.Next() {
		var row ActivityRow
		var day time.Time
		if err := rows.Scan(&day, &row.StatusEvents, &row.Alerts, &row.TotalEvents); err != nil {
			return nil, fmt.Errorf("scan activity row: %w", err)
		}
		row.Date = day.Format("2006-01-02")
		report.TotalEvents += row.TotalEvents
		report.TotalAlerts += row.Alerts
		report.Daily = append(report.Daily, row)
	}

	if report.Daily == nil {
		report.Daily = []ActivityRow{}
	}

	return report, nil
}

// --- Audit Log Report ---

// AuditRow represents a single audit log entry for export.
type AuditRow struct {
	ID           uuid.UUID              `json:"id"`
	EventID      *uuid.UUID             `json:"event_id,omitempty"`
	ActorID      uuid.UUID              `json:"actor_id"`
	ActorType    string                 `json:"actor_type"`
	Action       string                 `json:"action"`
	ResourceType string                 `json:"resource_type"`
	ResourceID   *uuid.UUID             `json:"resource_id,omitempty"`
	IPAddress    string                 `json:"ip_address"`
	Timestamp    time.Time              `json:"timestamp"`
	Details      map[string]interface{} `json:"details,omitempty"`
}

// AuditReport contains audit log entries for a time range, optionally filtered by actor.
type AuditReport struct {
	From    time.Time  `json:"from"`
	To      time.Time  `json:"to"`
	ActorID *uuid.UUID `json:"actor_id,omitempty"`
	Total   int        `json:"total"`
	Entries []AuditRow `json:"entries"`
}

func (s *Store) GetAuditReport(ctx context.Context, from, to time.Time, actorID *uuid.UUID, limit int) (*AuditReport, error) {
	report := &AuditReport{
		From:    from,
		To:      to,
		ActorID: actorID,
	}

	var query string
	var args []interface{}

	if actorID != nil {
		query = `
			SELECT id, event_id, actor_id, actor_type, action, resource_type, resource_id, ip_address, timestamp, details
			FROM audit_log
			WHERE timestamp >= $1 AND timestamp < $2 AND actor_id = $3
			ORDER BY timestamp ASC
			LIMIT $4
		`
		args = []interface{}{from, to, *actorID, limit}
	} else {
		query = `
			SELECT id, event_id, actor_id, actor_type, action, resource_type, resource_id, ip_address, timestamp, details
			FROM audit_log
			WHERE timestamp >= $1 AND timestamp < $2
			ORDER BY timestamp ASC
			LIMIT $3
		`
		args = []interface{}{from, to, limit}
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("audit query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var row AuditRow
		var detailsJSON []byte
		if err := rows.Scan(
			&row.ID, &row.EventID, &row.ActorID, &row.ActorType,
			&row.Action, &row.ResourceType, &row.ResourceID,
			&row.IPAddress, &row.Timestamp, &detailsJSON,
		); err != nil {
			return nil, fmt.Errorf("scan audit row: %w", err)
		}
		if detailsJSON != nil {
			json.Unmarshal(detailsJSON, &row.Details)
		}
		report.Entries = append(report.Entries, row)
	}

	if report.Entries == nil {
		report.Entries = []AuditRow{}
	}
	report.Total = len(report.Entries)

	return report, nil
}

// --- Event Summary ---

// EventSummary shows count of each event type over a period.
type EventSummary struct {
	EventType string `json:"event_type"`
	Count     int    `json:"count"`
}

func (s *Store) GetEventSummary(ctx context.Context, deviceID uuid.UUID, from, to time.Time) ([]EventSummary, error) {
	query := `
		SELECT event_type, COUNT(*) AS cnt
		FROM events
		WHERE stream_id = $1
		  AND created_at >= $2
		  AND created_at < $3
		GROUP BY event_type
		ORDER BY cnt DESC
	`

	rows, err := s.pool.Query(ctx, query, deviceID, from, to)
	if err != nil {
		return nil, fmt.Errorf("event summary query: %w", err)
	}
	defer rows.Close()

	var results []EventSummary
	for rows.Next() {
		var s EventSummary
		if err := rows.Scan(&s.EventType, &s.Count); err != nil {
			return nil, fmt.Errorf("scan event summary: %w", err)
		}
		results = append(results, s)
	}

	if results == nil {
		results = []EventSummary{}
	}

	return results, nil
}
