package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatsReport(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "talus-stats-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	activitiesDir := filepath.Join(tmpDir, "activities")
	if err := os.MkdirAll(activitiesDir, 0755); err != nil {
		t.Fatalf("failed to create activities dir: %v", err)
	}

	// Create test activities:
	// 1. Road run: 10km in 50m (3000s) = 5:00/km, 100m vert, HR 150
	act1 := ActivityDetail{
		ID:                 1,
		Name:               "Road 10k",
		Type:               "Run",
		SportType:          "Run",
		StartDate:          time.Date(2026, 3, 10, 8, 0, 0, 0, time.UTC),
		Distance:           10000,
		MovingTime:         3000,
		TotalElevationGain: 100,
		AverageHeartrate:   150,
	}

	// 2. Trail run: 10km in 60m (3600s) = 6:00/km, 500m vert, HR 150
	act2 := ActivityDetail{
		ID:                 2,
		Name:               "Mountain Trail Loop",
		Type:               "Run",
		SportType:          "TrailRun",
		StartDate:          time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC),
		Distance:           10000,
		MovingTime:         3600,
		TotalElevationGain: 500,
		AverageHeartrate:   150,
	}

	// 3. Road run in different month: 5km in 25m (1500s)
	act3 := ActivityDetail{
		ID:                 3,
		Name:               "Short Road Run",
		Type:               "Run",
		SportType:          "Run",
		StartDate:          time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC),
		Distance:           5000,
		MovingTime:         1500,
		TotalElevationGain: 30,
		AverageHeartrate:   145,
	}

	for _, act := range []ActivityDetail{act1, act2, act3} {
		data, _ := json.Marshal(act)
		if err := os.WriteFile(filepath.Join(activitiesDir, filepath.Base(filepath.Join(activitiesDir, act.Name+".json"))), data, 0644); err != nil {
			t.Fatalf("failed to write test activity: %v", err)
		}
	}

	// Test Monthly report
	opts := StatsOptions{
		Period:      "month",
		Year:        2026,
		SportType:   "run",
		Unit:        "km",
		ShowSubtype: true,
	}

	if err := GenerateStatsReport(tmpDir, opts); err != nil {
		t.Fatalf("GenerateStatsReport failed: %v", err)
	}

	// Test Weekly report in miles
	optsWeekly := StatsOptions{
		Period:      "week",
		Year:        2026,
		SportType:   "trail",
		Unit:        "mi",
		ShowSubtype: true,
	}
	if err := GenerateStatsReport(tmpDir, optsWeekly); err != nil {
		t.Fatalf("GenerateStatsReport weekly failed: %v", err)
	}

	// Test Yearly report
	optsYearly := StatsOptions{
		Period:      "year",
		Year:        0,
		SportType:   "all",
		Unit:        "km",
		ShowSubtype: true,
	}
	if err := GenerateStatsReport(tmpDir, optsYearly); err != nil {
		t.Fatalf("GenerateStatsReport yearly failed: %v", err)
	}
}

func TestFormatDurationShort(t *testing.T) {
	if s := formatDurationShort(3665); s != "1h 01m" {
		t.Errorf("expected 1h 01m, got %s", s)
	}
	if s := formatDurationShort(125); s != "2m 05s" {
		t.Errorf("expected 2m 05s, got %s", s)
	}
}

func TestFormatComma(t *testing.T) {
	if s := formatComma(1234567); s != "1,234,567" {
		t.Errorf("expected 1,234,567, got %s", s)
	}
	if s := formatComma(500); s != "500" {
		t.Errorf("expected 500, got %s", s)
	}
	if s := formatComma(12345); s != "12,345" {
		t.Errorf("expected 12,345, got %s", s)
	}
}

func TestParseDurationCustom(t *testing.T) {
	d, err := parseDurationCustom("30d")
	if err != nil || d != 30*24*time.Hour {
		t.Errorf("failed parsing 30d: %v, %v", d, err)
	}

	w, err := parseDurationCustom("2w")
	if err != nil || w != 14*24*time.Hour {
		t.Errorf("failed parsing 2w: %v, %v", w, err)
	}
}
