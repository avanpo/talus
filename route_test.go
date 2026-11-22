package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSyntheticActivity writes a minimal Strava-format activity JSON with dense latlng & altitude streams.
func writeSyntheticActivity(t *testing.T, dir string, id int64, name string, waypoints [][3]float64) {
	t.Helper()
	actDir := filepath.Join(dir, "activities")
	if err := os.MkdirAll(actDir, 0755); err != nil {
		t.Fatalf("mkdir activities: %v", err)
	}

	// Interpolate waypoints every ~15 meters so grid snapping connects cells cleanly
	var latlng [][2]float64
	var alt []float64
	for i := 0; i < len(waypoints)-1; i++ {
		p0 := waypoints[i]
		p1 := waypoints[i+1]
		d := haversineM(p0[0], p0[1], p1[0], p1[1])
		steps := int(math.Ceil(d / 15.0))
		if steps < 1 {
			steps = 1
		}
		for s := 0; s < steps; s++ {
			frac := float64(s) / float64(steps)
			lat := p0[0] + frac*(p1[0]-p0[0])
			lon := p0[1] + frac*(p1[1]-p0[1])
			ele := p0[2] + frac*(p1[2]-p0[2])
			latlng = append(latlng, [2]float64{lat, lon})
			alt = append(alt, ele)
		}
	}
	last := waypoints[len(waypoints)-1]
	latlng = append(latlng, [2]float64{last[0], last[1]})
	alt = append(alt, last[2])

	payload := map[string]interface{}{
		"id":           id,
		"name":         name,
		"type":         "Run",
		"sport_type":   "TrailRun",
		"start_latlng": []float64{waypoints[0][0], waypoints[0][1]},
		"streams": map[string]interface{}{
			"latlng":   map[string]interface{}{"data": latlng},
			"altitude": map[string]interface{}{"data": alt},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal synthetic activity: %v", err)
	}
	fpath := filepath.Join(actDir, fmt.Sprintf("%d.json", id))
	if err := os.WriteFile(fpath, b, 0644); err != nil {
		t.Fatalf("write synthetic activity: %v", err)
	}
}

// makeLoopWaypoints constructs a diamond-shaped loop starting/ending at origin (lat0, lon0)
// in direction angleRad with approximate total perimeter perimM and summit elevation peakGainM.
func makeLoopWaypoints(lat0, lon0, angleRad, perimM, baseEle, peakGainM float64) [][3]float64 {
	proj := newLocalProj(lat0, lon0)
	// Diamond with 4 sides of length side = perimM / 4, width = 0.6*reach
	side := perimM / 4.0
	reach := side * 1.65 // distance from origin to far vertex
	halfW := math.Sqrt(math.Max(side*side-(reach/2)*(reach/2), 10000))

	cosA, sinA := math.Cos(angleRad), math.Sin(angleRad)
	perpX, perpY := -sinA, cosA

	ptsXY := [][3]float64{
		{0, 0, baseEle},
		{cosA*(reach/2) + perpX*halfW, sinA*(reach/2) + perpY*halfW, baseEle + peakGainM*0.5},
		{cosA * reach, sinA * reach, baseEle + peakGainM},
		{cosA*(reach/2) - perpX*halfW, sinA*(reach/2) - perpY*halfW, baseEle + peakGainM*0.5},
		{0, 0, baseEle},
	}

	out := make([][3]float64, len(ptsXY))
	for i, p := range ptsXY {
		lat, lon := proj.toLatLon(p[0], p[1])
		out[i] = [3]float64{lat, lon, p[2]}
	}
	return out
}

func TestRouteGraphMinRunsFilter(t *testing.T) {
	dir := t.TempDir()
	lat0, lon0 := 40.0150, -105.2705

	// Regular 10km trail loop with 2 runs (+200m gain)
	normalLoop := makeLoopWaypoints(lat0, lon0, 0.0, 10000, 1600, 200)
	writeSyntheticActivity(t, dir, 101, "Normal Trail 1", normalLoop)
	writeSyntheticActivity(t, dir, 102, "Normal Trail 2", normalLoop)

	// One-off bushwhack 10km loop with huge +800m gain (only 1 run!)
	bushwhack := makeLoopWaypoints(lat0, lon0, math.Pi, 10000, 1600, 800)
	writeSyntheticActivity(t, dir, 103, "Random Bushwhack", bushwhack)

	req := RouteBuildRequest{
		StartLat:         lat0,
		StartLon:         lon0,
		TargetDistanceKm: 10.0,
		ToleranceKm:      1.5,
		MaxOverlapPct:    5.0,
		MinRuns:          2,
		TimeoutSec:       3,
		MaxResults:       5,
	}
	if err := req.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	g, err := BuildTrailGraph(context.Background(), dir, req, nil)
	if err != nil {
		t.Fatalf("BuildTrailGraph: %v", err)
	}
	out, err := FindTopRoutes(context.Background(), g, req, nil)
	if err != nil {
		t.Fatalf("FindTopRoutes: %v", err)
	}
	if len(out.Routes) == 0 {
		t.Fatalf("expected at least 1 route")
	}
	// With min_runs=2, the 800m bushwhack must be excluded, so top gain should be ~200m (well under 400m)
	if out.Routes[0].ElevationGainM > 350 {
		t.Errorf("bushwhack loop leaked into min_runs=2 graph: got gain %.1fm", out.Routes[0].ElevationGainM)
	}

	// Now allow min_runs=1: the 800m bushwhack should become #1
	req.MinRuns = 1
	g1, err := BuildTrailGraph(context.Background(), dir, req, nil)
	if err != nil {
		t.Fatalf("BuildTrailGraph(min_runs=1): %v", err)
	}
	out1, err := FindTopRoutes(context.Background(), g1, req, nil)
	if err != nil {
		t.Fatalf("FindTopRoutes(min_runs=1): %v", err)
	}
	if out1.Routes[0].ElevationGainM < 600 {
		t.Errorf("expected bushwhack loop (~800m) when min_runs=1, got %.1fm", out1.Routes[0].ElevationGainM)
	}
}

func TestRouteBuilderTop5DistinctAndMaxElevation(t *testing.T) {
	dir := t.TempDir()
	lat0, lon0 := 40.0150, -105.2705

	// 5 distinct ~10km loops radiating in 5 directions with different elevation gains, each run twice
	specs := []struct {
		angle float64
		gain  float64
	}{
		{0.0, 420},
		{2 * math.Pi / 5, 350},
		{4 * math.Pi / 5, 280},
		{6 * math.Pi / 5, 210},
		{8 * math.Pi / 5, 150},
	}

	var id int64 = 1000
	for i, sp := range specs {
		wps := makeLoopWaypoints(lat0, lon0, sp.angle, 10000, 1600, sp.gain)
		writeSyntheticActivity(t, dir, id, fmt.Sprintf("Loop %d A", i), wps)
		id++
		writeSyntheticActivity(t, dir, id, fmt.Sprintf("Loop %d B", i), wps)
		id++
	}

	req := RouteBuildRequest{
		StartLat:         lat0,
		StartLon:         lon0,
		TargetDistanceKm: 10.0,
		ToleranceKm:      1.0,
		MaxOverlapPct:    5.0,
		MinRuns:          2,
		TimeoutSec:       4,
		MaxResults:       5,
	}
	if err := req.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	g, err := BuildTrailGraph(context.Background(), dir, req, nil)
	if err != nil {
		t.Fatalf("BuildTrailGraph: %v", err)
	}

	out, err := FindTopRoutes(context.Background(), g, req, nil)
	if err != nil {
		t.Fatalf("FindTopRoutes: %v", err)
	}

	if len(out.Routes) < 3 {
		t.Fatalf("expected at least 3 distinct routes, got %d", len(out.Routes))
	}

	distinctCount := 0
	for i, r := range out.Routes {
		if r.DistanceKm < 9.0 || r.DistanceKm > 11.0 {
			t.Errorf("route #%d distance %.2f km outside [9, 11] km", r.Rank, r.DistanceKm)
		}
		if r.OverlapPct > 5.01 {
			t.Errorf("route #%d overlap %.2f%% exceeds 5%%", r.Rank, r.OverlapPct)
		}
		if r.DistinctLabel == "Top Vert" || r.DistinctLabel == "Distinct" {
			distinctCount++
		}
		if i > 0 && r.ElevationGainM > out.Routes[i-1].ElevationGainM+1 {
			t.Errorf("routes not sorted by elevation gain descending: #%d (%.0fm) > #%d (%.0fm)",
				r.Rank, r.ElevationGainM, out.Routes[i-1].Rank, out.Routes[i-1].ElevationGainM)
		}
	}

	if distinctCount < 3 {
		t.Errorf("expected at least 3 mostly distinct routes, got %d", distinctCount)
	}

	// Verify #1 route picked the ~420m gain loop
	if out.Routes[0].ElevationGainM < 360 {
		t.Errorf("expected top route to pick highest-vert (~420m) loop, got %.0fm", out.Routes[0].ElevationGainM)
	}

	// Verify GPX output is valid XML with trackpoints
	gpxBytes, err := GenerateRouteGPX(&out.Routes[0])
	if err != nil {
		t.Fatalf("GenerateRouteGPX: %v", err)
	}
	var parsed gpxRoot
	if err := xml.Unmarshal(gpxBytes, &parsed); err != nil {
		t.Fatalf("invalid GPX XML: %v", err)
	}
	if len(parsed.Trk.Seg.Pts) < 20 {
		t.Errorf("expected dense GPX trackpoints, got %d", len(parsed.Trk.Seg.Pts))
	}
}

func TestRouteBuilderRepeatingHighVertLoops(t *testing.T) {
	dir := t.TempDir()
	lat0, lon0 := 40.0150, -105.2705

	// One ~10km mountain loop with +460m gain (run twice)
	mtnLoop := makeLoopWaypoints(lat0, lon0, 0.2, 10000, 1600, 460)
	writeSyntheticActivity(t, dir, 2001, "Mountain Loop 1", mtnLoop)
	writeSyntheticActivity(t, dir, 2002, "Mountain Loop 2", mtnLoop)

	// One ~10km flat loop with +100m gain (run twice)
	flatLoop := makeLoopWaypoints(lat0, lon0, math.Pi+0.2, 10000, 1600, 100)
	writeSyntheticActivity(t, dir, 2003, "Flat Loop 1", flatLoop)
	writeSyntheticActivity(t, dir, 2004, "Flat Loop 2", flatLoop)

	// Ask for a 30k ± 2k route with repeating loops allowed (>= 400m gain per loop)
	req := RouteBuildRequest{
		StartLat:         lat0,
		StartLon:         lon0,
		TargetDistanceKm: 30.0,
		ToleranceKm:      2.0,
		MaxOverlapPct:    5.0,
		MinRuns:          2,
		AllowRepeatLoops: true,
		MinLoopGainM:     400.0,
		TimeoutSec:       4,
		MaxResults:       5,
	}
	if err := req.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	g, err := BuildTrailGraph(context.Background(), dir, req, nil)
	if err != nil {
		t.Fatalf("BuildTrailGraph: %v", err)
	}

	out, err := FindTopRoutes(context.Background(), g, req, nil)
	if err != nil {
		t.Fatalf("FindTopRoutes (repeat loops): %v", err)
	}
	if len(out.Routes) == 0 {
		t.Fatalf("expected repeating 30k route")
	}

	top := out.Routes[0]
	if top.RepeatInfo == nil || top.RepeatInfo.Laps != 3 {
		t.Fatalf("expected 3-lap repeat route, got RepeatInfo=%+v", top.RepeatInfo)
	}
	if top.ElevationGainM < 1100 {
		t.Errorf("expected ~1380m gain for 3 laps of 460m loop, got %.0fm", top.ElevationGainM)
	}

	// Now raise MinLoopGainM to 600m: neither the 460m nor 100m loop qualifies, so 30k should find no valid route
	reqStrict := req
	reqStrict.MinLoopGainM = 600.0
	if _, err := FindTopRoutes(context.Background(), g, reqStrict, nil); err == nil {
		t.Errorf("expected error when no loop meets MinLoopGainM=600m for 30k target")
	}
}

func TestDetectFrequentStartsAndHTTPAPI(t *testing.T) {
	dir := t.TempDir()
	lat0, lon0 := 40.0150, -105.2705

	loopA := makeLoopWaypoints(lat0, lon0, 0.5, 10000, 1600, 300)
	writeSyntheticActivity(t, dir, 3001, "Home Run 1", loopA)
	writeSyntheticActivity(t, dir, 3002, "Home Run 2", loopA)
	writeSyntheticActivity(t, dir, 3003, "Home Run 3", loopA)

	starts := DetectFrequentStarts(dir, 5)
	if len(starts) != 1 || starts[0].Count != 3 {
		t.Fatalf("expected 1 start cluster with count 3, got %+v", starts)
	}

	// Wire up global daemon for HTTP handler tests
	prevDaemon := daemon
	daemon = newSyncDaemon(dir, time.Hour, 24*time.Hour, true)
	defer func() { daemon = prevDaemon }()

	// 1. GET /api/routes/starts
	rec := httptest.NewRecorder()
	handleRouteStartsAPI(rec, httptest.NewRequest(http.MethodGet, "/api/routes/starts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("starts API status = %d", rec.Code)
	}

	// 2. POST /api/routes/build
	body, _ := json.Marshal(RouteBuildRequest{
		StartLat:         lat0,
		StartLon:         lon0,
		TargetDistanceKm: 10.0,
		ToleranceKm:      1.5,
		MaxOverlapPct:    5.0,
		MinRuns:          2,
		TimeoutSec:       3,
		MaxResults:       5,
	})
	rec = httptest.NewRecorder()
	handleRouteBuildAPI(rec, httptest.NewRequest(http.MethodPost, "/api/routes/build", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("build API status = %d, body=%s", rec.Code, rec.Body.String())
	}

	// 3. Poll /api/routes/status until done
	deadline := time.Now().Add(10 * time.Second)
	var finalStatus RouteJobStatus
	for time.Now().Before(deadline) {
		rec = httptest.NewRecorder()
		handleRouteStatusAPI(rec, httptest.NewRequest(http.MethodGet, "/api/routes/status", nil))
		if err := json.Unmarshal(rec.Body.Bytes(), &finalStatus); err != nil {
			t.Fatalf("unmarshal status: %v", err)
		}
		if finalStatus.State == "done" || finalStatus.State == "error" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if finalStatus.State != "done" || finalStatus.Output == nil || len(finalStatus.Output.Routes) == 0 {
		t.Fatalf("expected job done with routes, got state=%q err=%q", finalStatus.State, finalStatus.Error)
	}

	// 4. GET /api/routes/gpx?rank=1
	rec = httptest.NewRecorder()
	handleRouteGPXAPI(rec, httptest.NewRequest(http.MethodGet, "/api/routes/gpx?rank=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("gpx API status = %d", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("<gpx")) {
		t.Errorf("GPX response missing <gpx root element")
	}
}

func TestRouteGraphBarometerOffsetAndJitterResistance(t *testing.T) {
	dir := t.TempDir()
	lat0, lon0 := 49.79809, -119.49037
	proj := newLocalProj(lat0, lon0)

	// True 10km loop with +200m elevation gain
	baseLoop := makeLoopWaypoints(lat0, lon0, 0.4, 10000, 600, 200)

	// Write 6 runs with different day-to-day barometric offsets (-40m..+35m) AND ±12m GPS jitter
	baroOffsets := []float64{-40, -15, 0, 12, 25, 35}
	for rIdx, offset := range baroOffsets {
		jittered := make([][3]float64, len(baseLoop))
		for i, wp := range baseLoop {
			x, y := proj.toXY(wp[0], wp[1])
			// Deterministic ±12m lateral jitter (not at start/end)
			if i > 0 && i < len(baseLoop)-1 {
				x += float64((rIdx%3)-1) * 10.0
				y += float64(((rIdx+i)%3)-1) * 10.0
			}
			lat, lon := proj.toLatLon(x, y)
			jittered[i] = [3]float64{lat, lon, wp[2] + offset}
		}
		writeSyntheticActivity(t, dir, int64(4000+rIdx), fmt.Sprintf("Jittered Baro Run %d", rIdx), jittered)
	}

	// Add 1 corrupted track with huge fake altitude spikes (+250m)
	corrupted := make([][3]float64, len(baseLoop))
	copy(corrupted, baseLoop)
	corrupted[1][2] += 250
	corrupted[3][2] += 250
	writeSyntheticActivity(t, dir, 4099, "Corrupted Alt Track", corrupted)

	req := RouteBuildRequest{
		StartLat:         lat0,
		StartLon:         lon0,
		TargetDistanceKm: 10.0,
		ToleranceKm:      1.0,
		MaxOverlapPct:    5.0,
		MinRuns:          2,
		TimeoutSec:       3,
		MaxResults:       5,
	}
	if err := req.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	g, err := BuildTrailGraph(context.Background(), dir, req, nil)
	if err != nil {
		t.Fatalf("BuildTrailGraph: %v", err)
	}
	// Jittered runs should collapse cleanly instead of creating hundreds of fake braided junctions
	if len(g.Nodes) > 30 {
		t.Errorf("expected braided jitter to collapse cleanly (<30 junctions), got %d junctions / %d edges", len(g.Nodes), len(g.Edges))
	}

	out, err := FindTopRoutes(context.Background(), g, req, nil)
	if err != nil {
		t.Fatalf("FindTopRoutes: %v", err)
	}
	if len(out.Routes) == 0 {
		t.Fatalf("expected clean <=5%% overlap loop to be found")
	}
	top := out.Routes[0]
	if top.OverlapPct > 5.0 {
		t.Errorf("expected <= 5%% overlap, got %.1f%%", top.OverlapPct)
	}
	if top.ElevationGainM < 150 || top.ElevationGainM > 260 {
		t.Errorf("expected true ~200m elevation gain (immune to baro offsets & corrupted spike), got %.0fm", top.ElevationGainM)
	}
}
