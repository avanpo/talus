package main

import (
	"math"
	"testing"
	"time"
)

func TestDecodePolyline(t *testing.T) {
	// A simple polyline representing a straight line from (38.5, -120.2) to (38.51, -120.21)
	// Points: (38.5, -120.2), (38.51, -120.21)
	// encoded: _p~iF~ps|U_ulLnnqC (standard Google representation)
	encoded := "_p~iF~ps|U_ulLnnqC"
	coords, err := decodePolyline(encoded)
	if err != nil {
		t.Fatalf("decodePolyline failed: %v", err)
	}

	if len(coords) != 2 {
		t.Fatalf("expected 2 coordinates, got %d", len(coords))
	}

	if math.Abs(coords[0][0]-38.5) > 1e-4 || math.Abs(coords[0][1]-(-120.2)) > 1e-4 {
		t.Errorf("first coord incorrect, got: %v", coords[0])
	}
}

func TestDownsamplePath(t *testing.T) {
	coords := [][2]float64{
		{0.0, 0.0},
		{1.0, 0.0},
		{2.0, 0.0},
		{3.0, 0.0},
		{4.0, 0.0},
	}

	sampled := downsamplePath(coords, 3)
	if len(sampled) != 3 {
		t.Fatalf("expected 3 points, got %d", len(sampled))
	}

	// Midpoint should be exactly {2.0, 0.0}
	if sampled[1][0] != 2.0 || sampled[1][1] != 0.0 {
		t.Errorf("midpoint incorrect, got: %v", sampled[1])
	}
}

func TestPathDistanceBiDirectional(t *testing.T) {
	path1 := [][2]float64{
		{0.0, 0.0},
		{1.0, 0.0},
		{2.0, 0.0},
	}
	path2 := [][2]float64{
		{2.0, 0.0},
		{1.0, 0.0},
		{0.0, 0.0},
	}

	dist := pathDistanceBiDirectional(path1, path2)
	if dist > 1.0 {
		t.Errorf("expected bidirectional distance to be close to 0, got: %f", dist)
	}
}

func TestCalculateRunStats(t *testing.T) {
	act := &ActivityDetail{
		ID:               1,
		Name:             "Morning Run",
		Type:             "Run",
		StartDate:        time.Now(),
		Distance:         5000, // 5km
		MovingTime:       1200, // 20min -> 15 km/h -> 250 m/min
		AverageHeartrate: 150,
		Streams: map[string]StreamInfo{
			"heartrate": {Data: []interface{}{140.0, 150.0, 160.0}},
			"time":      {Data: []interface{}{0.0, 600.0, 1200.0}},
			"distance":  {Data: []interface{}{0.0, 2500.0, 5000.0}},
		},
	}

	stats := calculateRunStats(act)

	// Pace should be 4:00/km (4.00 decimal minutes)
	if math.Abs(stats.AvgPaceMinKm-4.00) > 1e-2 {
		t.Errorf("pace calculation incorrect, got %f", stats.AvgPaceMinKm)
	}

	// EF = 250 m/min / 150 bpm = 1.6667
	expectedEF := 250.0 / 150.0
	if math.Abs(stats.EF-expectedEF) > 1e-3 {
		t.Errorf("EF calculation incorrect, got %f, expected %f", stats.EF, expectedEF)
	}

	if !stats.HasDecoupling {
		t.Error("expected decoupling stats to be present")
	}

	// First half: d=2500m, t=600s (10min) -> speed = 250 m/min. hr1 = 145 bpm. ef1 = 250/145 = 1.7241
	// Second half: d=2500m, t=600s (10min) -> speed = 250 m/min. hr2 = 155 bpm. ef2 = 250/155 = 1.6129
	// Decoupling = ((1.7241 - 1.6129) / 1.7241) * 100 = 6.45%
	if math.Abs(stats.DecouplingPct-6.45) > 0.5 {
		t.Errorf("decoupling calculation incorrect, got %f", stats.DecouplingPct)
	}
}

