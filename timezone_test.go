package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalStartTime_TimeZones(t *testing.T) {
	// Case 1: California (PDT, UTC-7) evening run
	// UTC: 2024-05-15 02:00:00 (May 15)
	// Local: 2024-05-14 19:00:00 (May 14)
	utcCal := time.Date(2024, 5, 15, 2, 0, 0, 0, time.UTC)
	actCal := &ActivityDetail{
		ID:             101,
		Name:           "Evening Los Angeles Run",
		StartDate:      utcCal,
		StartDateLocal: "2024-05-14T19:00:00Z",
		Timezone:       "(GMT-07:00) America/Los_Angeles",
		UtcOffset:      -25200,
	}

	locCal := actCal.LocalStartTime()
	if locCal.Year() != 2024 || locCal.Month() != 5 || locCal.Day() != 14 {
		t.Fatalf("expected California local date 2024-05-14, got %s", locCal.Format("2006-01-02"))
	}
	if locCal.Hour() != 19 || locCal.Minute() != 0 {
		t.Fatalf("expected California local time 19:00, got %s", locCal.Format("15:04"))
	}

	// Case 2: Tokyo (JST, UTC+9) morning run
	// UTC: 2024-05-14 21:30:00 (May 14)
	// Local: 2024-05-15 06:30:00 (May 15)
	utcTokyo := time.Date(2024, 5, 14, 21, 30, 0, 0, time.UTC)
	actTokyo := &ActivityDetail{
		ID:             102,
		Name:           "Morning Tokyo Run",
		StartDate:      utcTokyo,
		StartDateLocal: "2024-05-15T06:30:00Z",
		Timezone:       "(GMT+09:00) Asia/Tokyo",
		UtcOffset:      32400,
	}

	locTokyo := actTokyo.LocalStartTime()
	if locTokyo.Year() != 2024 || locTokyo.Month() != 5 || locTokyo.Day() != 15 {
		t.Fatalf("expected Tokyo local date 2024-05-15, got %s", locTokyo.Format("2006-01-02"))
	}
	if locTokyo.Hour() != 6 || locTokyo.Minute() != 30 {
		t.Fatalf("expected Tokyo local time 06:30, got %s", locTokyo.Format("15:04"))
	}

	// Case 3: Honolulu (UTC-10) New Year's Eve run
	// Local: Dec 31, 2023 at 20:00
	// UTC: Jan 01, 2024 at 06:00
	utcHnl := time.Date(2024, 1, 1, 6, 0, 0, 0, time.UTC)
	actHnl := &ActivityDetail{
		ID:             103,
		Name:           "NYE Honolulu Run",
		StartDate:      utcHnl,
		StartDateLocal: "2023-12-31T20:00:00Z",
		Timezone:       "(GMT-10:00) Pacific/Honolulu",
		UtcOffset:      -36000,
	}

	locHnl := actHnl.LocalStartTime()
	if locHnl.Year() != 2023 || locHnl.Month() != 12 || locHnl.Day() != 31 {
		t.Fatalf("expected Honolulu local year 2023-12-31, got %s", locHnl.Format("2006-01-02"))
	}

	// Case 4: Sydney (UTC+11) New Year's Day run
	// Local: Jan 01, 2024 at 07:00
	// UTC: Dec 31, 2023 at 20:00
	utcSyd := time.Date(2023, 12, 31, 20, 0, 0, 0, time.UTC)
	actSyd := &ActivityDetail{
		ID:             104,
		Name:           "New Year Morning Sydney Run",
		StartDate:      utcSyd,
		StartDateLocal: "2024-01-01T07:00:00Z",
		Timezone:       "(GMT+11:00) Australia/Sydney",
		UtcOffset:      39600,
	}

	locSyd := actSyd.LocalStartTime()
	if locSyd.Year() != 2024 || locSyd.Month() != 1 || locSyd.Day() != 1 {
		t.Fatalf("expected Sydney local year 2024-01-01, got %s", locSyd.Format("2006-01-02"))
	}
}

