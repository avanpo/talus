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

func TestSkiTouringVsDownhill(t *testing.T) {
	alpine := &ActivityDetail{
		ID:                 10,
		SportType:          "AlpineSki",
		Type:               "AlpineSki",
		Distance:           25000,
		TotalElevationGain: 4000,
		MovingTime:         7200,
		StartDate:          time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC),
	}
	snowboard := &ActivityDetail{
		ID:                 11,
		SportType:          "Snowboard",
		Type:               "Snowboard",
		Distance:           15000,
		TotalElevationGain: 3000,
		MovingTime:         5000,
		StartDate:          time.Date(2026, 1, 16, 9, 0, 0, 0, time.UTC),
	}
	backcountry := &ActivityDetail{
		ID:                 12,
		SportType:          "BackcountrySki",
		Type:               "BackcountrySki",
		Distance:           8000,
		TotalElevationGain: 1200,
		MovingTime:         9000,
		StartDate:          time.Date(2026, 1, 17, 9, 0, 0, 0, time.UTC),
	}

	if !IsDownhillSki(alpine) {
		t.Errorf("expected AlpineSki to be recognized as downhill")
	}
	if !IsDownhillSki(snowboard) {
		t.Errorf("expected Snowboard to be recognized as downhill")
	}
	if IsDownhillSki(backcountry) {
		t.Errorf("expected BackcountrySki to NOT be downhill")
	}

	if IsSkiTour(alpine) {
		t.Errorf("AlpineSki must not be recognized as ski tour")
	}
	if !IsSkiTour(backcountry) {
		t.Errorf("BackcountrySki must be recognized as ski tour")
	}

	// Verify stats calculation ignores downhill
	activities := []*ActivityDetail{alpine, snowboard, backcountry}
	_, totalSki, _ := CalculatePeriodicStatsFromActivities(activities, StatsOptions{
		SportType: "ski_tour",
	})

	if totalSki.Count != 1 {
		t.Errorf("expected 1 ski tour activity, got %d", totalSki.Count)
	}
	if totalSki.ElevationM != 1200 {
		t.Errorf("expected 1200m elevation gain, got %f", totalSki.ElevationM)
	}

	// Also verify 'all' ignores downhill
	_, totalAll, _ := CalculatePeriodicStatsFromActivities(activities, StatsOptions{
		SportType: "all",
	})
	if totalAll.Count != 1 {
		t.Errorf("expected 1 activity in 'all', got %d", totalAll.Count)
	}
}

