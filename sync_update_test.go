package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNeedsUpdate(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "talus-update-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	file := filepath.Join(tmpDir, "100.json")

	// Base local activity
	local := ActivityDetail{
		ID:                 100,
		Name:               "Morning Run",
		Type:               "Run",
		SportType:          "Run",
		Distance:           10000.0,
		MovingTime:         3000,
		TotalElevationGain: 100,
		GearID:             "g12345",
		Streams: map[string]StreamInfo{
			"latlng": {Data: []interface{}{1.0}},
		},
	}
	data, _ := json.Marshal(local)
	if err := os.WriteFile(file, data, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// 1. Identical summary: should not need update
	sa := SummaryActivity{
		ID:         100,
		Name:       "Morning Run",
		Type:       "Run",
		SportType:  "Run",
		Distance:   10000.0,
		MovingTime: 3000,
		GearID:     "g12345",
		StartDate:  time.Now(),
	}
	needed, _ := needsUpdate(file, &sa)
	if needed {
		t.Errorf("expected no update needed for identical summary")
	}

	// 2. Renamed on Strava
	saRenamed := sa
	saRenamed.Name = "Morning 10k Tempo"
	needed, reason := needsUpdate(file, &saRenamed)
	if !needed {
		t.Errorf("expected update needed for renamed activity")
	}
	t.Logf("Reason: %s", reason)

	// 3. Changed sport type to TrailRun
	saTrail := sa
	saTrail.SportType = "TrailRun"
	needed, reason = needsUpdate(file, &saTrail)
	if !needed {
		t.Errorf("expected update needed for sport type change")
	}
	t.Logf("Reason: %s", reason)

	// 4. Cropped on Strava (distance changed)
	saCropped := sa
	saCropped.Distance = 9500.0
	needed, reason = needsUpdate(file, &saCropped)
	if !needed {
		t.Errorf("expected update needed for distance crop")
	}
	t.Logf("Reason: %s", reason)

	// 5. Gear changed
	saGear := sa
	saGear.GearID = "g99999"
	needed, reason = needsUpdate(file, &saGear)
	if !needed {
		t.Errorf("expected update needed for gear change")
	}
	t.Logf("Reason: %s", reason)
}
