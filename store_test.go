package main

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

func BenchmarkCalculateStatsInMemory(b *testing.B) {
	// Generate 5,000 synthetic in-memory activities
	activities := make([]*ActivityDetail, 5000)
	baseTime := time.Date(2020, 1, 1, 8, 0, 0, 0, time.UTC)

	for i := 0; i < 5000; i++ {
		t := baseTime.Add(time.Duration(i*12) * time.Hour)
		sport := "Run"
		if i%3 == 0 {
			sport = "TrailRun"
		} else if i%5 == 0 {
			sport = "Ride"
		}

		activities[i] = &ActivityDetail{
			ID:                 int64(i + 1),
			Name:               fmt.Sprintf("Workout %d", i+1),
			Type:               "Run",
			SportType:          sport,
			StartDate:          t,
			Distance:           float64(5000 + rand.Intn(15000)),
			MovingTime:         1500 + rand.Intn(3600),
			TotalElevationGain: float64(50 + rand.Intn(500)),
			AverageHeartrate:   140.0 + float64(rand.Intn(30)),
		}
	}

	opts := StatsOptions{
		Period:    "month",
		SportType: "run",
		Unit:      "km",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		periods, total, _ := CalculatePeriodicStatsFromActivities(activities, opts)
		if len(periods) == 0 || total.Count == 0 {
			b.Fatal("unexpected empty stats")
		}
	}
}
