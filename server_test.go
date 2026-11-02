package main

import (
	"encoding/json"
	"fmt"
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

	// Test 2b: Activities page
	req = httptest.NewRequest(http.MethodGet, "/activities", nil)
	w = httptest.NewRecorder()
	handleActivitiesPage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleActivitiesPage returned status %d, expected %d", w.Code, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), "Activities - talus") {
		t.Errorf("handleActivitiesPage missing expected title in body")
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

func TestSyncDaemon_DelayedConfiguration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "talus-daemon-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create unconfigured daemon
	d := newSyncDaemon(tmpDir, 100*time.Millisecond, 200*time.Millisecond, false)
	d.Start()
	defer d.Stop()

	// Verify daemon is running in waiting state
	d.mu.RLock()
	started := d.started
	d.mu.RUnlock()
	if !started {
		t.Errorf("expected daemon.started to be true while waiting for config")
	}

	// Now simulate configuration being written later
	cfg := &Config{
		ClientID:     "12345",
		ClientSecret: "secret",
		AccessToken:  "token",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}
	if err := SaveConfig(tmpDir, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Notify daemon that configuration is ready
	d.NotifyConfigured()

	// Wait briefly for daemon to detect config
	time.Sleep(50 * time.Millisecond)

	if !isConfigured(tmpDir) {
		t.Errorf("expected isConfigured to be true")
	}
}

func TestSyncDaemon_PanicRecovery(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "talus-panic-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	d := newSyncDaemon(tmpDir, 1*time.Hour, 24*time.Hour, false)

	// runSync should recover from any internal error or invalid state without panicking
	d.runSync(SyncOptions{}, "Test run with unconfigured dir")

	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.isSyncing {
		t.Errorf("expected isSyncing to be false after sync completes/fails")
	}
	if d.lastSyncErr == "" {
		t.Errorf("expected lastSyncErr to be recorded")
	}
}

func TestServer_ActivitiesPageAndAPI(t *testing.T) {
	// 1. Test unconfigured redirect
	unconfiguredDir, err := os.MkdirTemp("", "talus-act-unconfig-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(unconfiguredDir)

	daemon = newSyncDaemon(unconfiguredDir, 1*time.Hour, 24*time.Hour, true)
	reqUncfg := httptest.NewRequest(http.MethodGet, "/activities", nil)
	wUncfg := httptest.NewRecorder()
	handleActivitiesPage(wUncfg, reqUncfg)
	if wUncfg.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect 303 when unconfigured, got %d", wUncfg.Code)
	}

	// 2. Test configured with 12 activities
	tmpDir, err := os.MkdirTemp("", "talus-act-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	activitiesDir := filepath.Join(tmpDir, "activities")
	_ = os.MkdirAll(activitiesDir, 0755)

	cfg := &Config{ClientID: "id", ClientSecret: "secret", AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)}
	_ = SaveConfig(tmpDir, cfg)

	baseTime := time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC)
	for i := 1; i <= 12; i++ {
		act := ActivityDetail{
			ID:                 int64(2000 + i),
			Name:               fmt.Sprintf("Activity %d", i),
			Type:               "Run",
			SportType:          "Run",
			StartDate:          baseTime.Add(time.Duration(i) * 24 * time.Hour),
			Distance:           float64(5000 + i*500),
			MovingTime:         1500 + i*60,
			ElapsedTime:        1600 + i*60,
			TotalElevationGain: float64(50 + i*10),
		}

		if i == 1 {
			// Oldest: has shoe gear object
			act.Gear = &GearInfo{ID: "g100", Name: "Saucony Endorphin Speed"}
			act.GearID = "g100"
		} else if i == 2 {
			// Has only gear_id
			act.GearID = "g200"
		} else if i == 3 {
			// Cycling: shoe should not be relevant
			act.Type = "Ride"
			act.SportType = "Ride"
			act.GearID = "b500"
		} else if i == 4 {
			// Trail run with shoe
			act.SportType = "TrailRun"
			act.Gear = &GearInfo{ID: "g300", Name: "Hoka Speedgoat 5"}
		}
		// Others: runs with no shoe specified

		data, _ := json.Marshal(act)
		_ = os.WriteFile(filepath.Join(activitiesDir, fmt.Sprintf("%d.json", act.ID)), data, 0644)
	}

	daemon = newSyncDaemon(tmpDir, 1*time.Hour, 24*time.Hour, true)

	// Test page render
	reqPage := httptest.NewRequest(http.MethodGet, "/activities", nil)
	wPage := httptest.NewRecorder()
	handleActivitiesPage(wPage, reqPage)
	if wPage.Code != http.StatusOK {
		t.Fatalf("handleActivitiesPage returned %d, expected 200", wPage.Code)
	}
	body := wPage.Body.String()
	if !strings.Contains(body, "Activities - talus") {
		t.Errorf("Activities page missing expected title")
	}
	if !strings.Contains(body, "Shoe Used") {
		t.Errorf("Activities page missing Shoe Used column")
	}

	// Test API with default limit (should return 10 newest out of 12)
	reqAPI := httptest.NewRequest(http.MethodGet, "/api/activities", nil)
	wAPI := httptest.NewRecorder()
	handleActivitiesAPI(wAPI, reqAPI)
	if wAPI.Code != http.StatusOK {
		t.Fatalf("handleActivitiesAPI returned %d, expected 200", wAPI.Code)
	}

	var resp ActivitiesResponseJSON
	if err := json.Unmarshal(wAPI.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal API response: %v", err)
	}

	if resp.Total != 12 {
		t.Errorf("expected total 12, got %d", resp.Total)
	}
	if len(resp.Activities) != 10 {
		t.Fatalf("expected 10 activities returned, got %d", len(resp.Activities))
	}

	// Verify order: newest first (Activity 12 should be first, Activity 3 should be last in top 10)
	if resp.Activities[0].ID != 2012 {
		t.Errorf("expected first activity to be ID 2012 (newest), got %d", resp.Activities[0].ID)
	}
	if resp.Activities[9].ID != 2003 {
		t.Errorf("expected 10th activity to be ID 2003, got %d", resp.Activities[9].ID)
	}

	// Check Activity 3 (Cycling ride): shoe not relevant
	act3Item := resp.Activities[9]
	if act3Item.ShoeRelevant {
		t.Errorf("expected ShoeRelevant to be false for cycling ride")
	}
	if act3Item.SportType != "Ride" {
		t.Errorf("expected SportType 'Ride', got %s", act3Item.SportType)
	}

	// Check custom limit: limit=3
	reqLimit := httptest.NewRequest(http.MethodGet, "/api/activities?limit=3", nil)
	wLimit := httptest.NewRecorder()
	handleActivitiesAPI(wLimit, reqLimit)
	var respLimit ActivitiesResponseJSON
	_ = json.Unmarshal(wLimit.Body.Bytes(), &respLimit)
	if len(respLimit.Activities) != 3 {
		t.Errorf("expected 3 activities with limit=3, got %d", len(respLimit.Activities))
	}

	// Check limit=12 with unit=mi
	reqMi := httptest.NewRequest(http.MethodGet, "/api/activities?limit=12&unit=mi", nil)
	wMi := httptest.NewRecorder()
	handleActivitiesAPI(wMi, reqMi)
	var respMi ActivitiesResponseJSON
	_ = json.Unmarshal(wMi.Body.Bytes(), &respMi)
	if respMi.Unit != "mi" {
		t.Errorf("expected unit mi, got %s", respMi.Unit)
	}

	// Activity 1 is last (12th item in respMi)
	act1Item := respMi.Activities[11]
	if act1Item.ID != 2001 {
		t.Errorf("expected 12th item to be ID 2001, got %d", act1Item.ID)
	}
	if !act1Item.ShoeRelevant {
		t.Errorf("expected ShoeRelevant to be true for run")
	}
	if act1Item.Shoe != "Saucony Endorphin Speed" {
		t.Errorf("expected shoe 'Saucony Endorphin Speed', got %q", act1Item.Shoe)
	}
	if !strings.HasSuffix(act1Item.DistanceFmt, "mi") {
		t.Errorf("expected distance in mi, got %s", act1Item.DistanceFmt)
	}
	if !strings.HasSuffix(act1Item.ElevationFmt, "ft") {
		t.Errorf("expected elevation in ft, got %s", act1Item.ElevationFmt)
	}

	// Activity 4 is TrailRun with shoe "Hoka Speedgoat 5"
	act4Item := respMi.Activities[8]
	if act4Item.ID != 2004 {
		t.Errorf("expected item to be ID 2004, got %d", act4Item.ID)
	}
	if act4Item.SportType != "Trail Run" {
		t.Errorf("expected sport type 'Trail Run', got %q", act4Item.SportType)
	}
	if act4Item.Shoe != "Hoka Speedgoat 5" {
		t.Errorf("expected shoe 'Hoka Speedgoat 5', got %q", act4Item.Shoe)
	}

	// Check sport filtering on activities API
	reqRide := httptest.NewRequest(http.MethodGet, "/api/activities?sport=ride", nil)
	wRide := httptest.NewRecorder()
	handleActivitiesAPI(wRide, reqRide)
	var respRide ActivitiesResponseJSON
	_ = json.Unmarshal(wRide.Body.Bytes(), &respRide)
	if respRide.Total != 1 || len(respRide.Activities) != 1 {
		t.Errorf("expected 1 ride activity, got total=%d len=%d", respRide.Total, len(respRide.Activities))
	}
	if len(respRide.Activities) > 0 && respRide.Activities[0].ID != 2003 {
		t.Errorf("expected ride activity ID 2003, got %d", respRide.Activities[0].ID)
	}

	reqTrail := httptest.NewRequest(http.MethodGet, "/api/activities?sport=trail", nil)
	wTrail := httptest.NewRecorder()
	handleActivitiesAPI(wTrail, reqTrail)
	var respTrail ActivitiesResponseJSON
	_ = json.Unmarshal(wTrail.Body.Bytes(), &respTrail)
	if respTrail.Total != 1 || len(respTrail.Activities) != 1 {
		t.Errorf("expected 1 trail activity, got total=%d len=%d", respTrail.Total, len(respTrail.Activities))
	}

	reqRun := httptest.NewRequest(http.MethodGet, "/api/activities?sport=run&limit=20", nil)
	wRun := httptest.NewRecorder()
	handleActivitiesAPI(wRun, reqRun)
	var respRun ActivitiesResponseJSON
	_ = json.Unmarshal(wRun.Body.Bytes(), &respRun)
	if respRun.Total != 11 || len(respRun.Activities) != 11 {
		t.Errorf("expected 11 run activities, got total=%d len=%d", respRun.Total, len(respRun.Activities))
	}

	// Check limits 20 and 50
	req50 := httptest.NewRequest(http.MethodGet, "/api/activities?limit=50", nil)
	w50 := httptest.NewRecorder()
	handleActivitiesAPI(w50, req50)
	var resp50 ActivitiesResponseJSON
	_ = json.Unmarshal(w50.Body.Bytes(), &resp50)
	if resp50.Total != 12 || len(resp50.Activities) != 12 {
		t.Errorf("expected 12 total activities with limit=50, got total=%d len=%d", resp50.Total, len(resp50.Activities))
	}

	// Test Progress API year filter
	reqProgAll := httptest.NewRequest(http.MethodGet, "/api/progress?sport=running", nil)
	wProgAll := httptest.NewRecorder()
	handleProgressAPI(wProgAll, reqProgAll)
	var respProgAll ProgressResponse
	_ = json.Unmarshal(wProgAll.Body.Bytes(), &respProgAll)
	if len(respProgAll.Years) == 0 {
		t.Errorf("expected ProgressResponse to have available years, got none")
	}

	reqProg2026 := httptest.NewRequest(http.MethodGet, "/api/progress?sport=running&year=2026", nil)
	wProg2026 := httptest.NewRecorder()
	handleProgressAPI(wProg2026, reqProg2026)
	var respProg2026 ProgressResponse
	_ = json.Unmarshal(wProg2026.Body.Bytes(), &respProg2026)
	if wProg2026.Code != http.StatusOK {
		t.Errorf("expected status 200 for year=2026, got %d", wProg2026.Code)
	}

	reqProgEmptyYear := httptest.NewRequest(http.MethodGet, "/api/progress?sport=running&year=2015", nil)
	wProgEmptyYear := httptest.NewRecorder()
	handleProgressAPI(wProgEmptyYear, reqProgEmptyYear)
	var respProgEmptyYear ProgressResponse
	_ = json.Unmarshal(wProgEmptyYear.Body.Bytes(), &respProgEmptyYear)
	if len(respProgEmptyYear.Runs) != 0 {
		t.Errorf("expected 0 runs for empty year 2015, got %d", len(respProgEmptyYear.Runs))
	}
}

