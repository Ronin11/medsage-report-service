package reports

import (
	"testing"
	"time"
)

func TestNextReconcileAt(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "before reconcile hour: same day",
			now:  time.Date(2026, 5, 8, 1, 30, 0, 0, time.UTC),
			want: time.Date(2026, 5, 8, 2, 0, 0, 0, time.UTC),
		},
		{
			name: "exactly at reconcile hour: next day",
			now:  time.Date(2026, 5, 8, 2, 0, 0, 0, time.UTC),
			want: time.Date(2026, 5, 9, 2, 0, 0, 0, time.UTC),
		},
		{
			name: "after reconcile hour: next day",
			now:  time.Date(2026, 5, 8, 3, 0, 0, 0, time.UTC),
			want: time.Date(2026, 5, 9, 2, 0, 0, 0, time.UTC),
		},
		{
			name: "month boundary",
			now:  time.Date(2026, 5, 31, 23, 30, 0, 0, time.UTC),
			want: time.Date(2026, 6, 1, 2, 0, 0, 0, time.UTC),
		},
		{
			name: "year boundary",
			now:  time.Date(2026, 12, 31, 23, 30, 0, 0, time.UTC),
			want: time.Date(2027, 1, 1, 2, 0, 0, 0, time.UTC),
		},
		{
			// Caller is responsible for passing UTC; we only verify behavior matches that contract.
			// PST 1:30 = UTC 9:30 → next 02:00 UTC is the following day.
			name: "non-UTC-converted input still produces UTC reconcile time",
			now:  time.Date(2026, 5, 8, 1, 30, 0, 0, time.FixedZone("PST", -8*3600)).UTC(),
			want: time.Date(2026, 5, 9, 2, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := nextReconcileAt(tc.now)
			if !got.Equal(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
			if !got.After(tc.now) {
				t.Errorf("result %v should be strictly after now %v", got, tc.now)
			}
		})
	}
}
