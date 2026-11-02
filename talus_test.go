package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// MockRoundTripper intercepts HTTP requests for testing.
type MockRoundTripper struct {
	RoundTripFunc func(req *http.Request) (*http.Response, error)
}

func (m *MockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.RoundTripFunc(req)
}

func TestConfigAndMetadata(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "talus-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Test Config
	cfg := &Config{
		ClientID:     "12345",
		ClientSecret: "secret123",
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Truncate(time.Second),
	}

	if err := SaveConfig(tmpDir, cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	loadedCfg, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if loadedCfg.ClientID != cfg.ClientID || loadedCfg.ClientSecret != cfg.ClientSecret ||
		loadedCfg.AccessToken != cfg.AccessToken || loadedCfg.RefreshToken != cfg.RefreshToken ||
		!loadedCfg.ExpiresAt.Equal(cfg.ExpiresAt) {
		t.Errorf("loaded config does not match saved config:\nGot: %+v\nWant: %+v", loadedCfg, cfg)
	}

	// Test Metadata
	meta := &Metadata{
		LastActivityTimestamp: 1627400000,
		LastSyncTime:          time.Now().Truncate(time.Second),
		TotalActivities:       42,
	}

	if err := SaveMetadata(tmpDir, meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	loadedMeta, err := LoadMetadata(tmpDir)
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}

	if loadedMeta.LastActivityTimestamp != meta.LastActivityTimestamp ||
		loadedMeta.TotalActivities != meta.TotalActivities ||
		!loadedMeta.LastSyncTime.Equal(meta.LastSyncTime) {
		t.Errorf("loaded metadata does not match saved metadata:\nGot: %+v\nWant: %+v", loadedMeta, meta)
	}
}

func TestClient_EnsureValidToken_Refresh(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "talus-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save expired config
	cfg := &Config{
		ClientID:     "12345",
		ClientSecret: "secret123",
		AccessToken:  "old-access",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.Now().Add(-1 * time.Hour), // Expired
	}
	if err := SaveConfig(tmpDir, cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	client := &StravaClient{
		httpClient: &http.Client{Timeout: 5 * time.Second},
		configDir:  tmpDir,
		config:     cfg,
	}

	// Mock HTTP client to intercept token refresh call
	refreshCalled := false
	client.httpClient.Transport = &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			if req.URL.String() == "https://www.strava.com/oauth/token" {
				refreshCalled = true
				if err := req.ParseForm(); err != nil {
					t.Errorf("failed to parse form: %v", err)
				}
				if req.FormValue("client_id") != "12345" || req.FormValue("client_secret") != "secret123" ||
					req.FormValue("refresh_token") != "old-refresh" || req.FormValue("grant_type") != "refresh_token" {
					t.Errorf("unexpected form values: %v", req.Form)
				}

				respJSON := `{
					"access_token": "new-access",
					"refresh_token": "new-refresh",
					"expires_at": 1900000000
				}`
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(respJSON)),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{StatusCode: 404}, nil
		},
	}

	if err := client.ensureValidToken(); err != nil {
		t.Fatalf("ensureValidToken failed: %v", err)
	}

	if !refreshCalled {
		t.Error("expected token refresh HTTP call but was not made")
	}

	// Verify local config was updated
	newCfg, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("failed to load updated config: %v", err)
	}

	if newCfg.AccessToken != "new-access" || newCfg.RefreshToken != "new-refresh" || newCfg.ExpiresAt.Unix() != 1900000000 {
		t.Errorf("config was not correctly updated and saved. Got: %+v", newCfg)
	}
}

