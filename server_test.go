package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupTestBackupDir(t *testing.T) string {
	tmpDir, err := os.MkdirTemp("", "talus-server-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	activitiesDir := filepath.Join(tmpDir, "activities")
	if err := os.MkdirAll(activitiesDir, 0755); err != nil {
		t.Fatalf("failed to create activities dir: %v", err)
	}

	act1 := ActivityDetail{
		ID:                 1001,
		Name:               "Morning 10k Run",
		Type:               "Run",
		SportType:          "Run",
		StartDate:          time.Date(2026, 3, 10, 7, 0, 0, 0, time.UTC),
		Distance:           10000.0,
		MovingTime:         3000,
		TotalElevationGain: 100.0,
		AverageHeartrate:   150.0,
		Map:                MapInfo{Polyline: "_p~iF~ps|U_ul^nnqC_mqNvxq`@"},
		SegmentEfforts: []SegmentEffort{
			{
				ID: 501,
				Name: "Hawk Hill Climb",
				Segment: SummarySegment{
					ID:           99,
					Name:         "Hawk Hill Climb",
					Distance:     1000.0,
					AverageGrade: 5.0,
				},
				StartDate:        time.Date(2026, 3, 10, 7, 10, 0, 0, time.UTC),
				MovingTime:       300,
				ElapsedTime:      300,
				Distance:         1000.0,
				AverageHeartrate: 160.0,
			},
		},
	}

	data1, err := json.Marshal(act1)
	if err != nil {
		t.Fatalf("failed to marshal activity 1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activitiesDir, "1001.json"), data1, 0644); err != nil {
		t.Fatalf("failed to write activity file: %v", err)
	}

	act2 := ActivityDetail{
		ID:                 1002,
		Name:               "Tempo Run",
		Type:               "Run",
		SportType:          "Run",
		StartDate:          time.Date(2026, 3, 12, 7, 0, 0, 0, time.UTC),
		Distance:           12000.0,
		MovingTime:         3600,
		TotalElevationGain: 150.0,
		AverageHeartrate:   155.0,
		Map:                MapInfo{Polyline: "_p~iF~ps|U_ul^nnqC_mqNvxq`@"},
		SegmentEfforts: []SegmentEffort{
			{
				ID: 502,
				Name: "Hawk Hill Climb",
				Segment: SummarySegment{
					ID:           99,
					Name:         "Hawk Hill Climb",
					Distance:     1000.0,
					AverageGrade: 5.0,
				},
				StartDate:        time.Date(2026, 3, 12, 7, 25, 0, 0, time.UTC),
				MovingTime:       290,
				ElapsedTime:      290,
				Distance:         1000.0,
				AverageHeartrate: 165.0,
			},
		},
	}

	data2, err := json.Marshal(act2)
	if err != nil {
		t.Fatalf("failed to marshal activity 2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activitiesDir, "1002.json"), data2, 0644); err != nil {
		t.Fatalf("failed to write activity file 2: %v", err)
	}

	// act3 has an effort on Hawk Hill Climb with 0 HR, and an effort on a useless segment with 0 HR
	act3 := ActivityDetail{
		ID:                 1003,
		Name:               "Run without HR strap",
		Type:               "Run",
		SportType:          "Run",
		StartDate:          time.Date(2026, 3, 14, 7, 0, 0, 0, time.UTC),
		Distance:           8000.0,
		MovingTime:         2400,
		TotalElevationGain: 80.0,
		AverageHeartrate:   0.0,
		SegmentEfforts: []SegmentEffort{
			{
				ID: 503,
				Name: "Hawk Hill Climb",
				Segment: SummarySegment{
					ID:           99,
					Name:         "Hawk Hill Climb",
					Distance:     1000.0,
					AverageGrade: 5.0,
				},
				StartDate:        time.Date(2026, 3, 14, 7, 10, 0, 0, time.UTC),
				MovingTime:       285,
				Distance:         1000.0,
				AverageHeartrate: 0.0,
			},
			{
				ID: 504,
				Name: "Zero HR Segment",
				Segment: SummarySegment{
					ID:           888,
					Name:         "Zero HR Segment",
					Distance:     500.0,
					AverageGrade: 2.0,
				},
				StartDate:        time.Date(2026, 3, 14, 7, 20, 0, 0, time.UTC),
				MovingTime:       120,
				Distance:         500.0,
				AverageHeartrate: 0.0,
			},
		},
	}
	data3, err := json.Marshal(act3)
	if err != nil {
		t.Fatalf("failed to marshal activity 3: %v", err)
	}
	if err := os.WriteFile(filepath.Join(activitiesDir, "1003.json"), data3, 0644); err != nil {
		t.Fatalf("failed to write activity file 3: %v", err)
	}

	// act4 and act5 give Hawk Hill Climb 4 valid HR efforts total, and introduce Short Hill with only 3 efforts
	act4 := ActivityDetail{
		ID:                 1004,
		Name:               "Long Run 1",
		Type:               "Run",
		SportType:          "Run",
		StartDate:          time.Date(2026, 3, 16, 7, 0, 0, 0, time.UTC),
		Distance:           15000.0,
		MovingTime:         4500,
		TotalElevationGain: 200.0,
		AverageHeartrate:   152.0,
		SegmentEfforts: []SegmentEffort{
			{
				ID: 505,
				Name: "Hawk Hill Climb",
				Segment: SummarySegment{
					ID:           99,
					Name:         "Hawk Hill Climb",
					Distance:     1000.0,
					AverageGrade: 5.0,
				},
				StartDate:        time.Date(2026, 3, 16, 7, 20, 0, 0, time.UTC),
				MovingTime:       280,
				Distance:         1000.0,
				AverageHeartrate: 162.0,
			},
			{
				ID: 601,
				Name: "Short Hill (3 efforts)",
				Segment: SummarySegment{
					ID:           100,
					Name:         "Short Hill (3 efforts)",
					Distance:     400.0,
					AverageGrade: 3.0,
				},
				StartDate:        time.Date(2026, 3, 16, 7, 40, 0, 0, time.UTC),
				MovingTime:       100,
				Distance:         400.0,
				AverageHeartrate: 155.0,
			},
		},
	}
	data4, _ := json.Marshal(act4)
	_ = os.WriteFile(filepath.Join(activitiesDir, "1004.json"), data4, 0644)

	act5 := ActivityDetail{
		ID:                 1005,
		Name:               "Long Run 2",
		Type:               "Run",
		SportType:          "Run",
		StartDate:          time.Date(2026, 3, 18, 7, 0, 0, 0, time.UTC),
		Distance:           15000.0,
		MovingTime:         4500,
		TotalElevationGain: 200.0,
		AverageHeartrate:   150.0,
		SegmentEfforts: []SegmentEffort{
			{
				ID: 506,
				Name: "Hawk Hill Climb",
				Segment: SummarySegment{
					ID:           99,
					Name:         "Hawk Hill Climb",
					Distance:     1000.0,
					AverageGrade: 5.0,
				},
				StartDate:        time.Date(2026, 3, 18, 7, 20, 0, 0, time.UTC),
				MovingTime:       275,
				Distance:         1000.0,
				AverageHeartrate: 161.0,
			},
			{
				ID: 602,
				Name: "Short Hill (3 efforts)",
				Segment: SummarySegment{
					ID:           100,
					Name:         "Short Hill (3 efforts)",
					Distance:     400.0,
					AverageGrade: 3.0,
				},
				StartDate:        time.Date(2026, 3, 18, 7, 40, 0, 0, time.UTC),
				MovingTime:       98,
				Distance:         400.0,
				AverageHeartrate: 158.0,
			},
		},
	}
	data5, _ := json.Marshal(act5)
	_ = os.WriteFile(filepath.Join(activitiesDir, "1005.json"), data5, 0644)

	// Add 3rd effort for Short Hill to act1 so Short Hill has exactly 3 useful efforts (should be filtered out by 4-effort threshold)
	act1.SegmentEfforts = append(act1.SegmentEfforts, SegmentEffort{
		ID: 603,
		Name: "Short Hill (3 efforts)",
		Segment: SummarySegment{
			ID:           100,
			Name:         "Short Hill (3 efforts)",
			Distance:     400.0,
			AverageGrade: 3.0,
		},
		StartDate:        time.Date(2026, 3, 10, 7, 30, 0, 0, time.UTC),
		MovingTime:       102,
		Distance:         400.0,
		AverageHeartrate: 154.0,
	})
	data1Updated, _ := json.Marshal(act1)
	_ = os.WriteFile(filepath.Join(activitiesDir, "1001.json"), data1Updated, 0644)

	cfg := &Config{
		ClientID:     "12345",
		ClientSecret: "secret",
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}
	if err := SaveConfig(tmpDir, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	meta := &Metadata{
		LastActivityTimestamp: 1773126000,
		LastSyncTime:          time.Now(),
		TotalActivities:       1,
	}
	if err := SaveMetadata(tmpDir, meta); err != nil {
		t.Fatalf("failed to save metadata: %v", err)
	}

	return tmpDir
}

func TestServer_PagesAndAPIs(t *testing.T) {
	// Test unconfigured redirect to /setup
	unconfiguredDir, _ := os.MkdirTemp("", "talus-unconfig-*")
	defer os.RemoveAll(unconfiguredDir)
	daemon = newSyncDaemon(unconfiguredDir, 1*time.Hour, 24*time.Hour, true)

	reqUncfg := httptest.NewRequest(http.MethodGet, "/", nil)
	wUncfg := httptest.NewRecorder()
	handleVolumePage(wUncfg, reqUncfg)
	if wUncfg.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect 303 when unconfigured, got %d", wUncfg.Code)
	}

	reqSetup := httptest.NewRequest(http.MethodGet, "/setup", nil)
	wSetup := httptest.NewRecorder()
	handleSetupPage(wSetup, reqSetup)
	if wSetup.Code != http.StatusOK {
		t.Fatalf("handleSetupPage returned %d, expected 200", wSetup.Code)
	}

	// Now test configured state
	dir := setupTestBackupDir(t)
	defer os.RemoveAll(dir)

	daemon = newSyncDaemon(dir, 1*time.Hour, 24*time.Hour, true)

	// Test 1: Volume page
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handleVolumePage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleVolumePage returned status %d, expected %d", w.Code, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), "Volume - talus") {
		t.Errorf("handleVolumePage missing expected title in body")
	}

	// Test 2: Progress page
	req = httptest.NewRequest(http.MethodGet, "/progress", nil)
	w = httptest.NewRecorder()
	handleProgressPage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleProgressPage returned status %d, expected %d", w.Code, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), "Fitness & Progress") {
		t.Errorf("handleProgressPage missing expected title in body")
	}

	// Test 3: Stats API
	req = httptest.NewRequest(http.MethodGet, "/api/stats?period=month&sport=run&unit=km", nil)
	w = httptest.NewRecorder()
	handleStatsAPI(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleStatsAPI returned status %d, expected %d", w.Code, http.StatusOK)
	}
	var statsResp VolumeStatsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &statsResp); err != nil {
		t.Fatalf("failed to unmarshal stats API response: %v", err)
	}
	if statsResp.Total.Count != 5 || statsResp.Total.Distance != 60.0 {
		t.Errorf("expected 5 activities and 60.0km, got count=%d, dist=%f", statsResp.Total.Count, statsResp.Total.Distance)
	}

	// Test 4: Progress API
	req = httptest.NewRequest(http.MethodGet, "/api/progress?unit=km", nil)
	w = httptest.NewRecorder()
	handleProgressAPI(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleProgressAPI returned status %d, expected %d", w.Code, http.StatusOK)
	}
	var progResp ProgressResponse
	if err := json.Unmarshal(w.Body.Bytes(), &progResp); err != nil {
		t.Fatalf("failed to unmarshal progress API response: %v", err)
	}
	if len(progResp.Runs) != 4 {
		t.Errorf("expected 4 runs with valid HR in progress response, got %d", len(progResp.Runs))
	}
	// Verify that Hawk Hill Climb (4 useful efforts) is included, and Short Hill (3 useful efforts) is excluded (< 4)
	if len(progResp.Segments) != 1 {
		t.Fatalf("expected 1 segment (>= 4 useful efforts) in progress response, got %d", len(progResp.Segments))
	}
	seg := progResp.Segments[0]
	if seg.Name != "Hawk Hill Climb" || len(seg.Efforts) != 4 {
		t.Errorf("expected segment 'Hawk Hill Climb' with 4 efforts, got %q with %d", seg.Name, len(seg.Efforts))
	}
	// Verify second effort has pre-run fatigue load from run 1
	eff2 := seg.Efforts[1]
	if eff2.PreRunFatiguePct <= 0 {
		t.Errorf("expected eff2 PreRunFatiguePct > 0, got %f", eff2.PreRunFatiguePct)
	}
	if eff2.InRunFatiguePct <= 0 {
		t.Errorf("expected eff2 InRunFatiguePct > 0, got %f", eff2.InRunFatiguePct)
	}
	if eff2.AdjustedEF <= eff2.EF {
		t.Errorf("expected AdjustedEF > EF due to fatigue, got adj=%f, raw=%f", eff2.AdjustedEF, eff2.EF)
	}

	// Test 4b: Progress API with sport=cycling (should return 0 running segments)
	req = httptest.NewRequest(http.MethodGet, "/api/progress?sport=cycling", nil)
	w = httptest.NewRecorder()
	handleProgressAPI(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleProgressAPI returned status %d, expected %d", w.Code, http.StatusOK)
	}
	var cycleResp ProgressResponse
	if err := json.Unmarshal(w.Body.Bytes(), &cycleResp); err != nil {
		t.Fatalf("failed to unmarshal cycle progress response: %v", err)
	}
	if len(cycleResp.Runs) != 0 || len(cycleResp.Segments) != 0 {
		t.Errorf("expected 0 runs and segments for cycling, got %d runs, %d segs", len(cycleResp.Runs), len(cycleResp.Segments))
	}

	// Test 5: Status API
	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w = httptest.NewRecorder()
	handleStatusAPI(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleStatusAPI returned status %d, expected %d", w.Code, http.StatusOK)
	}
	var statusResp DaemonStatusJSON
	if err := json.Unmarshal(w.Body.Bytes(), &statusResp); err != nil {
		t.Fatalf("failed to unmarshal status API response: %v", err)
	}
	if statusResp.TotalActivities != 1 {
		t.Errorf("expected total activities = 1, got %d", statusResp.TotalActivities)
	}

	// Test 6: Trigger Sync API
	req = httptest.NewRequest(http.MethodPost, "/api/sync", nil)
	w = httptest.NewRecorder()
	handleTriggerSyncAPI(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleTriggerSyncAPI returned status %d, expected %d", w.Code, http.StatusOK)
	}
}
