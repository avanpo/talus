package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestComputeActivityHRSummaryAndEstimation(t *testing.T) {
	// Build a 40-second stream with a 1-second 220 bpm glitch and 35s sustained ~180 bpm
	hrData := make([]interface{}, 40)
	timeData := make([]interface{}, 40)
	for i := 0; i < 40; i++ {
		timeData[i] = float64(i)
		if i == 0 {
			hrData[i] = 220.0 // 1s spike
		} else if i < 5 {
			hrData[i] = 140.0
		} else {
			hrData[i] = 180.0
		}
	}

	actWithStream := &ActivityDetail{
		ID:           1,
		Type:         "Run",
		StartDate:    time.Date(2025, 6, 1, 8, 0, 0, 0, time.UTC),
		MaxHeartrate: 220,
		MovingTime:   40,
		Streams: map[string]StreamInfo{
			"heartrate": {Data: hrData},
			"time":      {Data: timeData},
		},
	}

	sum1 := computeActivityHRSummary(actWithStream)
	if sum1 == nil || !sum1.HasStream {
		t.Fatalf("expected stream-backed HR summary")
	}
	if sum1.Peak30sHR < 179.0 || sum1.Peak30sHR > 181.5 {
		t.Fatalf("expected Peak30sHR near 180 (ignoring 1s spike), got %.2f", sum1.Peak30sHR)
	}

	// Fallback activity without streams
	actNoStream := &ActivityDetail{
		ID:               2,
		Type:             "Run",
		StartDate:        time.Date(2024, 5, 1, 8, 0, 0, 0, time.UTC),
		AverageHeartrate: 150,
		MovingTime:       1800,
	}
	sum2 := computeActivityHRSummary(actNoStream)
	if sum2 == nil || sum2.HasStream {
		t.Fatalf("expected non-stream fallback HR summary")
	}
	if sum2.BPMSeconds[150] != 1800 || sum2.TotalSeconds != 1800 {
		t.Fatalf("unexpected fallback histogram: %+v", sum2)
	}

	zones, meta := EstimateHRZones([]*ActivityHRSummary{sum1, sum2})
	if len(zones) != 5 {
		t.Fatalf("expected 5 zones, got %d", len(zones))
	}
	if meta.UsedDefaultHRMax {
		t.Fatalf("did not expect default HRmax to be used")
	}
	if meta.EstimatedHRMax < 179 || meta.EstimatedHRMax > 182 {
		t.Fatalf("expected EstimatedHRMax ~180, got %d", meta.EstimatedHRMax)
	}
	for i := 1; i < len(zones); i++ {
		if zones[i].Min != zones[i-1].Max+1 {
			t.Errorf("expected contiguous zones, got Z%d max %d and Z%d min %d", i, zones[i-1].Max, i+1, zones[i].Min)
		}
	}
}

func TestEstimateHRZonesLTHRAnchored(t *testing.T) {
	// Simulate a fit runner with HRmax ~190 and peak 20-min effort at ~181 bpm -> LTHR ~172 bpm
	summaries := []*ActivityHRSummary{
		{
			BPMSeconds:   map[int]int{150: 1200, 181: 1200},
			TotalSeconds: 2400,
			Peak30sHR:    190,
			Peak20mHR:    181,
			HasStream:    true,
		},
	}

	zones, meta := EstimateHRZones(summaries)
	if meta.Model != "lthr" {
		t.Fatalf("expected LTHR model, got %q", meta.Model)
	}
	if meta.EstimatedLTHR != 172 {
		t.Fatalf("expected EstimatedLTHR 172, got %d", meta.EstimatedLTHR)
	}
	// Z2 should be 85-89% of 172 -> 146..154 (compressed), Z5 starts at 172
	if zones[1].Min != 146 || zones[4].Min != 172 || zones[4].Max != 190 {
		t.Fatalf("unexpected LTHR-anchored zones: %+v", zones)
	}
	for i := 1; i < len(zones); i++ {
		if zones[i].Min != zones[i-1].Max+1 {
			t.Errorf("expected contiguous zones, got Z%d max %d and Z%d min %d", i, zones[i-1].Max, i+1, zones[i].Min)
		}
	}
}

func TestComputeZoneDistribution(t *testing.T) {
	zones := []HRZone{
		{Zone: 1, Name: "Zone 1: Recovery", Min: 95, Max: 114},
		{Zone: 2, Name: "Zone 2: Aerobic", Min: 115, Max: 133},
		{Zone: 3, Name: "Zone 3: Tempo", Min: 134, Max: 152},
		{Zone: 4, Name: "Zone 4: Threshold", Min: 153, Max: 171},
		{Zone: 5, Name: "Zone 5: VO2 Max", Min: 172, Max: 190},
	}

	hist := map[int]int{
		100: 100, // Z1 (10%)
		125: 400, // Z2 (40%)
		145: 300, // Z3 (30%)
		165: 150, // Z4 (15%)
		180: 50,  // Z5 (5%)
	}

	dist := ComputeZoneDistribution(hist, zones)
	if len(dist) != 5 {
		t.Fatalf("expected 5 items, got %d", len(dist))
	}
	expectedPct := []float64{10, 40, 30, 15, 5}
	for i, exp := range expectedPct {
		if dist[i].Percent != exp {
			t.Errorf("zone %d: expected %.1f%%, got %.1f%%", i+1, exp, dist[i].Percent)
		}
	}
}

func TestHRZonesEndpointsAndPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	actsDir := filepath.Join(tmpDir, "activities")
	if err := os.MkdirAll(actsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(tmpDir, &Config{ClientID: "test", AccessToken: "tok"}); err != nil {
		t.Fatal(err)
	}

	// Reset global store & daemon for isolated test
	globalStoreMu.Lock()
	globalStore = nil
	globalStoreMu.Unlock()
	daemon = newSyncDaemon(tmpDir, time.Hour, time.Hour, true)

	// Create one 2025 Run and one 2026 Ride
	runAct := ActivityDetail{
		ID:               101,
		Name:             "Morning Run",
		Type:             "Run",
		StartDate:        time.Date(2025, 7, 10, 7, 0, 0, 0, time.UTC),
		AverageHeartrate: 145,
		MaxHeartrate:     185,
		MovingTime:       1800,
	}
	rideAct := ActivityDetail{
		ID:               102,
		Name:             "Evening Ride",
		Type:             "Ride",
		StartDate:        time.Date(2026, 3, 15, 17, 0, 0, 0, time.UTC),
		AverageHeartrate: 135,
		MaxHeartrate:     175,
		MovingTime:       3600,
	}
	for _, a := range []ActivityDetail{runAct, rideAct} {
		b, _ := json.Marshal(a)
		fileName := filepath.Join(actsDir, map[int64]string{101: "101.json", 102: "102.json"}[a.ID])
		if err := os.WriteFile(fileName, b, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Page handler
	wPage := httptest.NewRecorder()
	reqPage := httptest.NewRequest(http.MethodGet, "/hr-zones", nil)
	handleHRZonesPage(wPage, reqPage)
	if wPage.Code != http.StatusOK {
		t.Fatalf("handleHRZonesPage returned %d, expected 200", wPage.Code)
	}

	// 2. GET /api/hr-zones?sport=running&year=0
	wGet := httptest.NewRecorder()
	reqGet := httptest.NewRequest(http.MethodGet, "/api/hr-zones?sport=running&year=0", nil)
	handleHRZonesAPI(wGet, reqGet)
	if wGet.Code != http.StatusOK {
		t.Fatalf("GET /api/hr-zones returned %d", wGet.Code)
	}
	var resp HRZonesResponseJSON
	if err := json.Unmarshal(wGet.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.HasSavedZones {
		t.Errorf("expected HasSavedZones=false initially")
	}
	if resp.EstimationMeta.EstimatedHRMax != 185 {
		t.Errorf("expected running HRmax 185, got %d", resp.EstimationMeta.EstimatedHRMax)
	}
	if resp.EstimationMeta.ActivitiesAnalyzed != 1 {
		t.Errorf("expected 1 running activity analyzed, got %d", resp.EstimationMeta.ActivitiesAnalyzed)
	}

	// 3. POST /api/hr-zones to save custom running zones
	customZones := []HRZone{
		{Zone: 1, Name: "Zone 1: Recovery", Min: 100, Max: 120},
		{Zone: 2, Name: "Zone 2: Aerobic", Min: 121, Max: 140},
		{Zone: 3, Name: "Zone 3: Tempo", Min: 141, Max: 155},
		{Zone: 4, Name: "Zone 4: Threshold", Min: 156, Max: 170},
		{Zone: 5, Name: "Zone 5: VO2 Max", Min: 171, Max: 190},
	}
	payload, _ := json.Marshal(SaveHRZonesRequestJSON{Sport: "running", Zones: customZones})
	wPost := httptest.NewRecorder()
	reqPost := httptest.NewRequest(http.MethodPost, "/api/hr-zones", bytes.NewReader(payload))
	handleHRZonesAPI(wPost, reqPost)
	if wPost.Code != http.StatusOK {
		t.Fatalf("POST /api/hr-zones returned %d: %s", wPost.Code, wPost.Body.String())
	}

	// 4. GET again should return HasSavedZones=true and SavedZones
	wGet2 := httptest.NewRecorder()
	reqGet2 := httptest.NewRequest(http.MethodGet, "/api/hr-zones?sport=running&year=0", nil)
	handleHRZonesAPI(wGet2, reqGet2)
	var resp2 HRZonesResponseJSON
	if err := json.Unmarshal(wGet2.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if !resp2.HasSavedZones || len(resp2.SavedZones) != 5 || resp2.SavedZones[1].Max != 140 {
		t.Fatalf("expected saved zones to persist, got %+v", resp2)
	}

	// 5. POST reset=true clears saved zones
	resetPayload, _ := json.Marshal(SaveHRZonesRequestJSON{Sport: "running", Reset: true})
	wReset := httptest.NewRecorder()
	reqReset := httptest.NewRequest(http.MethodPost, "/api/hr-zones", bytes.NewReader(resetPayload))
	handleHRZonesAPI(wReset, reqReset)
	if wReset.Code != http.StatusOK {
		t.Fatalf("POST reset returned %d", wReset.Code)
	}

	wGet3 := httptest.NewRecorder()
	reqGet3 := httptest.NewRequest(http.MethodGet, "/api/hr-zones?sport=running&year=0", nil)
	handleHRZonesAPI(wGet3, reqGet3)
	var resp3 HRZonesResponseJSON
	if err := json.Unmarshal(wGet3.Body.Bytes(), &resp3); err != nil {
		t.Fatal(err)
	}
	if resp3.HasSavedZones {
		t.Errorf("expected HasSavedZones=false after reset")
	}
}
