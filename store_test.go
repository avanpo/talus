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

func TestStore_HolisticInsights(t *testing.T) {
	act1 := &ActivityDetail{
		ID:                 1,
		Name:               "2025 Fast 5K",
		Type:               "Run",
		SportType:          "Run",
		StartDate:          time.Date(2025, 5, 10, 8, 0, 0, 0, time.UTC),
		StartDateLocal:     "2025-05-10T08:00:00Z",
		Distance:           10000,
		MovingTime:         2800,
		TotalElevationGain: 120,
		AverageHeartrate:   155,
		GearID:             "g1",
		Gear:               &GearInfo{ID: "g1", Name: "Pegasus 41", Primary: true},
		BestEfforts: []BestEffortDetail{
			{Name: "5k", MovingTime: 1250, ElapsedTime: 1250, Distance: 5000},
		},
	}
	act2 := &ActivityDetail{
		ID:                 2,
		Name:               "2026 Mountain Trail",
		Type:               "Run",
		SportType:          "TrailRun",
		StartDate:          time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC),
		StartDateLocal:     "2026-03-15T09:00:00Z",
		Distance:           22000,
		MovingTime:         7200,
		TotalElevationGain: 950,
		AverageHeartrate:   148,
		GearID:             "g2",
		Gear:               &GearInfo{ID: "g2", Name: "Speedgoat 6"},
		BestEfforts: []BestEffortDetail{
			{Name: "5k", MovingTime: 1380, ElapsedTime: 1380, Distance: 5000},
		},
	}

	s := &ActivityStore{
		activities: []*ActivityDetail{act1, act2},
		years:      []int{2025, 2026},
		cacheMap: map[string]*cachedFileItem{
			"1.json": {
				activity:  act1,
				hrSummary: &ActivityHRSummary{BPMSeconds: map[int]int{155: 2800}, TotalSeconds: 2800},
			},
			"2.json": {
				activity:  act2,
				hrSummary: &ActivityHRSummary{BPMSeconds: map[int]int{148: 7200}, TotalSeconds: 7200},
			},
		},
	}

	// 1. Running Bests (all-time vs 2026 season)
	bests := s.GetRunningBests(2026, "km")
	if len(bests) < 3 {
		t.Fatalf("expected at least 3 best records (5K, Longest Run, Biggest Vert), got %d", len(bests))
	}
	if bests[0].Label != "5K" || bests[0].AllTimeActID != 1 || bests[0].SeasonActID != 2 {
		t.Errorf("unexpected 5K bests: %+v", bests[0])
	}

	// 2. Shoe Stats
	shoes := s.GetShoeStats("km")
	if len(shoes) != 2 {
		t.Fatalf("expected 2 shoes, got %d", len(shoes))
	}
	if shoes[0].Name != "Speedgoat 6" || shoes[0].TrailDistance != 22.0 {
		t.Errorf("expected Speedgoat 6 most recently used with 22.0 km trail, got %+v", shoes[0])
	}

	// 3. YoY Cumulative Volume
	yoy := s.GetYoYVolume("running", "km")
	if len(yoy.Years) != 2 || len(yoy.Series[2025]) == 0 || len(yoy.Series[2026]) == 0 {
		t.Errorf("expected YoY volume for 2025 and 2026, got %+v", yoy.Years)
	}

	// 4. Monthly HR Histograms
	_, _, _, monthly := s.GetHRZoneData("running", 0)
	if monthly["2025-05"][155] != 2800 || monthly["2026-03"][148] != 7200 {
		t.Errorf("unexpected monthly histograms: %+v", monthly)
	}

	// 5. Training Load with hrMaxOverride & YoY CTL
	pts, yoyCTL := s.GetTrainingLoad("running", 2026, 192.0)
	if len(pts) == 0 || len(yoyCTL[2025]) == 0 || len(yoyCTL[2026]) == 0 {
		t.Errorf("expected training load and YoY CTL points, got pts=%d yoy=%v", len(pts), yoyCTL)
	}
}
