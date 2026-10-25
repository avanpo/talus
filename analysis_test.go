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
