package reports

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"testing"

	eventsv1 "github.com/Ronin11/medsage-proto/medsage/events/v1"
)

func TestAdherenceColumn(t *testing.T) {
	tests := []struct {
		typ  eventsv1.EventType
		want string
	}{
		{eventsv1.EventType_EVENT_TYPE_MEDICATION_DISPENSED, "dispensed"},
		{eventsv1.EventType_EVENT_TYPE_MEDICATION_MISSED, "missed"},
		{eventsv1.EventType_EVENT_TYPE_MEDICATION_CONFIRMED, "confirmed"},
		{eventsv1.EventType_EVENT_TYPE_UNSPECIFIED, ""},
		{eventsv1.EventType_EVENT_TYPE_MEDICATION_RECONCILED, ""},
	}
	for _, tc := range tests {
		t.Run(tc.typ.String(), func(t *testing.T) {
			if got := adherenceColumn(tc.typ); got != tc.want {
				t.Errorf("adherenceColumn(%v) = %q, want %q", tc.typ, got, tc.want)
			}
		})
	}
}

// Regression: daily_adherence.device_id references devices, so an event from a
// device that was never registered fails the foreign key. Returning that error
// leaves the event to be redelivered forever; the projector must drop it from
// the rollup instead — while any other database error still surfaces.
func TestIsUnknownDeviceFK(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "the unknown-device foreign key",
			err: &pgconn.PgError{
				Code:           "23503",
				ConstraintName: "daily_adherence_device_id_fkey",
			},
			want: true,
		},
		{
			name: "a different foreign key must not be swallowed",
			err: &pgconn.PgError{
				Code:           "23503",
				ConstraintName: "some_other_fkey",
			},
			want: false,
		},
		{
			name: "an unrelated database error must not be swallowed",
			err:  &pgconn.PgError{Code: "42P01"}, // undefined_table
			want: false,
		},
		{
			name: "a plain error must not be swallowed",
			err:  errors.New("connection refused"),
			want: false,
		},
		{
			name: "wrapped still matches",
			err: fmt.Errorf("upsert daily_adherence: %w", &pgconn.PgError{
				Code:           "23503",
				ConstraintName: "daily_adherence_device_id_fkey",
			}),
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUnknownDeviceFK(tc.err); got != tc.want {
				t.Errorf("isUnknownDeviceFK = %v, want %v", got, tc.want)
			}
		})
	}
}