func TestCalculateRunStats_ExtendedMetrics(t *testing.T) {
	// 75-minute (4500s) flat run at 200 m/min (3.333 m/s), 15 km total
	// Minutes 0-60: HR = 145 bpm; Minutes 60-72: HR = 155 bpm; Minutes 72-75: cooldown drop from 155 -> 120 bpm in 60s
	n := 4501
	hrData := make([]interface{}, n)
	timeData := make([]interface{}, n)
	distData := make([]interface{}, n)
	altData := make([]interface{}, n)

	dist := 0.0
	for i := 0; i < n; i++ {
		timeData[i] = float64(i)
		altData[i] = 100.0 // flat
		if i > 0 {
			if i < 4320 {
				dist += 200.0 / 60.0
			} else {
				dist += 1.0 // slow cooldown walk
			}
		}
		distData[i] = dist
		if i < 3600 {
			hrData[i] = 145.0
		} else if i < 4320 {
			hrData[i] = 155.0
		} else if i < 4380 {
			// Linear drop from 155 to 120 over 60 seconds
			hrData[i] = 155.0 - (35.0 * float64(i-4320) / 60.0)
		} else {
			hrData[i] = 120.0
		}
	}

	act := &ActivityDetail{
		ID:               99,
		Type:             "Run",
		SportType:        "Run",
		Distance:         dist,
		MovingTime:       4500,
		AverageHeartrate: 147.0,
		AverageCadence:   85.0, // 170 total spm
		AverageTemp:      22.0,
		Streams: map[string]StreamInfo{
			"heartrate": {Data: hrData},
			"time":      {Data: timeData},
			"distance":  {Data: distData},
			"altitude":  {Data: altData},
		},
	}

	stats := calculateRunStats(act)

	if stats.MetersPerBeat <= 1.0 || stats.MetersPerBeat >= 2.0 {
		t.Errorf("unexpected MetersPerBeat: %f", stats.MetersPerBeat)
	}
	if stats.StrideLengthM < 1.0 || stats.StrideLengthM > 1.4 {
		t.Errorf("unexpected StrideLengthM: %f", stats.StrideLengthM)
	}
	if !stats.HasTemp || stats.TempC != 22.0 {
		t.Errorf("expected TempC=22.0, got HasTemp=%v TempC=%f", stats.HasTemp, stats.TempC)
	}
	if spd, ok := stats.FlatSpeedByHR[145]; !ok || math.Abs(spd-200.0) > 2.0 {
		t.Errorf("expected FlatSpeedByHR[145] ~200 m/min, got ok=%v spd=%f", ok, spd)
	}
	if !stats.HasDurability {
		t.Fatalf("expected HasDurability=true")
	}
	// Early EF = 200/145 = 1.379, Late EF (mostly 155 bpm) -> ~93.5% retention
	if stats.DurabilityPct < 80.0 || stats.DurabilityPct > 98.0 {
		t.Errorf("unexpected DurabilityPct: %f", stats.DurabilityPct)
	}
	if !stats.HasHRR60 || stats.HRR60 < 30 || stats.HRR60 > 38 {
		t.Errorf("expected HRR60 around 35 bpm, got HasHRR60=%v HRR60=%d", stats.HasHRR60, stats.HRR60)
	}
}

func TestComputeTempAdjustedEF(t *testing.T) {
	// Synthetic dataset: EF = 1.60 - 0.01 * (TempC - 15) across 20 runs from 5°C to 24°C
	var runs []*RunStats
	for i := 0; i < 20; i++ {
		tempC := 5.0 + float64(i)
		ef := 1.60 - 0.01*(tempC-15.0)
		runs = append(runs, &RunStats{
			Activity: &ActivityDetail{ID: int64(i + 1)},
			EF:       ef,
			TempC:    tempC,
			HasTemp:  true,
		})
	}

	adj, model := ComputeTempAdjustedEF(runs)
	if !model.Enabled {
		t.Fatalf("expected temp model enabled")
	}
	if math.Abs(model.SlopePer10C-(-0.10)) > 0.005 {
		t.Errorf("expected SlopePer10C ~ -0.10, got %f", model.SlopePer10C)
	}
	// Every run normalized to 15°C should recover ~1.60
	for _, r := range runs {
		if v := adj[r.Activity.ID]; math.Abs(v-1.60) > 0.01 {
			t.Errorf("run %d: expected normalized EF ~1.60, got %f", r.Activity.ID, v)
		}
	}
}

func TestComputeTrainingLoadSeries(t *testing.T) {
	daily := map[string]float64{
		"2025-12-30": 100.0,
		"2025-12-31": 100.0,
		"2026-01-01": 0.0,
		"2026-01-02": 120.0,
	}

	allPts := ComputeTrainingLoadSeries(daily, 0)
	if len(allPts) != 4 {
		t.Fatalf("expected 4 daily points, got %d", len(allPts))
	}
	if allPts[3].CTL <= 0 || allPts[3].ATL <= allPts[3].CTL {
		t.Errorf("expected ATL > CTL > 0 after initial load burst, got CTL=%f ATL=%f", allPts[3].CTL, allPts[3].ATL)
	}

	y2026 := ComputeTrainingLoadSeries(daily, 2026)
	if len(y2026) != 2 {
		t.Fatalf("expected 2 points for year 2026, got %d", len(y2026))
	}
	// CTL on 2026-01-01 should carry over from Dec 2025 rather than starting at 0
	if y2026[0].CTL <= 0 {
		t.Errorf("expected carried-over CTL > 0 on 2026-01-01, got %f", y2026[0].CTL)
	}
}
