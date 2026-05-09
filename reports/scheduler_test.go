package reports

import (
	"strings"
	"testing"
	"time"
)

func TestWindowFor(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		cadence   string
		wantFromD int // days before now
	}{
		{"weekly", 7},
		{"monthly", 30}, // approximate; verified via AddDate below
		{"unknown", 7},  // default falls through to weekly window
		{"", 7},
	}
	for _, tc := range tests {
		t.Run(tc.cadence, func(t *testing.T) {
			from, to := windowFor(tc.cadence, now)
			if !to.Equal(now) {
				t.Errorf("to = %v, want %v", to, now)
			}
			var want time.Time
			if tc.cadence == "monthly" {
				want = now.AddDate(0, -1, 0)
			} else {
				want = now.AddDate(0, 0, -tc.wantFromD)
			}
			if !from.Equal(want) {
				t.Errorf("from = %v, want %v", from, want)
			}
		})
	}
}

func TestPgIntervalFor(t *testing.T) {
	tests := []struct {
		cadence string
		want    string
		wantErr bool
	}{
		{"weekly", "7 days", false},
		{"monthly", "1 month", false},
		{"daily", "", true},
		{"", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.cadence, func(t *testing.T) {
			got, err := pgIntervalFor(tc.cadence)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr=%v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRenderAdherenceHTML(t *testing.T) {
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)

	t.Run("happy path with daily rows", func(t *testing.T) {
		report := &AdherenceReport{
			From:           from,
			To:             to,
			TotalDispensed: 14,
			TotalMissed:    1,
			TotalConfirmed: 14,
			AdherenceRate:  0.9333,
			Daily: []AdherenceRow{
				{Date: "2026-05-01", Dispensed: 2, Missed: 0, Confirmed: 2},
				{Date: "2026-05-02", Dispensed: 2, Missed: 1, Confirmed: 2},
			},
		}

		html, err := renderAdherenceHTML(report, "weekly")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Adherence percent rounded to whole number.
		if !strings.Contains(html, "93%") {
			t.Errorf("expected rendered percent 93%%, got: %s", html)
		}
		// Cadence appears in subject line.
		if !strings.Contains(html, "weekly") {
			t.Error("expected cadence in output")
		}
		// Date labels: from is rendered as-is, to is rendered as (to - 1 day).
		if !strings.Contains(html, "May 1, 2026") {
			t.Error("expected from-label May 1, 2026")
		}
		if !strings.Contains(html, "May 7, 2026") {
			t.Error("expected to-label May 7, 2026 (to minus one day)")
		}
		// Daily rows rendered.
		if !strings.Contains(html, "2026-05-01") {
			t.Error("expected daily row date")
		}
	})

	t.Run("empty daily renders no-data message", func(t *testing.T) {
		report := &AdherenceReport{From: from, To: to, Daily: []AdherenceRow{}}
		html, err := renderAdherenceHTML(report, "monthly")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(html, "No medication events recorded") {
			t.Error("expected no-events fallback copy")
		}
		if strings.Contains(html, "<table") {
			t.Error("table should not be rendered when Daily is empty")
		}
	})

	t.Run("zero adherence rate", func(t *testing.T) {
		report := &AdherenceReport{From: from, To: to, AdherenceRate: 0}
		html, err := renderAdherenceHTML(report, "weekly")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(html, "0%") {
			t.Error("expected 0% rendered")
		}
	})
}