func TestServer_ActivitySearchAndDetail(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "talus-search-detail-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	activitiesDir := filepath.Join(tmpDir, "activities")
	_ = os.MkdirAll(activitiesDir, 0755)

	cfg := &Config{ClientID: "id", ClientSecret: "secret", AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)}
	_ = SaveConfig(tmpDir, cfg)

	prRank1 := 1
	act1 := ActivityDetail{
		ID:                 5001,
		Name:               "Morning Trail Run in Marin",
		Description:        "Great misty morning on the trails",
		Type:               "Run",
		SportType:          "TrailRun",
		StartDate:          time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC),
		Distance:           10000.0,
		MovingTime:         3600,
		ElapsedTime:        3700,
		TotalElevationGain: 400.0,
		AverageHeartrate:   150.0,
		MaxHeartrate:       175.0,
		AverageCadence:     170.0,
		AverageWatts:       220.0,
		Calories:           750.0,
		Gear:               &GearInfo{ID: "g_hoka", Name: "Hoka Speedgoat 5"},
		DeviceName:         "Garmin Forerunner 965",
		Map:                MapInfo{Polyline: "_p~iF~ps|U_ul^nnqC_mqNvxq`@"},
		SegmentEfforts: []SegmentEffort{
			{
				ID: 901,
				Name: "Dipsea Climb",
				Segment: SummarySegment{
					ID:           10,
					Name:         "Dipsea Climb",
					Distance:     1500.0,
					AverageGrade: 8.5,
				},
				MovingTime:       450,
				ElapsedTime:      450,
				Distance:         1500.0,
				AverageHeartrate: 168.0,
				PRRank:           &prRank1,
			},
		},
		SplitsMetric: []SplitDetail{
			{
				Split:               1,
				Distance:            1000.0,
				MovingTime:          350,
				ElapsedTime:         350,
				ElevationDifference: 20.0,
				AverageHeartrate:    142.0,
			},
			{
				Split:               2,
				Distance:            1000.0,
				MovingTime:          360,
				ElapsedTime:         360,
				ElevationDifference: 50.0,
				AverageHeartrate:    155.0,
			},
		},
		SplitsStandard: []SplitDetail{
			{
				Split:               1,
				Distance:            1609.344,
				MovingTime:          560,
				ElapsedTime:         560,
				ElevationDifference: 30.0,
				AverageHeartrate:    145.0,
			},
		},
		BestEfforts: []BestEffortDetail{
			{
				ID:          801,
				Name:        "1k",
				Distance:    1000.0,
				MovingTime:  310,
				ElapsedTime: 310,
				PRRank:      &prRank1,
			},
		},
		Streams: map[string]StreamInfo{
			"distance": {
				Data: []interface{}{0.0, 500.0, 1000.0, 2000.0},
			},
			"altitude": {
				Data: []interface{}{100.0, 150.0, 180.0, 220.0},
			},
			"heartrate": {
				Data: []interface{}{130.0, 145.0, 152.0, 160.0},
			},
			"time": {
				Data: []interface{}{0, 180, 360, 720},
			},
		},
	}

	act2 := ActivityDetail{
		ID:                 5002,
		Name:               "Alpine Ski Tour on Mt Hood",
		Type:               "BackcountrySki",
		SportType:          "BackcountrySki",
		StartDate:          time.Date(2026, 5, 2, 8, 0, 0, 0, time.UTC),
		Distance:           8000.0,
		MovingTime:         7200,
		TotalElevationGain: 900.0,
	}

	act3 := ActivityDetail{
		ID:                 5003,
		Name:               "Marin Headlands Tempo Run",
		Type:               "Run",
		SportType:          "Run",
		StartDate:          time.Date(2026, 5, 3, 7, 30, 0, 0, time.UTC),
		Distance:           12000.0,
		MovingTime:         3600,
		TotalElevationGain: 250.0,
	}

	act4 := ActivityDetail{
		ID:                 5004,
		Name:               "Recovery Road Ride",
		Type:               "Ride",
		SportType:          "Ride",
		StartDate:          time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		Distance:           30000.0,
		MovingTime:         4500,
		TotalElevationGain: 200.0,
	}

	for _, a := range []ActivityDetail{act1, act2, act3, act4} {
		data, err := json.Marshal(a)
		if err != nil {
			t.Fatalf("failed to marshal activity %d: %v", a.ID, err)
		}
		_ = os.WriteFile(filepath.Join(activitiesDir, fmt.Sprintf("%d.json", a.ID)), data, 0644)
	}

	daemon = newSyncDaemon(tmpDir, 1*time.Hour, 24*time.Hour, true)

	// 1. Search tests on /api/activities
	// Search for "Marin" -> should match 5003 and 5001 (newest first: 5003, 5001)
	reqSearchMarin := httptest.NewRequest(http.MethodGet, "/api/activities?search=Marin", nil)
	wSearchMarin := httptest.NewRecorder()
	handleActivitiesAPI(wSearchMarin, reqSearchMarin)
	if wSearchMarin.Code != http.StatusOK {
		t.Fatalf("expected 200 from search, got %d", wSearchMarin.Code)
	}

	var respSearchMarin ActivitiesResponseJSON
	_ = json.Unmarshal(wSearchMarin.Body.Bytes(), &respSearchMarin)
	if respSearchMarin.Total != 2 || len(respSearchMarin.Activities) != 2 {
		t.Fatalf("expected 2 activities for 'Marin', got total=%d len=%d", respSearchMarin.Total, len(respSearchMarin.Activities))
	}
	if respSearchMarin.Activities[0].ID != 5003 || respSearchMarin.Activities[1].ID != 5001 {
		t.Errorf("unexpected activities order for 'Marin': %v", respSearchMarin.Activities)
	}

	// Search case-insensitivity: "ski tour"
	reqSearchSki := httptest.NewRequest(http.MethodGet, "/api/activities?search=ski+tour", nil)
	wSearchSki := httptest.NewRecorder()
	handleActivitiesAPI(wSearchSki, reqSearchSki)
	var respSearchSki ActivitiesResponseJSON
	_ = json.Unmarshal(wSearchSki.Body.Bytes(), &respSearchSki)
	if respSearchSki.Total != 1 || len(respSearchSki.Activities) != 1 {
		t.Fatalf("expected 1 activity for 'ski tour', got total=%d len=%d", respSearchSki.Total, len(respSearchSki.Activities))
	}
	if respSearchSki.Activities[0].ID != 5002 {
		t.Errorf("expected activity 5002, got %d", respSearchSki.Activities[0].ID)
	}

	// Search combined with sport filter: search="marin" and sport="trail"
	reqSearchSport := httptest.NewRequest(http.MethodGet, "/api/activities?search=marin&sport=trail", nil)
	wSearchSport := httptest.NewRecorder()
	handleActivitiesAPI(wSearchSport, reqSearchSport)
	var respSearchSport ActivitiesResponseJSON
	_ = json.Unmarshal(wSearchSport.Body.Bytes(), &respSearchSport)
	if respSearchSport.Total != 1 || len(respSearchSport.Activities) != 1 {
		t.Fatalf("expected 1 activity for 'marin' + 'trail', got total=%d", respSearchSport.Total)
	}
	if respSearchSport.Activities[0].ID != 5001 {
		t.Errorf("expected activity 5001, got %d", respSearchSport.Activities[0].ID)
	}

	// Search for non-existent text
	reqSearchNone := httptest.NewRequest(http.MethodGet, "/api/activities?search=NonExistentWordXYZ", nil)
	wSearchNone := httptest.NewRecorder()
	handleActivitiesAPI(wSearchNone, reqSearchNone)
	var respSearchNone ActivitiesResponseJSON
	_ = json.Unmarshal(wSearchNone.Body.Bytes(), &respSearchNone)
	if respSearchNone.Total != 0 || len(respSearchNone.Activities) != 0 {
		t.Fatalf("expected 0 activities for non-existent search, got %d", respSearchNone.Total)
	}

	// 2. Activity Detail tests on /api/activity
	reqDetail := httptest.NewRequest(http.MethodGet, "/api/activity?id=5001&unit=mi", nil)
	wDetail := httptest.NewRecorder()
	handleActivityDetailAPI(wDetail, reqDetail)
	if wDetail.Code != http.StatusOK {
		t.Fatalf("expected 200 from activity detail, got %d", wDetail.Code)
	}

	var detailResp ActivityDetailResponseJSON
	if err := json.Unmarshal(wDetail.Body.Bytes(), &detailResp); err != nil {
		t.Fatalf("failed to decode detail JSON: %v", err)
	}

	if detailResp.ID != 5001 {
		t.Errorf("expected detail ID 5001, got %d", detailResp.ID)
	}
	if detailResp.Name != "Morning Trail Run in Marin" {
		t.Errorf("expected name 'Morning Trail Run in Marin', got %q", detailResp.Name)
	}
	if detailResp.Description != "Great misty morning on the trails" {
		t.Errorf("expected description 'Great misty morning on the trails', got %q", detailResp.Description)
	}
	if detailResp.SportType != "Trail Run" {
		t.Errorf("expected sport type 'Trail Run', got %q", detailResp.SportType)
	}
	if detailResp.Shoe != "Hoka Speedgoat 5" {
		t.Errorf("expected shoe 'Hoka Speedgoat 5', got %q", detailResp.Shoe)
	}
	if detailResp.DeviceName != "Garmin Forerunner 965" {
		t.Errorf("expected device 'Garmin Forerunner 965', got %q", detailResp.DeviceName)
	}
	if detailResp.AverageHeartrate != 150.0 {
		t.Errorf("expected avg HR 150, got %f", detailResp.AverageHeartrate)
	}
	if detailResp.EfficiencyFactorFmt == "" {
		t.Errorf("expected non-empty efficiency factor format")
	}
	if len(detailResp.Coordinates) == 0 {
		t.Errorf("expected decoded GPS coordinates from polyline")
	}
	if len(detailResp.Segments) != 1 {
		t.Fatalf("expected 1 segment effort, got %d", len(detailResp.Segments))
	}
	if detailResp.Segments[0].Name != "Dipsea Climb" {
		t.Errorf("expected segment name 'Dipsea Climb', got %q", detailResp.Segments[0].Name)
	}
	if detailResp.Segments[0].PRRank == nil || *detailResp.Segments[0].PRRank != 1 {
		t.Errorf("expected PRRank 1, got %v", detailResp.Segments[0].PRRank)
	}
	if len(detailResp.Splits) == 0 {
		t.Errorf("expected splits in detail response")
	}
	if detailResp.Streams == nil || len(detailResp.Streams.Altitude) == 0 {
		t.Errorf("expected telemetry streams in detail response")
	}
	if !strings.Contains(detailResp.StravaURL, "5001") {
		t.Errorf("expected StravaURL to contain activity ID 5001, got %q", detailResp.StravaURL)
	}

	// Test detail via /api/activities?id=5001
	reqDetailViaActivities := httptest.NewRequest(http.MethodGet, "/api/activities?id=5001", nil)
	wDetailViaActivities := httptest.NewRecorder()
	handleActivitiesAPI(wDetailViaActivities, reqDetailViaActivities)
	if wDetailViaActivities.Code != http.StatusOK {
		t.Fatalf("expected 200 via /api/activities?id=5001, got %d", wDetailViaActivities.Code)
	}

	// 3. Error cases
	// Missing ID
	reqNoID := httptest.NewRequest(http.MethodGet, "/api/activity", nil)
	wNoID := httptest.NewRecorder()
	handleActivityDetailAPI(wNoID, reqNoID)
	if wNoID.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing id, got %d", wNoID.Code)
	}

	// Invalid ID
	reqBadID := httptest.NewRequest(http.MethodGet, "/api/activity?id=abc", nil)
	wBadID := httptest.NewRecorder()
	handleActivityDetailAPI(wBadID, reqBadID)
	if wBadID.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad id, got %d", wBadID.Code)
	}

	// Non-existent ID
	reqMissingAct := httptest.NewRequest(http.MethodGet, "/api/activity?id=99999", nil)
	wMissingAct := httptest.NewRecorder()
	handleActivityDetailAPI(wMissingAct, reqMissingAct)
	if wMissingAct.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing activity, got %d", wMissingAct.Code)
	}

	// 4. Test page render for /activity and /activities
	reqActivityPage := httptest.NewRequest(http.MethodGet, "/activity?id=5001", nil)
	wActivityPage := httptest.NewRecorder()
	handleActivitiesPage(wActivityPage, reqActivityPage)
	if wActivityPage.Code != http.StatusOK {
		t.Fatalf("expected 200 for /activity page, got %d", wActivityPage.Code)
	}
	body := wActivityPage.Body.String()
	if !strings.Contains(body, "searchInput") {
		t.Errorf("activities page missing searchInput element")
	}
	if !strings.Contains(body, "activityDetailView") {
		t.Errorf("activities page missing activityDetailView container")
	}
}

