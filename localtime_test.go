package main

import (
	"testing"
	"time"
)

func TestLocalStartTime(t *testing.T) {
	// A 7:15 AM run in San Francisco. Strava reports start_date in UTC and
	// start_date_local as the wall-clock time with a misleading trailing "Z".
	act := &ActivityDetail{
		StartDate:      time.Date(2023, 6, 15, 14, 15, 0, 0, time.UTC),
		StartDateLocal: "2023-06-15T07:15:00Z",
	}

	got := act.LocalStartTime()
	want := time.Date(2023, 6, 15, 7, 15, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("LocalStartTime() = %v, want %v", got, want)
	}
}

func TestLocalStartTimeFallback(t *testing.T) {
	startDate := time.Date(2023, 6, 15, 14, 15, 0, 0, time.UTC)

	cases := []struct {
		name string
		act  *ActivityDetail
	}{
		{
			name: "missing",
			act:  &ActivityDetail{StartDate: startDate, StartDateLocal: ""},
		},
		{
			name: "unparseable",
			act:  &ActivityDetail{StartDate: startDate, StartDateLocal: "not-a-date"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.act.LocalStartTime()
			if !got.Equal(startDate) {
				t.Errorf("LocalStartTime() = %v, want fallback %v", got, startDate)
			}
		})
	}
}