func TestStatsTimezoneGrouping(t *testing.T) {
	// Activity A: Run in California on March 31, 2024 at 7:00 PM PDT (UTC-7)
	// UTC is 2024-04-01 02:00:00 UTC (April 1)
	actMarch := &ActivityDetail{
		ID:                 201,
		Name:               "End of March Run",
		Type:               "Run",
		SportType:          "Run",
		StartDate:          time.Date(2024, 4, 1, 2, 0, 0, 0, time.UTC),
		StartDateLocal:     "2024-03-31T19:00:00Z",
		Timezone:           "(GMT-07:00) America/Los_Angeles",
		UtcOffset:          -25200,
		Distance:           10000,
		MovingTime:         3000,
		TotalElevationGain: 100,
	}

	// Activity B: Run in Tokyo on April 1, 2024 at 6:00 AM JST (UTC+9)
	// UTC is 2024-03-31 21:00:00 UTC (March 31)
	actApril := &ActivityDetail{
		ID:                 202,
		Name:               "Start of April Tokyo Run",
		Type:               "Run",
		SportType:          "Run",
		StartDate:          time.Date(2024, 3, 31, 21, 0, 0, 0, time.UTC),
		StartDateLocal:     "2024-04-01T06:00:00Z",
		Timezone:           "(GMT+09:00) Asia/Tokyo",
		UtcOffset:          32400,
		Distance:           12000,
		MovingTime:         3600,
		TotalElevationGain: 150,
	}

	// Monthly aggregation for 2024
	periods, total, _ := CalculatePeriodicStatsFromActivities([]*ActivityDetail{actMarch, actApril}, StatsOptions{
		Period:    "month",
		SportType: "run",
		Year:      2024,
	})

	if total.DistanceM != 22000 {
		t.Fatalf("expected total distance 22000m, got %f", total.DistanceM)
	}

	var marchStats, aprilStats *PeriodStats
	for _, p := range periods {
		if p.Key == "2024-03" {
			marchStats = p
		} else if p.Key == "2024-04" {
			aprilStats = p
		}
	}

	if marchStats == nil || marchStats.DistanceM != 10000 {
		t.Fatalf("expected 2024-03 to have 10000m from California run, got %v", marchStats)
	}
	if aprilStats == nil || aprilStats.DistanceM != 12000 {
		t.Fatalf("expected 2024-04 to have 12000m from Tokyo run, got %v", aprilStats)
	}

	// Year filter test: Activity spanning year boundary
	// Honolulu run Dec 31, 2023 (UTC Jan 1, 2024)
	actNYE := &ActivityDetail{
		ID:                 203,
		Name:               "NYE Honolulu",
		Type:               "Run",
		SportType:          "Run",
		StartDate:          time.Date(2024, 1, 1, 6, 0, 0, 0, time.UTC),
		StartDateLocal:     "2023-12-31T20:00:00Z",
		Timezone:           "(GMT-10:00) Pacific/Honolulu",
		UtcOffset:          -36000,
		Distance:           5000,
		MovingTime:         1500,
		TotalElevationGain: 50,
	}

	// Filtering for Year 2023 should include actNYE
	periods2023, total2023, _ := CalculatePeriodicStatsFromActivities([]*ActivityDetail{actNYE}, StatsOptions{
		Period:    "year",
		SportType: "run",
		Year:      2023,
	})
	if total2023.DistanceM != 5000 {
		t.Fatalf("expected 2023 filter to include Honolulu NYE run (local Dec 31 2023), got %f", total2023.DistanceM)
	}
	if len(periods2023) != 1 || periods2023[0].Key != "2023" {
		t.Fatalf("expected period 2023, got %v", periods2023)
	}

	// Filtering for Year 2024 should NOT include actNYE
	_, total2024, _ := CalculatePeriodicStatsFromActivities([]*ActivityDetail{actNYE}, StatsOptions{
		Period:    "year",
		SportType: "run",
		Year:      2024,
	})
	if total2024.DistanceM != 0 {
		t.Fatalf("expected 2024 filter to NOT include Honolulu NYE run, got %f", total2024.DistanceM)
	}
}

