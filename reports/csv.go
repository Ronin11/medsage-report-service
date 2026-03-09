package reports

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
)

// WriteAdherenceCSV writes the adherence report as CSV to w.
func WriteAdherenceCSV(w io.Writer, report *AdherenceReport) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	// Header
	if err := cw.Write([]string{"date", "dispensed", "missed", "confirmed"}); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	for _, row := range report.Daily {
		if err := cw.Write([]string{
			row.Date,
			strconv.Itoa(row.Dispensed),
			strconv.Itoa(row.Missed),
			strconv.Itoa(row.Confirmed),
		}); err != nil {
			return fmt.Errorf("write row: %w", err)
		}
	}

	// Summary row
	if err := cw.Write([]string{
		"TOTAL",
		strconv.Itoa(report.TotalDispensed),
		strconv.Itoa(report.TotalMissed),
		strconv.Itoa(report.TotalConfirmed),
	}); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}

	return nil
}

// WriteActivityCSV writes the activity report as CSV to w.
func WriteActivityCSV(w io.Writer, report *ActivityReport) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	if err := cw.Write([]string{"date", "status_events", "alerts", "total_events"}); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	for _, row := range report.Daily {
		if err := cw.Write([]string{
			row.Date,
			strconv.Itoa(row.StatusEvents),
			strconv.Itoa(row.Alerts),
			strconv.Itoa(row.TotalEvents),
		}); err != nil {
			return fmt.Errorf("write row: %w", err)
		}
	}

	return nil
}

// WriteAuditCSV writes the audit report as CSV to w.
func WriteAuditCSV(w io.Writer, report *AuditReport) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	if err := cw.Write([]string{"id", "timestamp", "actor_id", "actor_type", "action", "resource_type", "resource_id", "ip_address"}); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	for _, row := range report.Entries {
		resourceID := ""
		if row.ResourceID != nil {
			resourceID = row.ResourceID.String()
		}
		if err := cw.Write([]string{
			row.ID.String(),
			row.Timestamp.Format("2006-01-02T15:04:05Z"),
			row.ActorID.String(),
			row.ActorType,
			row.Action,
			row.ResourceType,
			resourceID,
			row.IPAddress,
		}); err != nil {
			return fmt.Errorf("write row: %w", err)
		}
	}

	return nil
}

// WriteEventSummaryCSV writes the event summary as CSV to w.
func WriteEventSummaryCSV(w io.Writer, summary []EventSummary) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	if err := cw.Write([]string{"event_type", "count"}); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	for _, s := range summary {
		if err := cw.Write([]string{s.EventType, strconv.Itoa(s.Count)}); err != nil {
			return fmt.Errorf("write row: %w", err)
		}
	}

	return nil
}