func TestServer_SeparatedDirectories(t *testing.T) {
	dataDir, err := os.MkdirTemp("", "talus-srv-data-*")
	if err != nil {
		t.Fatalf("failed to create dataDir: %v", err)
	}
	defer os.RemoveAll(dataDir)

	configDir, err := os.MkdirTemp("", "talus-srv-config-*")
	if err != nil {
		t.Fatalf("failed to create configDir: %v", err)
	}
	defer os.RemoveAll(configDir)

	metadataDir, err := os.MkdirTemp("", "talus-srv-meta-*")
	if err != nil {
		t.Fatalf("failed to create metadataDir: %v", err)
	}
	defer os.RemoveAll(metadataDir)

	// Initially unconfigured
	dUncfg := newSyncDaemonWithDirs(dataDir, configDir, metadataDir, time.Hour, 24*time.Hour, true)
	if isDaemonConfigured(dUncfg) {
		t.Errorf("expected isDaemonConfigured to be false for empty configDir")
	}

	daemon = dUncfg
	reqSetupCheck := httptest.NewRequest(http.MethodGet, "/", nil)
	wSetupCheck := httptest.NewRecorder()
	handleVolumePage(wSetupCheck, reqSetupCheck)
	if wSetupCheck.Code != http.StatusSeeOther {
		t.Errorf("expected redirect to /setup when unconfigured, got %d", wSetupCheck.Code)
	}

	// Now configure: Save config in configDir
	cfg := &Config{
		ClientID:     "srv_client_id",
		ClientSecret: "srv_client_secret",
		AccessToken:  "valid_tok",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	if err := SaveConfig(configDir, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	// Save metadata in metadataDir
	meta := &Metadata{
		LastActivityTimestamp: 1700000000,
		LastSyncTime:          time.Now(),
		TotalActivities:       1,
	}
	if err := SaveMetadata(metadataDir, meta); err != nil {
		t.Fatalf("failed to save metadata: %v", err)
	}

	// Save an activity in dataDir/activities/7001.json
	activitiesDir := filepath.Join(dataDir, "activities")
	_ = os.MkdirAll(activitiesDir, 0755)
	act := ActivityDetail{
		ID:                 7001,
		Name:               "Run from Separated Data Dir",
		Type:               "Run",
		SportType:          "Run",
		StartDate:          time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC),
		Distance:           10000.0,
		MovingTime:         3000,
		TotalElevationGain: 100.0,
	}
	actData, _ := json.Marshal(act)
	_ = os.WriteFile(filepath.Join(activitiesDir, "7001.json"), actData, 0644)

	dConfigured := newSyncDaemonWithDirs(dataDir, configDir, metadataDir, time.Hour, 24*time.Hour, true)
	daemon = dConfigured

	if !isDaemonConfigured(daemon) {
		t.Errorf("expected isDaemonConfigured to be true when config is in configDir")
	}

	// Status API reads metadata from metadataDir
	reqStatus := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	wStatus := httptest.NewRecorder()
	handleStatusAPI(wStatus, reqStatus)
	if wStatus.Code != http.StatusOK {
		t.Fatalf("expected 200 from status API, got %d", wStatus.Code)
	}
	var statusResp DaemonStatusJSON
	_ = json.Unmarshal(wStatus.Body.Bytes(), &statusResp)
	if statusResp.TotalActivities != 1 {
		t.Errorf("expected 1 total activity in status API, got %d", statusResp.TotalActivities)
	}

	// Activities API reads activity from dataDir
	reqActs := httptest.NewRequest(http.MethodGet, "/api/activities", nil)
	wActs := httptest.NewRecorder()
	handleActivitiesAPI(wActs, reqActs)
	if wActs.Code != http.StatusOK {
		t.Fatalf("expected 200 from activities API, got %d", wActs.Code)
	}
	var actsResp ActivitiesResponseJSON
	_ = json.Unmarshal(wActs.Body.Bytes(), &actsResp)
	if actsResp.Total != 1 || len(actsResp.Activities) != 1 || actsResp.Activities[0].ID != 7001 {
		t.Errorf("expected activities API to load activity 7001 from dataDir, got %+v", actsResp)
	}

	// Volume page should render 200 now
	reqVol := httptest.NewRequest(http.MethodGet, "/", nil)
	wVol := httptest.NewRecorder()
	handleVolumePage(wVol, reqVol)
	if wVol.Code != http.StatusOK {
		t.Errorf("expected 200 for volume page when configured, got %d", wVol.Code)
	}

	// Verify no config or metadata file was written to dataDir
	if _, err := os.Stat(filepath.Join(dataDir, ".talus-config.json")); err == nil {
		t.Errorf(".talus-config.json should not exist in dataDir")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "metadata.json")); err == nil {
		t.Errorf("metadata.json should not exist in dataDir")
	}
}