func TestGapFillingAndSubcategories(t *testing.T) {
	// 1. Test month gap filling in a specific year
	actApr := &ActivityDetail{
		ID:        201,
		Type:      "BackcountrySki",
		SportType: "BackcountrySki",
		StartDate: time.Date(2024, 4, 10, 10, 0, 0, 0, time.UTC),
		Distance:  5000,
	}
	actDec := &ActivityDetail{
		ID:        202,
		Type:      "BackcountrySki",
		SportType: "BackcountrySki",
		StartDate: time.Date(2024, 12, 5, 10, 0, 0, 0, time.UTC),
		Distance:  8000,
	}

	periods, total, _ := CalculatePeriodicStatsFromActivities([]*ActivityDetail{actApr, actDec}, StatsOptions{
		Period:    "month",
		Year:      2024,
		SportType: "ski_tour",
	})

	if total.Count != 2 {
		t.Errorf("expected total count 2, got %d", total.Count)
	}

	// For a specific year, all 12 months should be generated so gaps show as zero bars
	if len(periods) != 12 {
		t.Fatalf("expected 12 months for 2024, got %d", len(periods))
	}
	if periods[0].Key != "2024-01" || periods[0].Count != 0 {
		t.Errorf("expected 2024-01 to have count 0, got %d", periods[0].Count)
	}
	if periods[3].Key != "2024-04" || periods[3].Count != 1 {
		t.Errorf("expected 2024-04 to have count 1, got %d", periods[3].Count)
	}
	if periods[4].Key != "2024-05" || periods[4].Count != 0 {
		t.Errorf("expected 2024-05 gap to have count 0, got %d", periods[4].Count)
	}
	if periods[11].Key != "2024-12" || periods[11].Count != 1 {
		t.Errorf("expected 2024-12 to have count 1, got %d", periods[11].Count)
	}

	// 2. Test year gap filling across all years (e.g. 2021 and 2024 activities)
	act2021 := &ActivityDetail{
		ID:        203,
		Type:      "Run",
		SportType: "Run",
		StartDate: time.Date(2021, 6, 1, 10, 0, 0, 0, time.UTC),
		Distance:  10000,
	}
	act2024 := &ActivityDetail{
		ID:        204,
		Type:      "Run",
		SportType: "Run",
		StartDate: time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC),
		Distance:  12000,
	}
	yearPeriods, _, _ := CalculatePeriodicStatsFromActivities([]*ActivityDetail{act2021, act2024}, StatsOptions{
		Period:    "year",
		Year:      0,
		SportType: "run",
	})

	if len(yearPeriods) != 4 {
		t.Fatalf("expected 4 years (2021, 2022, 2023, 2024), got %d", len(yearPeriods))
	}
	if yearPeriods[1].Key != "2022" || yearPeriods[1].Count != 0 {
		t.Errorf("expected 2022 gap to have count 0, got %d", yearPeriods[1].Count)
	}
	if yearPeriods[2].Key != "2023" || yearPeriods[2].Count != 0 {
		t.Errorf("expected 2023 gap to have count 0, got %d", yearPeriods[2].Count)
	}

	// 3. Test 'cardio': aggregates Running (including hike/walk), Cycling, and Backcountry Skiing, excluding other sports like kayak
	trailRun := &ActivityDetail{ID: 301, Type: "Run", SportType: "TrailRun", StartDate: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC), Distance: 10000}
	gravelRide := &ActivityDetail{ID: 302, Type: "Ride", SportType: "GravelRide", StartDate: time.Date(2024, 5, 2, 0, 0, 0, 0, time.UTC), Distance: 30000}
	mtb := &ActivityDetail{ID: 303, Type: "Ride", SportType: "MountainBikeRide", StartDate: time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC), Distance: 20000}
	hike := &ActivityDetail{ID: 304, Type: "Hike", SportType: "Hike", StartDate: time.Date(2024, 5, 4, 0, 0, 0, 0, time.UTC), Distance: 5000}
	skiTour := &ActivityDetail{ID: 305, Type: "BackcountrySki", SportType: "BackcountrySki", StartDate: time.Date(2024, 5, 5, 0, 0, 0, 0, time.UTC), Distance: 8000}
	kayak := &ActivityDetail{ID: 306, Type: "Kayaking", SportType: "Kayaking", StartDate: time.Date(2024, 5, 6, 0, 0, 0, 0, time.UTC), Distance: 4000}

	activities := []*ActivityDetail{trailRun, gravelRide, mtb, hike, skiTour, kayak}

	_, cardioTot, cardioCats := CalculatePeriodicStatsFromActivities(activities, StatsOptions{
		Period:    "month",
		SportType: "cardio",
	})

	if cardioTot.Breakdowns["Running"] == nil {
		t.Errorf("expected 'Running' breakdown under cardio")
	}
	if cardioTot.Breakdowns["Cycling"] == nil {
		t.Errorf("expected 'Cycling' breakdown under cardio")
	}
	if cardioTot.Breakdowns["Backcountry Skiing"] == nil {
		t.Errorf("expected 'Backcountry Skiing' breakdown under cardio")
	}
	if cardioTot.Breakdowns["Kayaking"] != nil {
		t.Errorf("kayaking should NOT be included in cardio")
	}
	if len(cardioCats) != 3 {
		t.Errorf("expected 3 cardio categories, got %d (%v)", len(cardioCats), cardioCats)
	}

	// 4. Test 'all': individual sports for everything
	_, allTot, allCats := CalculatePeriodicStatsFromActivities(activities, StatsOptions{
		Period:    "month",
		SportType: "all",
	})

	if allTot.Breakdowns["Trail Run"] == nil {
		t.Errorf("expected 'Trail Run' in all sports")
	}
	if allTot.Breakdowns["Gravel Cycling"] == nil {
		t.Errorf("expected 'Gravel Cycling' in all sports")
	}
	if allTot.Breakdowns["Mountain Biking"] == nil {
		t.Errorf("expected 'Mountain Biking' in all sports")
	}
	if allTot.Breakdowns["Hiking"] == nil {
		t.Errorf("expected 'Hiking' in all sports")
	}
	if allTot.Breakdowns["Backcountry Skiing"] == nil {
		t.Errorf("expected 'Backcountry Skiing' in all sports")
	}
	if allTot.Breakdowns["Kayaking"] == nil {
		t.Errorf("expected 'Kayaking' in all sports")
	}
	if len(allCats) < 5 {
		t.Errorf("expected at least 5 individual categories under all, got %d", len(allCats))
	}
}