func TestSyncActivities_IdempotencyAndFlow(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "talus-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Save valid config
	cfg := &Config{
		ClientID:     "123",
		ClientSecret: "sec",
		AccessToken:  "tok",
		RefreshToken: "ref",
		ExpiresAt:    time.Now().Add(10 * time.Hour), // Valid
	}
	if err := SaveConfig(tmpDir, cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// Save empty initial metadata (starts at epoch 1600000000)
	meta := &Metadata{
		LastActivityTimestamp: 1600000000,
	}
	if err := SaveMetadata(tmpDir, meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	listCalled := 0
	detailsFetched := make(map[string]int)

	// Set up our mock transport hook in the package
	testTransport = &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			// Intercept list activities endpoint
			if req.URL.Path == "/api/v3/athlete/activities" {
				listCalled++
				query := req.URL.Query()
				if query.Get("after") != "1600000000" {
					t.Errorf("expected after=1600000000, got: %s", query.Get("after"))
				}

				page := query.Get("page")
				if page == "1" {
					// Return 2 mock summary activities
					activitiesJSON := `[
						{"id": 1001, "start_date": "2020-09-20T12:00:00Z"},
						{"id": 1002, "start_date": "2020-09-21T12:00:00Z"}
					]`
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewBufferString(activitiesJSON)),
						Header:     make(http.Header),
					}, nil
				} else if page == "2" {
					// End of pagination
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewBufferString("[]")),
						Header:     make(http.Header),
					}, nil
				}
				t.Errorf("unexpected page requested: %s", page)
			}

			// Intercept detailed activity endpoint
			if req.URL.Path == "/api/v3/activities/1001" {
				detailsFetched["1001"]++
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"id": 1001, "name": "Morning Ride", "distance": 15000}`)),
					Header:     make(http.Header),
				}, nil
			}
			if req.URL.Path == "/api/v3/activities/1001/streams" {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"time":{"data":[1,2,3]}}`)),
					Header:     make(http.Header),
				}, nil
			}
			if req.URL.Path == "/api/v3/activities/1002" {
				detailsFetched["1002"]++
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"id": 1002, "name": "Evening Run", "distance": 5000}`)),
					Header:     make(http.Header),
				}, nil
			}
			if req.URL.Path == "/api/v3/activities/1002/streams" {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"time":{"data":[4,5,6]}}`)),
					Header:     make(http.Header),
				}, nil
			}

			return &http.Response{StatusCode: 404}, nil
		},
	}
	// Clean up global hook after test
	defer func() { testTransport = nil }()

	// First Run: Should download both activities
	if err := SyncActivities(tmpDir, false); err != nil {
		t.Fatalf("first SyncActivities failed: %v", err)
	}

	if listCalled != 2 {
		t.Errorf("expected list activities API to be fetched 2 times (page 1 and page 2), was fetched %d times", listCalled)
	}

	if detailsFetched["1001"] != 1 {
		t.Errorf("expected detail endpoint for 1001 to be called exactly once, was: %d", detailsFetched["1001"])
	}
	if detailsFetched["1002"] != 1 {
		t.Errorf("expected detail endpoint for 1002 to be called exactly once, was: %d", detailsFetched["1002"])
	}

	// Verify metadata was updated correctly to the newest activity's start_date (2020-09-21T12:00:00Z -> Unix 1600689600)
	updatedMeta, err := LoadMetadata(tmpDir)
	if err != nil {
		t.Fatalf("failed to load updated metadata: %v", err)
	}

	expectedTimestamp := int64(1600689600) // 2020-09-21 12:00:00 UTC
	if updatedMeta.LastActivityTimestamp != expectedTimestamp {
		t.Errorf("expected last activity timestamp to be advanced to %d, got: %d", expectedTimestamp, updatedMeta.LastActivityTimestamp)
	}
	if updatedMeta.TotalActivities != 2 {
		t.Errorf("expected TotalActivities in metadata to be 2, got: %d", updatedMeta.TotalActivities)
	}

	// Verify files actually exist on disk
	f1 := filepath.Join(tmpDir, "activities", "1001.json")
	if _, err := os.Stat(f1); err != nil {
		t.Errorf("activity 1001 file was not saved: %v", err)
	}
	f2 := filepath.Join(tmpDir, "activities", "1002.json")
	if _, err := os.Stat(f2); err != nil {
		t.Errorf("activity 1002 file was not saved: %v", err)
	}

	// Reset counters for Second Run (Idempotency check)
	listCalled = 0
	detailsFetched["1001"] = 0
	detailsFetched["1002"] = 0

	// Set up mock transport again but with the new updated timestamp
	testTransport = &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/api/v3/athlete/activities" {
				listCalled++
				query := req.URL.Query()
				if query.Get("after") != "1600689600" {
					t.Errorf("expected after=1600689600 (advanced timestamp), got: %s", query.Get("after"))
				}

				page := query.Get("page")
				if page == "1" {
					// Return the same list of activities to see if it gracefully skips them without re-downloading details
					activitiesJSON := `[
						{"id": 1001, "start_date": "2020-09-20T12:00:00Z"},
						{"id": 1002, "start_date": "2020-09-21T12:00:00Z"}
					]`
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewBufferString(activitiesJSON)),
						Header:     make(http.Header),
					}, nil
				} else if page == "2" {
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewBufferString("[]")),
						Header:     make(http.Header),
					}, nil
				}
			}

			// These should NEVER be called on the second run because files already exist!
			if req.URL.Path == "/api/v3/activities/1001" {
				detailsFetched["1001"]++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString("{}"))}, nil
			}
			if req.URL.Path == "/api/v3/activities/1002" {
				detailsFetched["1002"]++
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString("{}"))}, nil
			}

			return &http.Response{StatusCode: 404}, nil
		},
	}

	// Run Sync again
	if err := SyncActivities(tmpDir, false); err != nil {
		t.Fatalf("second SyncActivities failed: %v", err)
	}

	if listCalled != 2 {
		t.Errorf("second run: expected list activities API to be fetched 2 times, was fetched %d times", listCalled)
	}

	if detailsFetched["1001"] != 0 {
		t.Errorf("idempotency failure: detailed activity 1001 fetched again despite already backed up!")
	}
	if detailsFetched["1002"] != 0 {
		t.Errorf("idempotency failure: detailed activity 1002 fetched again despite already backed up!")
	}
}

