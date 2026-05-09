package reports

import (
	"testing"

	eventsv1 "medsage/proto/medsage/events/v1"
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