func TestServerTimezoneFormatting(t *testing.T) {
	tempDir := t.TempDir()
	activitiesDir := filepath.Join(tempDir, "activities")
	_ = os.MkdirAll(activitiesDir, 0755)

	// Create an activity in Tokyo (UTC+9)
	// UTC: 2024-06-10 21:00:00 (Monday night UTC)
	// Local: 2024-06-11 06:00:00 (Tuesday morning Tokyo)
	tokyoAct := ActivityDetail{
		ID:                 301,
		Name:               "Tokyo Imperial Palace Loop",
		Type:               "Run",
		SportType:          "Run",
		StartDate:          time.Date(2024, 6, 10, 21, 0, 0, 0, time.UTC),
		StartDateLocal:     "2024-06-11T06:00:00Z",
		Timezone:           "(GMT+09:00) Asia/Tokyo",
		UtcOffset:          32400,
		Distance:           5000,
		MovingTime:         1500,
		ElapsedTime:        1500,
		TotalElevationGain: 30,
	}
	bytes, _ := json.Marshal(tokyoAct)
	_ = os.WriteFile(filepath.Join(activitiesDir, "301.json"), bytes, 0644)

	daemon = newSyncDaemon(tempDir, 1*time.Hour, 24*time.Hour, true)

	// 1. Test /api/activities
	req := httptest.NewRequest("GET", "/api/activities?limit=10", nil)
	w := httptest.NewRecorder()
	handleActivitiesAPI(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("activities API returned %d", w.Code)
	}

	var actResp ActivitiesResponseJSON
	if err := json.Unmarshal(w.Body.Bytes(), &actResp); err != nil {
		t.Fatalf("failed to parse activities response: %v", err)
	}
	if len(actResp.Activities) != 1 {
		t.Fatalf("expected 1 activity, got %d", len(actResp.Activities))
	}
	// The formatted date should be Jun 11, 2024 (Tokyo date), NOT Jun 10, 2024 (UTC date)!
	if actResp.Activities[0].StartDateFmt != "Jun 11, 2024" {
		t.Fatalf("expected StartDateFmt 'Jun 11, 2024', got '%s'", actResp.Activities[0].StartDateFmt)
	}
	if actResp.Activities[0].Timezone != "(GMT+09:00) Asia/Tokyo" {
		t.Fatalf("expected Timezone '(GMT+09:00) Asia/Tokyo', got '%s'", actResp.Activities[0].Timezone)
	}

	// 2. Test /api/activity?id=301
	reqDetail := httptest.NewRequest("GET", "/api/activity?id=301", nil)
	wDetail := httptest.NewRecorder()
	handleActivityDetailAPI(wDetail, reqDetail)

	if wDetail.Code != http.StatusOK {
		t.Fatalf("activity detail API returned %d", wDetail.Code)
	}

	var detailResp ActivityDetailResponseJSON
	if err := json.Unmarshal(wDetail.Body.Bytes(), &detailResp); err != nil {
		t.Fatalf("failed to parse activity detail response: %v", err)
	}
	if detailResp.StartDateFmt != "Jun 11, 2024" {
		t.Fatalf("expected detail StartDateFmt 'Jun 11, 2024', got '%s'", detailResp.StartDateFmt)
	}
	if detailResp.StartDateFull != "Tuesday, Jun 11, 2024 at 6:00 AM" {
		t.Fatalf("expected detail StartDateFull 'Tuesday, Jun 11, 2024 at 6:00 AM', got '%s'", detailResp.StartDateFull)
	}
	if detailResp.Timezone != "(GMT+09:00) Asia/Tokyo" {
		t.Fatalf("expected detail Timezone '(GMT+09:00) Asia/Tokyo', got '%s'", detailResp.Timezone)
	}
}