func TestSyncActivities_DailyRateLimit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "talus-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &Config{
		ClientID:     "123",
		ClientSecret: "sec",
		AccessToken:  "tok",
		RefreshToken: "ref",
		ExpiresAt:    time.Now().Add(10 * time.Hour),
	}
	if err := SaveConfig(tmpDir, cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	meta := &Metadata{
		LastActivityTimestamp: 1600000000,
	}
	if err := SaveMetadata(tmpDir, meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	testTransport = &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/api/v3/athlete/activities" {
				headers := make(http.Header)
				headers.Set("X-RateLimit-Limit", "100,1000")
				headers.Set("X-RateLimit-Usage", "50,1000") // Daily limit met
				return &http.Response{
					StatusCode: 429,
					Body:       io.NopCloser(bytes.NewBufferString(`{}`)),
					Header:     headers,
				}, nil
			}
			return &http.Response{StatusCode: 404}, nil
		},
	}
	defer func() { testTransport = nil }()

	err = SyncActivities(tmpDir, false)
	if err == nil {
		t.Fatal("expected daily rate limit error, got nil")
	}

	if !isDailyRateLimit(err) {
		t.Errorf("expected error to be daily rate limit error, got: %v", err)
	}
}

func TestSyncActivities_Force(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "talus-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &Config{
		ClientID:     "123",
		ClientSecret: "sec",
		AccessToken:  "tok",
		RefreshToken: "ref",
		ExpiresAt:    time.Now().Add(10 * time.Hour),
	}
	if err := SaveConfig(tmpDir, cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// Metadata indicating last synced up to 1600689600
	meta := &Metadata{
		LastActivityTimestamp: 1600689600,
		TotalActivities:       1,
	}
	if err := SaveMetadata(tmpDir, meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	activitiesDir := filepath.Join(tmpDir, "activities")
	if err := os.MkdirAll(activitiesDir, 0755); err != nil {
		t.Fatalf("failed to create activities dir: %v", err)
	}
	// Write dummy existing file for 1001
	if err := os.WriteFile(filepath.Join(activitiesDir, "1001.json"), []byte(`{"id": 1001, "name": "old"}`), 0644); err != nil {
		t.Fatalf("failed to write dummy file: %v", err)
	}

	listCalled := 0
	detailsFetched := 0

	testTransport = &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/api/v3/athlete/activities" {
				listCalled++
				query := req.URL.Query()
				if query.Get("after") != "0" {
					t.Errorf("expected after=0 when force=true, got: %s", query.Get("after"))
				}
				page := query.Get("page")
				if page == "1" {
					activitiesJSON := `[
						{"id": 1001, "start_date": "2020-09-20T12:00:00Z"}
					]`
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewBufferString(activitiesJSON)),
						Header:     make(http.Header),
					}, nil
				} else if page == "2" {
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewBufferString("[]")),
						Header:     make(http.Header),
					}, nil
				}
			}

			if req.URL.Path == "/api/v3/activities/1001" {
				detailsFetched++
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"id": 1001, "name": "refreshed with streams"}`)),
					Header:     make(http.Header),
				}, nil
			}
			if req.URL.Path == "/api/v3/activities/1001/streams" {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`{"heartrate":{"data":[120,125]}}`)),
					Header:     make(http.Header),
				}, nil
			}

			return &http.Response{StatusCode: 404}, nil
		},
	}
	defer func() { testTransport = nil }()

	// Run sync with force = true
	if err := SyncActivities(tmpDir, true); err != nil {
		t.Fatalf("SyncActivities with force failed: %v", err)
	}

	if listCalled != 2 {
		t.Errorf("expected list activities called 2 times (page 1 and 2), got %d", listCalled)
	}

	if detailsFetched != 1 {
		t.Errorf("expected details for 1001 to be fetched (overwritten) when force=true, got %d", detailsFetched)
	}

	// Verify file was overwritten
	content, err := os.ReadFile(filepath.Join(activitiesDir, "1001.json"))
	if err != nil {
		t.Fatalf("failed to read activity file: %v", err)
	}
	if !bytes.Contains(content, []byte("refreshed with streams")) {
		t.Errorf("expected file content to be overwritten with new details/streams, got: %s", string(content))
	}
}

func TestSeparatedDirectories_SyncAndStore(t *testing.T) {
	// Create three separate directories: dataDir, configDir, metadataDir
	dataDir, err := os.MkdirTemp("", "talus-data-*")
	if err != nil {
		t.Fatalf("failed to create data dir: %v", err)
	}
	defer os.RemoveAll(dataDir)

	configDir, err := os.MkdirTemp("", "talus-config-*")
	if err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	defer os.RemoveAll(configDir)

	metadataDir, err := os.MkdirTemp("", "talus-meta-*")
	if err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	defer os.RemoveAll(metadataDir)

	// Save OAuth config ONLY in configDir
	cfg := &Config{
		ClientID:     "client123",
		ClientSecret: "secret456",
		AccessToken:  "valid_access_token",
		RefreshToken: "valid_refresh_token",
		ExpiresAt:    time.Now().Add(10 * time.Hour),
	}
	if err := SaveConfig(configDir, cfg); err != nil {
		t.Fatalf("failed to save config to configDir: %v", err)
	}

	// Verify dataDir does NOT have config
	if _, err := LoadConfig(dataDir); err == nil {
		t.Fatalf("dataDir should not have config file")
	}

	// Mock Strava API to return 1 activity
	testTransport = &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/api/v3/athlete/activities" {
				if req.URL.Query().Get("page") == "1" {
					jsonBody := `[
						{
							"id": 9001,
							"name": "Trail Run on Separate Data Volume",
							"distance": 8500.0,
							"moving_time": 2700,
							"elapsed_time": 2800,
							"total_elevation_gain": 320.0,
							"type": "Run",
							"sport_type": "TrailRun",
							"start_date": "2026-05-10T14:00:00Z"
						}
					]`
					return &http.Response{
						StatusCode: 200,
						Body:       io.NopCloser(bytes.NewBufferString(jsonBody)),
						Header:     make(http.Header),
					}, nil
				}
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString("[]")),
					Header:     make(http.Header),
				}, nil
			}
			if req.URL.Path == "/api/v3/activities/9001" {
				detailJSON := `{
					"id": 9001,
					"name": "Trail Run on Separate Data Volume",
					"distance": 8500.0,
					"moving_time": 2700,
					"elapsed_time": 2800,
					"total_elevation_gain": 320.0,
					"type": "Run",
					"sport_type": "TrailRun",
					"start_date": "2026-05-10T14:00:00Z"
				}`
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(detailJSON)),
					Header:     make(http.Header),
				}, nil
			}
			if req.URL.Path == "/api/v3/activities/9001/streams" {
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(bytes.NewBufferString(`null`)),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{StatusCode: 404}, nil
		},
	}
	defer func() { testTransport = nil }()

	// Execute sync with separate directories
	opts := SyncOptions{
		DataDir:     dataDir,
		ConfigDir:   configDir,
		MetadataDir: metadataDir,
	}
	if err := SyncActivitiesWithOptions(dataDir, opts); err != nil {
		t.Fatalf("SyncActivitiesWithOptions failed: %v", err)
	}

	// 1. Check raw JSON data was saved to dataDir/activities/9001.json
	actFile := filepath.Join(dataDir, "activities", "9001.json")
	if _, err := os.Stat(actFile); err != nil {
		t.Fatalf("expected raw activity JSON to exist at %s: %v", actFile, err)
	}

	// 2. Check metadata was saved in metadataDir
	metaFile := filepath.Join(metadataDir, "metadata.json")
	if _, err := os.Stat(metaFile); err != nil {
		t.Fatalf("expected metadata to exist at %s: %v", metaFile, err)
	}
	meta, err := LoadMetadata(metadataDir)
	if err != nil || meta.TotalActivities != 1 {
		t.Errorf("expected metadata in metadataDir to have 1 total activity, got: %+v (err: %v)", meta, err)
	}

	// 3. Check dataDir does NOT contain metadata.json or .talus-config.json
	if _, err := os.Stat(filepath.Join(dataDir, "metadata.json")); err == nil {
		t.Errorf("metadata.json should NOT be present in dataDir when metadataDir is separated")
	}
	if _, err := os.Stat(filepath.Join(dataDir, ".talus-config.json")); err == nil {
		t.Errorf(".talus-config.json should NOT be present in dataDir when configDir is separated")
	}

	// 4. Verify ActivityStore loads from dataDir
	store := GetGlobalStore(dataDir)
	acts, total, err := store.GetActivities(10, "all", "")
	if err != nil {
		t.Fatalf("store.GetActivities failed: %v", err)
	}
	if total != 1 || len(acts) != 1 || acts[0].ID != 9001 {
		t.Errorf("expected store to load activity 9001 from dataDir, got %d activities", len(acts))
	}
}

func TestResolveDirectories(t *testing.T) {
	// Case 1: Positional arg only
	data, config, meta := resolveDirectories("/pos/path", "", "", "")
	if data != "/pos/path" || config != "/pos/path" || meta != "/pos/path" {
		t.Errorf("expected all dirs to match positional path, got data=%s config=%s meta=%s", data, config, meta)
	}

	// Case 2: Separate config-dir flag
	data, config, meta = resolveDirectories("/pos/path", "", "/my/config", "")
	if data != "/pos/path" || config != "/my/config" || meta != "/my/config" {
		t.Errorf("expected config and meta to match flag, got data=%s config=%s meta=%s", data, config, meta)
	}

	// Case 3: Explicit data-dir, config-dir, metadata-dir flags
	data, config, meta = resolveDirectories("", "/custom/data", "/custom/config", "/custom/meta")
	if data != "/custom/data" || config != "/custom/config" || meta != "/custom/meta" {
		t.Errorf("expected custom dirs, got data=%s config=%s meta=%s", data, config, meta)
	}

	// Case 4: Environment variables
	os.Setenv("DATA_DIR", "/env/data")
	os.Setenv("CONFIG_DIR", "/env/config")
	os.Setenv("METADATA_DIR", "/env/meta")
	defer func() {
		os.Unsetenv("DATA_DIR")
		os.Unsetenv("CONFIG_DIR")
		os.Unsetenv("METADATA_DIR")
	}()

	data, config, meta = resolveDirectories("", "", "", "")
	if data != "/env/data" || config != "/env/config" || meta != "/env/meta" {
		t.Errorf("expected env dirs, got data=%s config=%s meta=%s", data, config, meta)
	}
}

