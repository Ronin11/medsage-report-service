package reports

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWriteAdherenceCSV(t *testing.T) {
	t.Run("with daily rows", func(t *testing.T) {
		report := &AdherenceReport{
			TotalDispensed: 5,
			TotalMissed:    1,
			TotalConfirmed: 4,
			Daily: []AdherenceRow{
				{Date: "2026-05-01", Dispensed: 3, Missed: 0, Confirmed: 3},
				{Date: "2026-05-02", Dispensed: 2, Missed: 1, Confirmed: 1},
			},
		}

		var buf bytes.Buffer
		if err := WriteAdherenceCSV(&buf, report); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		records := mustReadCSV(t, buf.Bytes())
		if len(records) != 4 {
			t.Fatalf("expected 4 rows (header + 2 daily + total), got %d", len(records))
		}
		want := [][]string{
			{"date", "dispensed", "missed", "confirmed"},
			{"2026-05-01", "3", "0", "3"},
			{"2026-05-02", "2", "1", "1"},
			{"TOTAL", "5", "1", "4"},
		}
		for i, row := range want {
			if !equalSlice(records[i], row) {
				t.Errorf("row %d: got %v, want %v", i, records[i], row)
			}
		}
	})

	t.Run("empty daily still emits header and total row", func(t *testing.T) {
		report := &AdherenceReport{Daily: []AdherenceRow{}}
		var buf bytes.Buffer
		if err := WriteAdherenceCSV(&buf, report); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		records := mustReadCSV(t, buf.Bytes())
		if len(records) != 2 {
			t.Fatalf("expected 2 rows (header + total), got %d", len(records))
		}
		if records[1][0] != "TOTAL" {
			t.Errorf("second row should be TOTAL, got %v", records[1])
		}
	})
}

func TestWriteAuditCSV(t *testing.T) {
	rid := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()
	actor := uuid.New()
	ts := time.Date(2026, 5, 8, 12, 30, 45, 0, time.UTC)

	report := &AuditReport{
		Entries: []AuditRow{
			{
				ID:           id1,
				ActorID:      actor,
				ActorType:    "user",
				Action:       "device.update",
				ResourceType: "device",
				ResourceID:   &rid,
				IPAddress:    "10.0.0.1",
				Timestamp:    ts,
			},
			{
				ID:           id2,
				ActorID:      actor,
				ActorType:    "user",
				Action:       "user.login",
				ResourceType: "user",
				ResourceID:   nil, // resource_id is optional
				IPAddress:    "10.0.0.2",
				Timestamp:    ts,
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteAuditCSV(&buf, report); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	records := mustReadCSV(t, buf.Bytes())
	if len(records) != 3 {
		t.Fatalf("expected 3 rows (header + 2 entries), got %d", len(records))
	}

	header := records[0]
	wantHeader := []string{"id", "timestamp", "actor_id", "actor_type", "action", "resource_type", "resource_id", "ip_address"}
	if !equalSlice(header, wantHeader) {
		t.Errorf("header: got %v, want %v", header, wantHeader)
	}

	// Timestamp formatted as RFC3339-ish (2006-01-02T15:04:05Z).
	if records[1][1] != "2026-05-08T12:30:45Z" {
		t.Errorf("timestamp formatting: got %q", records[1][1])
	}
	// Resource ID rendered when set.
	if records[1][6] != rid.String() {
		t.Errorf("resource_id: got %q, want %q", records[1][6], rid.String())
	}
	// Resource ID empty when nil.
	if records[2][6] != "" {
		t.Errorf("nil resource_id should render empty, got %q", records[2][6])
	}
}

func mustReadCSV(t *testing.T, data []byte) [][]string {
	t.Helper()
	r := csv.NewReader(strings.NewReader(string(data)))
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv parse failed: %v", err)
	}
	return rows
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
