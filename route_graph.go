package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	earthRadiusM = 6371000.0
	degToRad     = math.Pi / 180.0
	radToDeg     = 180.0 / math.Pi
)

// RouteBuildRequest configures a trail-graph build and route search job.
type RouteBuildRequest struct {
	StartLat         float64 `json:"start_lat"`
	StartLon         float64 `json:"start_lon"`
	TargetDistanceKm float64 `json:"target_distance_km"` // default 10.0
	ToleranceKm      float64 `json:"tolerance_km"`       // default 1.0
	MaxOverlapPct    float64 `json:"max_overlap_pct"`    // default 5.0 (0..100)
	MinRuns          int     `json:"min_runs"`           // default 2
	SportFilter      string  `json:"sport_filter"`       // "run" (default), "foot" (run+hike+walk), "all"
	AllowRepeatLoops bool    `json:"allow_repeat_loops"` // default false
	MinLoopGainM     float64 `json:"min_loop_gain_m"`    // default 400.0
	SnapRadiusM      float64 `json:"snap_radius_m"`      // default 35.0
	TimeoutSec       int     `json:"timeout_sec"`        // default 15
	MaxResults       int     `json:"max_results"`        // default 5
	MaxCPUs          int     `json:"max_cpus"`           // 0 = auto (~25% of NumCPU, min 1)
}

// Normalize fills in sensible defaults and clamps out-of-range values.
func (r *RouteBuildRequest) Normalize() error {
	if math.IsNaN(r.StartLat) || math.IsNaN(r.StartLon) ||
		r.StartLat < -90 || r.StartLat > 90 || r.StartLon < -180 || r.StartLon > 180 ||
		(r.StartLat == 0 && r.StartLon == 0) {
		return fmt.Errorf("valid start coordinates (start_lat, start_lon) are required")
	}
	if r.TargetDistanceKm <= 0 {
		r.TargetDistanceKm = 10.0
	}
	if r.TargetDistanceKm < 1.0 {
		r.TargetDistanceKm = 1.0
	}
	if r.TargetDistanceKm > 150.0 {
		r.TargetDistanceKm = 150.0
	}
	if r.ToleranceKm <= 0 {
		r.ToleranceKm = 1.0
	}
	if r.ToleranceKm >= r.TargetDistanceKm*0.8 {
		r.ToleranceKm = r.TargetDistanceKm * 0.5
	}
	if r.MaxOverlapPct < 0 {
		r.MaxOverlapPct = 0
	}
	if r.MaxOverlapPct > 100 {
		r.MaxOverlapPct = 100
	}
	if r.MinRuns <= 0 {
		r.MinRuns = 2
	}
	if r.SportFilter == "" {
		r.SportFilter = "run"
	}
	r.SportFilter = strings.ToLower(strings.TrimSpace(r.SportFilter))
	if r.SportFilter != "run" && r.SportFilter != "foot" && r.SportFilter != "all" {
		r.SportFilter = "run"
	}
	if r.MinLoopGainM <= 0 {
		r.MinLoopGainM = 400.0
	}
	if r.SnapRadiusM <= 0 {
		r.SnapRadiusM = 30.0
	}
	if r.SnapRadiusM < 15.0 {
		r.SnapRadiusM = 15.0
	}
	if r.SnapRadiusM > 100.0 {
		r.SnapRadiusM = 100.0
	}
	if r.TimeoutSec <= 0 {
		r.TimeoutSec = 15
	}
	if r.TimeoutSec > 120 {
		r.TimeoutSec = 120
	}
	if r.MaxResults <= 0 {
		r.MaxResults = 5
	}
	if r.MaxResults > 10 {
		r.MaxResults = 10
	}
	if r.MaxCPUs <= 0 {
		r.MaxCPUs = defaultRouteWorkers()
	}
	return nil
}

func defaultRouteWorkers() int {
	w := runtime.NumCPU() / 4
	if w < 1 {
		w = 1
	}
	return w
}

// StartCluster represents a frequently used starting coordinate detected from activities.
type StartCluster struct {
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	Count      int     `json:"count"`
	SampleName string  `json:"sample_name"`
}

// GraphNode is a junction (or start anchor) in the simplified personal trail network.
type GraphNode struct {
	ID    int
	Lat   float64
	Lon   float64
	Ele   float64
	X     float64
	Y     float64
	Edges []int // indices into TrailGraph.Edges
}

// GraphEdge is a simplified trail segment between two junctions.
type GraphEdge struct {
	ID            int
	U             int
	V             int
	Length        float64      // meters
	GainUV        float64      // elevation gain going U -> V (meters)
	GainVU        float64      // elevation gain going V -> U (meters)
	NetDeltaUV    float64      // net elevation change U -> V (meters)
	RunCount      int          // unique activity count supporting this segment
	Points        [][3]float64 // ordered [lat, lon, ele] from U to V
	StartDirX     float64      // unit vector leaving U toward V
	StartDirY     float64
	EndDirX       float64 // unit vector arriving at V from U
	EndDirY       float64
	CorridorCells []gridKey // interior 45m spatial cells for proximity overlap detection
}

// Other returns the opposite endpoint of the edge from node u.
func (e *GraphEdge) Other(u int) int {
	if e.U == u {
		return e.V
	}
	return e.U
}

// GainFrom returns the directional elevation gain when traversing starting at node u.
func (e *GraphEdge) GainFrom(u int) float64 {
	if e.U == u {
		return e.GainUV
	}
	return e.GainVU
}

// LeaveDir returns the unit direction vector when leaving node u along this edge.
func (e *GraphEdge) LeaveDir(u int) (float64, float64) {
	if e.U == u {
		return e.StartDirX, e.StartDirY
	}
	return -e.EndDirX, -e.EndDirY
}

// ArriveDir returns the unit direction vector when arriving at node v along this edge.
func (e *GraphEdge) ArriveDir(v int) (float64, float64) {
	if e.V == v {
		return e.EndDirX, e.EndDirY
	}
	return -e.StartDirX, -e.StartDirY
}

// TrailGraph is the personal trail network built from historical GPS traces.
type TrailGraph struct {
	OriginLat        float64
	OriginLon        float64
	StartNode        int
	SnapDistanceM    float64
	Nodes            []GraphNode
	Edges            []GraphEdge
	HistoricalLoops  [][]RouteStep // historical home-start loops projected onto this graph
	ActivitiesLoaded int
	ActivitiesUsed   int
	BuildDuration    time.Duration
}

// NetworkPolyline is a compact representation of an edge for optional UI trail-network overlay.
type NetworkPolyline struct {
	RunCount int          `json:"run_count"`
	Points   [][2]float64 `json:"points"` // [lat, lon]
}

// ExportNetworkOverlay returns simplified polylines for rendering the trail network on the map.
func (g *TrailGraph) ExportNetworkOverlay(maxEdges int) []NetworkPolyline {
	if g == nil || len(g.Edges) == 0 {
		return nil
	}
	type edgeRef struct {
		idx      int
		runCount int
		length   float64
	}
	refs := make([]edgeRef, len(g.Edges))
	for i, e := range g.Edges {
		refs[i] = edgeRef{idx: i, runCount: e.RunCount, length: e.Length}
	}
	if maxEdges > 0 && len(refs) > maxEdges {
		sort.Slice(refs, func(i, j int) bool {
			if refs[i].runCount != refs[j].runCount {
				return refs[i].runCount > refs[j].runCount
			}
			return refs[i].length > refs[j].length
		})
		refs = refs[:maxEdges]
	}
	out := make([]NetworkPolyline, 0, len(refs))
	for _, r := range refs {
		e := g.Edges[r.idx]
		pts := make([][2]float64, 0, len(e.Points))
		step := 1
		if len(e.Points) > 40 {
			step = len(e.Points) / 40
		}
		for i := 0; i < len(e.Points); i += step {
			pts = append(pts, [2]float64{e.Points[i][0], e.Points[i][1]})
		}
		last := e.Points[len(e.Points)-1]
		if len(pts) == 0 || pts[len(pts)-1][0] != last[0] || pts[len(pts)-1][1] != last[1] {
			pts = append(pts, [2]float64{last[0], last[1]})
		}
		out = append(out, NetworkPolyline{
			RunCount: e.RunCount,
			Points:   pts,
		})
	}
	return out
}

// lightweight struct for fast, low-memory unmarshaling of activity JSON files.
type routeActivityFile struct {
	ID                 int64     `json:"id"`
	Name               string    `json:"name"`
	Type               string    `json:"type"`
	SportType          string    `json:"sport_type"`
	TotalElevationGain float64   `json:"total_elevation_gain"`
	StartLatLng        []float64 `json:"start_latlng"`
	Streams     struct {
		LatLng struct {
			Data [][2]float64 `json:"data"`
		} `json:"latlng"`
		Altitude struct {
			Data []float64 `json:"data"`
		} `json:"altitude"`
	} `json:"streams"`
}

func matchesRouteSportFilter(actType, sportType, name, filter string) bool {
	dummy := &ActivityDetail{
		Type:      actType,
		SportType: sportType,
		Name:      name,
	}
	if IsTreadmillRun(dummy) || IsVirtualRide(dummy) {
		return false
	}
	switch filter {
	case "all":
		return true
	case "foot":
		return IsRunActivity(dummy) ||
			actType == "Hike" || sportType == "Hike" ||
			actType == "Walk" || sportType == "Walk"
	default: // "run"
		return IsRunActivity(dummy)
	}
}

type localProj struct {
	lat0    float64
	lon0    float64
	cosLat0 float64
}

func newLocalProj(lat0, lon0 float64) localProj {
	return localProj{
		lat0:    lat0,
		lon0:    lon0,
		cosLat0: math.Cos(lat0 * degToRad),
	}
}

func (p localProj) toXY(lat, lon float64) (float64, float64) {
	x := (lon - p.lon0) * degToRad * earthRadiusM * p.cosLat0
	y := (lat - p.lat0) * degToRad * earthRadiusM
	return x, y
}

func (p localProj) toLatLon(x, y float64) (float64, float64) {
	lat := p.lat0 + (y/earthRadiusM)*radToDeg
	lon := p.lon0 + (x/(earthRadiusM*p.cosLat0))*radToDeg
	return lat, lon
}

func haversineM(lat1, lon1, lat2, lon2 float64) float64 {
	dLat := (lat2 - lat1) * degToRad
	dLon := (lon2 - lon1) * degToRad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*degToRad)*math.Cos(lat2*degToRad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusM * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

type gridKey struct {
	gx int32
	gy int32
}

type gridEdgeKey struct {
	a gridKey
	b gridKey
}

func makeGridEdgeKey(u, v gridKey) (gridEdgeKey, bool) {
	if u.gx < v.gx || (u.gx == v.gx && u.gy < v.gy) {
		return gridEdgeKey{a: u, b: v}, true
	}
	return gridEdgeKey{a: v, b: u}, false
}

type cellAccum struct {
	sumX   float64
	sumY   float64
	count  int
	sumAlt float64
	altCnt int
}

// edgeDeltaSample stores a single activity's observed relative elevation change (b - a) on an edge.
type edgeDeltaAccum struct {
	runCount int
	deltas   []float64 // canonical delta (alt(b) - alt(a)), capped for memory
}

type historicalLoopTrace struct {
	gainM float64
	cells []gridKey
}

type activityGridExtract struct {
	actID      int64
	cells      map[gridKey]cellAccum
	edgeDeltas map[gridEdgeKey]float64 // canonical grade (dEle / dist)
	edgeHasEle map[gridEdgeKey]bool
	loopTrace  *historicalLoopTrace // non-nil if activity starts and ends near origin
}

// graphCache avoids re-parsing 1,100+ JSON files when tweaking route distance/overlap/loop params.
type graphCacheKey struct {
	dataDir     string
	dirSig      string
	startLatQ   int64
	startLonQ   int64
	maxRadiusQ  int
	snapRadiusQ int
	minRuns     int
	sportFilter string
}

var (
	routeGraphCacheMu sync.Mutex
	routeGraphCache   *struct {
		key   graphCacheKey
		graph *TrailGraph
	}
)

func computeActivitiesDirSignature(activitiesDir string) (string, []string, error) {
	entries, err := os.ReadDir(activitiesDir)
	if err != nil {
		return "", nil, err
	}
	var paths []string
	var maxMod int64
	var totalSize int64
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		paths = append(paths, filepath.Join(activitiesDir, e.Name()))
		if info, err := e.Info(); err == nil {
			if mt := info.ModTime().Unix(); mt > maxMod {
				maxMod = mt
			}
			totalSize += info.Size()
		}
	}
	sig := fmt.Sprintf("%d:%d:%d", len(paths), maxMod, totalSize)
	return sig, paths, nil
}

// BuildTrailGraph streams activity JSON files within the reachable bounding box and constructs
// a clean, topologically simplified undirected trail graph with robust median relative elevations.
func BuildTrailGraph(ctx context.Context, dataDir string, req RouteBuildRequest, progressFn func(string)) (*TrailGraph, error) {
	startTime := time.Now()
	activitiesDir := filepath.Join(dataDir, "activities")
	dirSig, files, err := computeActivitiesDirSignature(activitiesDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read activities directory %s: %w", activitiesDir, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no activity JSON files found in %s", activitiesDir)
	}

	maxDistM := (req.TargetDistanceKm + req.ToleranceKm) * 1000.0
	// A loop of length maxDistM cannot reach further than maxDistM/2 from start; add a 1km buffer.
	maxRadiusM := maxDistM/2.0 + 1000.0
	if maxRadiusM < 3000.0 {
		maxRadiusM = 3000.0
	}

	cacheKey := graphCacheKey{
		dataDir:     dataDir,
		dirSig:      dirSig,
		startLatQ:   int64(math.Round(req.StartLat * 10000)),
		startLonQ:   int64(math.Round(req.StartLon * 10000)),
		maxRadiusQ:  int(math.Ceil(maxRadiusM / 1000.0)),
		snapRadiusQ: int(math.Round(req.SnapRadiusM)),
		minRuns:     req.MinRuns,
		sportFilter: req.SportFilter,
	}
	effectiveRadiusM := float64(cacheKey.maxRadiusQ) * 1000.0

	routeGraphCacheMu.Lock()
	if routeGraphCache != nil && routeGraphCache.key == cacheKey {
		cached := routeGraphCache.graph
		routeGraphCacheMu.Unlock()
		if progressFn != nil {
			progressFn(fmt.Sprintf("Reusing cached trail graph (%d junctions, %d segments)", len(cached.Nodes), len(cached.Edges)))
		}
		return cached, nil
	}
	routeGraphCacheMu.Unlock()

	if progressFn != nil {
		progressFn(fmt.Sprintf("Scanning %d activities within %.1f km radius...", len(files), effectiveRadiusM/1000.0))
	}

	proj := newLocalProj(req.StartLat, req.StartLon)
	// Pass 1 uses a fine 20m grid; Pass 2 merges nearby cells within SnapRadiusM to eliminate grid-boundary braiding!
	fineCellSize := math.Min(20.0, req.SnapRadiusM*0.65)
	if fineCellSize < 12.0 {
		fineCellSize = 12.0
	}
	maxRadiusSq := effectiveRadiusM * effectiveRadiusM

	workers := req.MaxCPUs
	if workers < 1 {
		workers = defaultRouteWorkers()
	}

	jobs := make(chan string, workers*4)
	results := make(chan *activityGridExtract, workers*4)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				if ctx.Err() != nil {
					return
				}
				ext := parseActivityForGrid(path, proj, maxRadiusSq, fineCellSize, req.SportFilter)
				if ext != nil {
					select {
					case results <- ext:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, f := range files {
			select {
			case jobs <- f:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	globalCells := make(map[gridKey]*cellAccum, 4096)
	globalEdges := make(map[gridEdgeKey]*edgeDeltaAccum, 8192)
	var loopTraces []*historicalLoopTrace
	activitiesUsed := 0

	for ext := range results {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		activitiesUsed++
		if ext.loopTrace != nil {
			loopTraces = append(loopTraces, ext.loopTrace)
		}
		for k, acc := range ext.cells {
			gc := globalCells[k]
			if gc == nil {
				gc = &cellAccum{}
				globalCells[k] = gc
			}
			gc.sumX += acc.sumX
			gc.sumY += acc.sumY
			gc.count += acc.count
			gc.sumAlt += acc.sumAlt
			gc.altCnt += acc.altCnt
		}
		// Each activity contributes at most 1 to each undirected edge's run count
		for ek, delta := range ext.edgeDeltas {
			ge := globalEdges[ek]
			if ge == nil {
				ge = &edgeDeltaAccum{}
				globalEdges[ek] = ge
			}
			ge.runCount++
			if ext.edgeHasEle[ek] && len(ge.deltas) < 40 {
				ge.deltas = append(ge.deltas, delta)
			}
		}
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if activitiesUsed == 0 {
		return nil, fmt.Errorf("no matching %s activities with GPS streams found within %.1f km of (%.5f, %.5f)",
			req.SportFilter, effectiveRadiusM/1000.0, req.StartLat, req.StartLon)
	}

	// Keep top 50 highest-vert historical home-start loops as seed candidates
	if len(loopTraces) > 50 {
		sort.Slice(loopTraces, func(i, j int) bool {
			return loopTraces[i].gainM > loopTraces[j].gainM
		})
		loopTraces = loopTraces[:50]
	}

	if progressFn != nil {
		progressFn(fmt.Sprintf("Simplifying trail skeleton from %d nearby activities (min %d runs/segment)...", activitiesUsed, req.MinRuns))
	}

	// Pass 2: Traffic-ordered spatial merging of fine cells within SnapRadiusM to collapse parallel GPS braids
	// into a single clean 1-cell-wide trail spine!
	graph, err := buildCleanTrailGraph(proj, globalCells, globalEdges, loopTraces, req.SnapRadiusM, req.MinRuns)
	if err != nil {
		return nil, err
	}
	graph.ActivitiesLoaded = len(files)
	graph.ActivitiesUsed = activitiesUsed
	graph.BuildDuration = time.Since(startTime)

	routeGraphCacheMu.Lock()
	routeGraphCache = &struct {
		key   graphCacheKey
		graph *TrailGraph
	}{
		key:   cacheKey,
		graph: graph,
	}
	routeGraphCacheMu.Unlock()

	if progressFn != nil {
		progressFn(fmt.Sprintf("Trail graph ready: %d junctions, %d trail segments (snapped %.0fm from start)",
			len(graph.Nodes), len(graph.Edges), graph.SnapDistanceM))
	}

	return graph, nil
}

func parseActivityForGrid(path string, proj localProj, maxRadiusSq, cellSize float64, sportFilter string) *activityGridExtract {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var act routeActivityFile
	if err := json.Unmarshal(data, &act); err != nil {
		return nil
	}
	if !matchesRouteSportFilter(act.Type, act.SportType, act.Name, sportFilter) {
		return nil
	}
	latlng := act.Streams.LatLng.Data
	if len(latlng) < 2 {
		return nil
	}
	rawAlts := act.Streams.Altitude.Data
	hasAlt := len(rawAlts) == len(latlng)

	// Quick coarse check: sample ~40 points to see if activity ever enters bounding box
	near := false
	step := len(latlng) / 40
	if step < 1 {
		step = 1
	}
	coarseRadiusSq := maxRadiusSq * 1.44
	for i := 0; i < len(latlng); i += step {
		x, y := proj.toXY(latlng[i][0], latlng[i][1])
		if x*x+y*y <= coarseRadiusSq {
			near = true
			break
		}
	}
	if !near {
		return nil
	}

	// Smooth this activity's altitude stream first (5-point moving average) to suppress per-second GPS/baro noise
	var smoothAlts []float64
	if hasAlt {
		smoothAlts = smoothActivityAltitude(rawAlts)
	}

	ext := &activityGridExtract{
		actID:      act.ID,
		cells:      make(map[gridKey]cellAccum, 128),
		edgeDeltas: make(map[gridEdgeKey]float64, 128),
		edgeHasEle: make(map[gridEdgeKey]bool, 128),
	}

	// Check if this activity is a home-start loop (starts & ends within 600m of origin)
	x0, y0 := proj.toXY(latlng[0][0], latlng[0][1])
	xEnd, yEnd := proj.toXY(latlng[len(latlng)-1][0], latlng[len(latlng)-1][1])
	isHomeLoop := (x0*x0+y0*y0 <= 600.0*600.0) && (xEnd*xEnd+yEnd*yEnd <= 600.0*600.0)
	var traceCells []gridKey
	if isHomeLoop {
		traceCells = make([]gridKey, 0, 256)
	}

	var prevKey gridKey
	var prevX, prevY, prevAlt float64
	hasPrev := false

	const maxStepJumpM = 150.0
	const maxPlausibleGrade = 0.45 // >45% sustained grade over a step is almost certainly a GPS/baro glitch

	for i, pt := range latlng {
		x, y := proj.toXY(pt[0], pt[1])
		if x*x+y*y > maxRadiusSq {
			hasPrev = false
			isHomeLoop = false
			continue
		}

		gk := gridKey{
			gx: int32(math.Floor(x / cellSize)),
			gy: int32(math.Floor(y / cellSize)),
		}
		if isHomeLoop && (len(traceCells) == 0 || traceCells[len(traceCells)-1] != gk) {
			traceCells = append(traceCells, gk)
		}

		acc := ext.cells[gk]
		acc.sumX += x
		acc.sumY += y
		acc.count++
		curAlt := 0.0
		if hasAlt && !math.IsNaN(smoothAlts[i]) {
			curAlt = smoothAlts[i]
			acc.sumAlt += curAlt
			acc.altCnt++
		}
		ext.cells[gk] = acc

		if hasPrev {
			dist := math.Hypot(x-prevX, y-prevY)
			if dist <= maxStepJumpM && gk != prevKey {
				ek, forward := makeGridEdgeKey(prevKey, gk)
				if _, exists := ext.edgeDeltas[ek]; !exists {
					ext.edgeDeltas[ek] = 0
					if hasAlt && !math.IsNaN(curAlt) && !math.IsNaN(prevAlt) && dist >= 3.0 {
						grade := (curAlt - prevAlt) / dist
						// Reject implausible elevation spikes (>45% sustained grade)
						if math.Abs(grade) <= maxPlausibleGrade {
							if !forward {
								grade = -grade
							}
							ext.edgeDeltas[ek] = grade
							ext.edgeHasEle[ek] = true
						}
					}
				}
			}
		}

		prevKey = gk
		prevX = x
		prevY = y
		prevAlt = curAlt
		hasPrev = true
	}

	if len(ext.edgeDeltas) == 0 {
		return nil
	}
	if isHomeLoop && len(traceCells) >= 10 {
		gain := act.TotalElevationGain
		if gain <= 0 && hasAlt {
			gain, _ = computeDirectionalGain(smoothAlts, 1.5)
		}
		ext.loopTrace = &historicalLoopTrace{
			gainM: gain,
			cells: traceCells,
		}
	}
	return ext
}

// smoothActivityAltitude applies a 5-point moving average to suppress per-second altitude chatter.
func smoothActivityAltitude(alts []float64) []float64 {
	n := len(alts)
	out := make([]float64, n)
	if n < 5 {
		copy(out, alts)
		return out
	}
	for i := 0; i < n; i++ {
		lo := i - 2
		if lo < 0 {
			lo = 0
		}
		hi := i + 2
		if hi >= n {
			hi = n - 1
		}
		sum := 0.0
		cnt := 0
		for j := lo; j <= hi; j++ {
			if !math.IsNaN(alts[j]) {
				sum += alts[j]
				cnt++
			}
		}
		if cnt > 0 {
			out[i] = sum / float64(cnt)
		} else {
			out[i] = alts[i]
		}
	}
	return out
}

func medianFloat(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := make([]float64, len(vals))
	copy(cp, vals)
	sort.Float64s(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[mid]
	}
	return 0.5 * (cp[mid-1] + cp[mid])
}

type superEdgeKey struct {
	u int
	v int
}

func makeSuperEdgeKey(a, b int) (superEdgeKey, bool) {
	if a < b {
		return superEdgeKey{u: a, v: b}, true
	}
	return superEdgeKey{u: b, v: a}, false
}

type superNode struct {
	id     int
	sumX   float64
	sumY   float64
	sumW   float64
	x, y   float64
	sumAlt float64
	altW   float64
	alt    float64
}

// buildCleanTrailGraph merges braided GPS cells into a 1-node-wide skeleton, prunes redundant
// micro-triangles and short dead-end spurs, and contracts degree-2 chains into TrailGraph edges.
func buildCleanTrailGraph(
	proj localProj,
	globalCells map[gridKey]*cellAccum,
	globalEdges map[gridEdgeKey]*edgeDeltaAccum,
	loopTraces []*historicalLoopTrace,
	snapRadiusM float64,
	minRuns int,
) (*TrailGraph, error) {
	// 1. Collect fine cells that participate in at least one edge (even before minRuns,
	// so parallel braided traces of 1+1 runs on the same trail can merge and reach minRuns=2!).
	type fineCellInfo struct {
		key    gridKey
		x, y   float64
		alt    float64
		hasAlt bool
		weight int
	}
	cellTraffic := make(map[gridKey]int, len(globalCells))
	for ek, ed := range globalEdges {
		cellTraffic[ek.a] += ed.runCount
		cellTraffic[ek.b] += ed.runCount
	}

	var fineList []fineCellInfo
	for k, acc := range globalCells {
		if cellTraffic[k] == 0 {
			continue
		}
		alt := 0.0
		if acc.altCnt > 0 {
			alt = acc.sumAlt / float64(acc.altCnt)
		}
		fineList = append(fineList, fineCellInfo{
			key:    k,
			x:      acc.sumX / float64(acc.count),
			y:      acc.sumY / float64(acc.count),
			alt:    alt,
			hasAlt: acc.altCnt > 0,
			weight: cellTraffic[k],
		})
	}
	if len(fineList) == 0 {
		return nil, fmt.Errorf("no connected trail points found")
	}

	// Sort highest-traffic cells first so popular trail centers become the canonical spine nodes
	sort.Slice(fineList, func(i, j int) bool {
		return fineList[i].weight > fineList[j].weight
	})

	// 2. Greedy spatial clustering at radius = snapRadiusM using a coarse hash bucket for O(1) lookup
	var sNodes []*superNode
	bucketSize := snapRadiusM
	buckets := make(map[gridKey][]int, len(fineList)/2)
	cellToSuper := make(map[gridKey]int, len(fineList))
	mergeRadSq := snapRadiusM * snapRadiusM

	for _, fc := range fineList {
		bx := int32(math.Floor(fc.x / bucketSize))
		by := int32(math.Floor(fc.y / bucketSize))
		bestID := -1
		bestD2 := mergeRadSq

		for dx := int32(-1); dx <= 1; dx++ {
			for dy := int32(-1); dy <= 1; dy++ {
				for _, sid := range buckets[gridKey{bx + dx, by + dy}] {
					sn := sNodes[sid]
					d2 := (fc.x-sn.x)*(fc.x-sn.x) + (fc.y-sn.y)*(fc.y-sn.y)
					if d2 <= bestD2 {
						bestD2 = d2
						bestID = sid
					}
				}
			}
		}

		w := float64(fc.weight)
		if bestID >= 0 {
			sn := sNodes[bestID]
			sn.sumX += fc.x * w
			sn.sumY += fc.y * w
			sn.sumW += w
			sn.x = sn.sumX / sn.sumW
			sn.y = sn.sumY / sn.sumW
			if fc.hasAlt {
				sn.sumAlt += fc.alt * w
				sn.altW += w
				sn.alt = sn.sumAlt / sn.altW
			}
			cellToSuper[fc.key] = bestID
		} else {
			sid := len(sNodes)
			sn := &superNode{
				id:   sid,
				sumX: fc.x * w,
				sumY: fc.y * w,
				sumW: w,
				x:    fc.x,
				y:    fc.y,
			}
			if fc.hasAlt {
				sn.sumAlt = fc.alt * w
				sn.altW = w
				sn.alt = fc.alt
			}
			sNodes = append(sNodes, sn)
			buckets[gridKey{bx, by}] = append(buckets[gridKey{bx, by}], sid)
			cellToSuper[fc.key] = sid
		}
	}

	// 3. Remap fine edges onto super-nodes, summing run counts and aggregating relative elevation deltas
	type superEdgeData struct {
		runCount int
		deltas   []float64 // canonical delta (v - u) where u < v
	}
	sEdges := make(map[superEdgeKey]*superEdgeData, len(globalEdges)/2)

	for ek, ed := range globalEdges {
		su, ok1 := cellToSuper[ek.a]
		sv, ok2 := cellToSuper[ek.b]
		if !ok1 || !ok2 || su == sv {
			continue
		}
		sek, sameDir := makeSuperEdgeKey(su, sv)
		sed := sEdges[sek]
		if sed == nil {
			sed = &superEdgeData{}
			sEdges[sek] = sed
		}
		sed.runCount += ed.runCount
		for _, d := range ed.deltas {
			if len(sed.deltas) >= 60 {
				break
			}
			if sameDir {
				sed.deltas = append(sed.deltas, d)
			} else {
				sed.deltas = append(sed.deltas, -d)
			}
		}
	}

	// Filter super-edges by minRuns
	adj := make(map[int]map[int]bool)
	keptSuperEdges := make(map[superEdgeKey]*superEdgeData)
	for sek, sed := range sEdges {
		if sed.runCount < minRuns {
			continue
		}
		keptSuperEdges[sek] = sed
		if adj[sek.u] == nil {
			adj[sek.u] = make(map[int]bool)
		}
		if adj[sek.v] == nil {
			adj[sek.v] = make(map[int]bool)
		}
		adj[sek.u][sek.v] = true
		adj[sek.v][sek.u] = true
	}

	if len(keptSuperEdges) == 0 {
		return nil, fmt.Errorf("no trail segments met the minimum %d runs requirement (try lowering min_runs)", minRuns)
	}

	// 4. Prune redundant micro-triangles (3 mutually adjacent super-nodes within ~2.3*snapRadiusM):
	// remove the longest/least-trafficked chord edge so trails don't retain small zigzag chords.
	maxTriLen := snapRadiusM * 2.3
	type triCandidate struct {
		sek superEdgeKey
		rc  int
		len float64
	}
	var triEdges []triCandidate
	for sek, sed := range keptSuperEdges {
		d := math.Hypot(sNodes[sek.u].x-sNodes[sek.v].x, sNodes[sek.u].y-sNodes[sek.v].y)
		if d <= maxTriLen {
			triEdges = append(triEdges, triCandidate{sek: sek, rc: sed.runCount, len: d})
		}
	}
	// Consider lowest-traffic, longest chords first for removal
	sort.Slice(triEdges, func(i, j int) bool {
		if triEdges[i].rc != triEdges[j].rc {
			return triEdges[i].rc < triEdges[j].rc
		}
		return triEdges[i].len > triEdges[j].len
	})
	for _, tc := range triEdges {
		u, v := tc.sek.u, tc.sek.v
		if !adj[u][v] {
			continue
		}
		// Check if u and v share a common neighbor w such that u-w and w-v are both shorter than or comparable to u-v
		hasTriangle := false
		for w := range adj[u] {
			if w != v && adj[v][w] {
				duw := math.Hypot(sNodes[u].x-sNodes[w].x, sNodes[u].y-sNodes[w].y)
				dvw := math.Hypot(sNodes[v].x-sNodes[w].x, sNodes[v].y-sNodes[w].y)
				if duw <= maxTriLen && dvw <= maxTriLen {
					hasTriangle = true
					break
				}
			}
		}
		if hasTriangle && len(adj[u]) > 2 && len(adj[v]) > 2 {
			delete(adj[u], v)
			delete(adj[v], u)
			delete(keptSuperEdges, tc.sek)
		}
	}

	// 5. Find nearest surviving super-node to start (0, 0)
	startSuper := -1
	bestD2 := math.Inf(1)
	for sid := range adj {
		if len(adj[sid]) == 0 {
			continue
		}
		sn := sNodes[sid]
		d2 := sn.x*sn.x + sn.y*sn.y
		if d2 < bestD2 {
			bestD2 = d2
			startSuper = sid
		}
	}
	if startSuper < 0 {
		return nil, fmt.Errorf("no reachable trail nodes found near start coordinates")
	}
	snapDistM := math.Sqrt(bestD2)
	if snapDistM > 2000.0 {
		return nil, fmt.Errorf("nearest trail segment with >=%d runs is %.0fm away from start point (max snap 2000m)", minRuns, snapDistM)
	}

	// 6. Keep only connected component containing startSuper
	reachable := make(map[int]bool)
	queue := []int{startSuper}
	reachable[startSuper] = true
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for nb := range adj[cur] {
			if !reachable[nb] {
				reachable[nb] = true
				queue = append(queue, nb)
			}
		}
	}

	// 7. Iteratively prune short dead-end spurs (< 200m) that don't contain startSuper
	const maxPruneSpurM = 200.0
	pruned := true
	for pruned {
		pruned = false
		for sid := range reachable {
			if sid == startSuper {
				continue
			}
			// Count reachable neighbors
			deg := 0
			onlyNb := -1
			for nb := range adj[sid] {
				if reachable[nb] {
					deg++
					onlyNb = nb
				}
			}
			if deg != 1 {
				continue
			}
			// Walk back along this degree-1/2 dead-end spur to measure its total length
			spurNodes := []int{sid}
			spurLen := 0.0
			prev := sid
			curr := onlyNb
			for curr != startSuper {
				spurLen += math.Hypot(sNodes[prev].x-sNodes[curr].x, sNodes[prev].y-sNodes[curr].y)
				if spurLen > maxPruneSpurM {
					break
				}
				cDeg := 0
				next := -1
				for nb := range adj[curr] {
					if reachable[nb] {
						cDeg++
						if nb != prev {
							next = nb
						}
					}
				}
				if cDeg != 2 || next == -1 {
					break
				}
				spurNodes = append(spurNodes, curr)
				prev = curr
				curr = next
			}
			if spurLen <= maxPruneSpurM {
				for _, rm := range spurNodes {
					delete(reachable, rm)
					for nb := range adj[rm] {
						delete(adj[nb], rm)
					}
					delete(adj, rm)
				}
				pruned = true
			}
		}
	}

	// 8. Contract degree-2 chains into clean GraphEdges with robust median relative elevation deltas
	isJunction := make(map[int]bool)
	isJunction[startSuper] = true
	for sid := range reachable {
		deg := 0
		for nb := range adj[sid] {
			if reachable[nb] {
				deg++
			}
		}
		if deg != 2 {
			isJunction[sid] = true
		}
	}

	superToNode := make(map[int]int, len(isJunction))
	var nodes []GraphNode
	addNode := func(sid int) int {
		if id, ok := superToNode[sid]; ok {
			return id
		}
		sn := sNodes[sid]
		lat, lon := proj.toLatLon(sn.x, sn.y)
		id := len(nodes)
		superToNode[sid] = id
		nodes = append(nodes, GraphNode{
			ID:  id,
			Lat: lat,
			Lon: lon,
			Ele: sn.alt,
			X:   sn.x,
			Y:   sn.y,
		})
		return id
	}

	startNodeID := addNode(startSuper)
	for sid := range isJunction {
		addNode(sid)
	}

	visitedEdge := make(map[superEdgeKey]bool)
	var edges []GraphEdge

	getEdgeDelta := func(a, b int) (float64, int) {
		sek, sameDir := makeSuperEdgeKey(a, b)
		sed := keptSuperEdges[sek]
		if sed == nil {
			return 0, 0
		}
		med := medianFloat(sed.deltas)
		if !sameDir {
			med = -med
		}
		return med, sed.runCount
	}

	buildChain := func(uSuper, firstNext int) {
		sek0, _ := makeSuperEdgeKey(uSuper, firstNext)
		if visitedEdge[sek0] {
			return
		}
		chain := []int{uSuper, firstNext}
		visitedEdge[sek0] = true

		prev := uSuper
		curr := firstNext
		for !isJunction[curr] {
			next := -1
			for nb := range adj[curr] {
				if reachable[nb] && nb != prev {
					next = nb
					break
				}
			}
			if next == -1 {
				break
			}
			sek, _ := makeSuperEdgeKey(curr, next)
			if visitedEdge[sek] {
				break
			}
			visitedEdge[sek] = true
			chain = append(chain, next)
			prev = curr
			curr = next
		}

		uID := addNode(uSuper)
		vID := addNode(curr)

		// Build polyline using robust median relative elevation deltas along the chain
		pts := make([][3]float64, len(chain))
		relAlts := make([]float64, len(chain))
		length := 0.0
		minRC := math.MaxInt32
		baseEle := sNodes[chain[0]].alt
		relAlts[0] = 0

		for i, sid := range chain {
			sn := sNodes[sid]
			lat, lon := proj.toLatLon(sn.x, sn.y)
			if i > 0 {
				prevSN := sNodes[chain[i-1]]
				segLen := math.Hypot(sn.x-prevSN.x, sn.y-prevSN.y)
				length += segLen
				medGrade, rc := getEdgeDelta(chain[i-1], sid)
				if rc > 0 && rc < minRC {
					minRC = rc
				}
				// Clamp median grade to plausible range (<= 40%) and scale by super-edge length
				if medGrade > 0.40 {
					medGrade = 0.40
				} else if medGrade < -0.40 {
					medGrade = -0.40
				}
				dEle := medGrade * segLen
				relAlts[i] = relAlts[i-1] + dEle
			}
			pts[i] = [3]float64{lat, lon, baseEle + relAlts[i]}
		}
		if minRC == math.MaxInt32 {
			minRC = minRuns
		}

		// Compute directional elevation gain with a 1.0m hysteresis threshold (data is already 5-pt smoothed + median filtered)
		gainUV, gainVU := computeDirectionalGain(relAlts, 1.0)

		// Compute unit leaving/arriving direction vectors for anti-U-turn filtering
		startDirX, startDirY := computeChainDir(sNodes, chain, true)
		endDirX, endDirY := computeChainDir(sNodes, chain, false)

		// Compute interior 25m spatial corridor cells (excluding 50m near endpoints U and V)
		corridor := computeCorridorCells(sNodes, chain, 25.0)

		edgeID := len(edges)
		edges = append(edges, GraphEdge{
			ID:            edgeID,
			U:             uID,
			V:             vID,
			Length:        length,
			GainUV:        gainUV,
			GainVU:        gainVU,
			NetDeltaUV:    relAlts[len(relAlts)-1],
			RunCount:      minRC,
			Points:        pts,
			StartDirX:     startDirX,
			StartDirY:     startDirY,
			EndDirX:       endDirX,
			EndDirY:       endDirY,
			CorridorCells: corridor,
		})
		nodes[uID].Edges = append(nodes[uID].Edges, edgeID)
		if vID != uID {
			nodes[vID].Edges = append(nodes[vID].Edges, edgeID)
		}
	}

	// Trace chains from every junction
	for sid := range isJunction {
		if !reachable[sid] {
			continue
		}
		for nb := range adj[sid] {
			if reachable[nb] {
				buildChain(sid, nb)
			}
		}
	}

	g := &TrailGraph{
		OriginLat:     proj.lat0,
		OriginLon:     proj.lon0,
		StartNode:     startNodeID,
		SnapDistanceM: snapDistM,
		Nodes:         nodes,
		Edges:         edges,
	}

	// Project historical home-start loop traces onto the simplified TrailGraph so proven high-vert
	// loops are automatically included as seed candidates for the solver and local search.
	if len(loopTraces) > 0 {
		g.HistoricalLoops = projectHistoricalLoops(g, loopTraces, cellToSuper, superToNode)
	}

	return g, nil
}

// projectHistoricalLoops maps historical fine-cell loop traces onto valid sequences of GraphEdges
// starting and ending at g.StartNode.
func projectHistoricalLoops(
	g *TrailGraph,
	traces []*historicalLoopTrace,
	cellToSuper map[gridKey]int,
	superToNode map[int]int,
) [][]RouteStep {
	// Quick lookup for direct edge between two junction nodes (picking shortest if multigraph)
	directEdge := func(u, v int) (RouteStep, bool) {
		bestID := -1
		bestLen := math.Inf(1)
		for _, eid := range g.Nodes[u].Edges {
			e := &g.Edges[eid]
			if e.Other(u) == v && e.Length < bestLen {
				bestLen = e.Length
				bestID = eid
			}
		}
		if bestID < 0 {
			return RouteStep{}, false
		}
		e := &g.Edges[bestID]
		return RouteStep{EdgeID: bestID, From: u, To: v, Forward: e.U == u}, true
	}

	var projected [][]RouteStep
	for _, tr := range traces {
		// Extract ordered sequence of visited junction node IDs
		jSeq := []int{g.StartNode}
		for _, ck := range tr.cells {
			sid, ok := cellToSuper[ck]
			if !ok {
				continue
			}
			nid, ok := superToNode[sid]
			if !ok {
				continue
			}
			if jSeq[len(jSeq)-1] != nid {
				jSeq = append(jSeq, nid)
			}
		}
		if jSeq[len(jSeq)-1] != g.StartNode {
			jSeq = append(jSeq, g.StartNode)
		}
		// Special case: a pure loop with no intermediate junctions contracts to a single self-loop edge on StartNode
		if len(jSeq) <= 2 {
			for _, eid := range g.Nodes[g.StartNode].Edges {
				e := &g.Edges[eid]
				if e.U == g.StartNode && e.V == g.StartNode {
					projected = append(projected, []RouteStep{{
						EdgeID:  eid,
						From:    g.StartNode,
						To:      g.StartNode,
						Forward: true,
					}})
				}
			}
			continue
		}

		// Connect consecutive junctions in jSeq via direct edges (or short <= 4-hop BFS bridge)
		var steps []RouteStep
		valid := true
		for i := 0; i < len(jSeq)-1; i++ {
			u, v := jSeq[i], jSeq[i+1]
			if u == v {
				continue
			}
			if st, ok := directEdge(u, v); ok {
				steps = append(steps, st)
				continue
			}
			// Short BFS bridge (up to 4 hops) in case a junction was slightly missed by GPS jitter
			bridge, ok := shortNodeBridge(g, u, v, 4)
			if !ok {
				valid = false
				break
			}
			steps = append(steps, bridge...)
		}
		if valid && len(steps) >= 2 && steps[0].From == g.StartNode && steps[len(steps)-1].To == g.StartNode {
			projected = append(projected, steps)
		}
	}
	return projected
}

func shortNodeBridge(g *TrailGraph, src, dst, maxHops int) ([]RouteStep, bool) {
	type qItem struct {
		node int
		hops int
	}
	parentNode := map[int]int{src: -1}
	parentEdge := map[int]int{src: -1}
	q := []qItem{{node: src, hops: 0}}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		if cur.node == dst {
			var rev []RouteStep
			c := dst
			for c != src {
				p := parentNode[c]
				eid := parentEdge[c]
				e := &g.Edges[eid]
				rev = append(rev, RouteStep{EdgeID: eid, From: p, To: c, Forward: e.U == p})
				c = p
			}
			for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
				rev[i], rev[j] = rev[j], rev[i]
			}
			return rev, true
		}
		if cur.hops >= maxHops {
			continue
		}
		for _, eid := range g.Nodes[cur.node].Edges {
			nb := g.Edges[eid].Other(cur.node)
			if _, seen := parentNode[nb]; !seen {
				parentNode[nb] = cur.node
				parentEdge[nb] = eid
				q = append(q, qItem{node: nb, hops: cur.hops + 1})
			}
		}
	}
	return nil, false
}

// computeChainDir computes a smoothed unit direction vector near the start (fromStart=true)
// or end (fromStart=false) of a super-node chain.
func computeChainDir(sNodes []*superNode, chain []int, fromStart bool) (float64, float64) {
	n := len(chain)
	if n < 2 {
		return 0, 0
	}
	var dx, dy float64
	if fromStart {
		// Look up to 3 hops ahead (or ~80m) from chain[0] for a stable leaving bearing
		u := sNodes[chain[0]]
		targetIdx := 1
		for i := 1; i < n && i <= 3; i++ {
			targetIdx = i
			if math.Hypot(sNodes[chain[i]].x-u.x, sNodes[chain[i]].y-u.y) >= 70.0 {
				break
			}
		}
		dx = sNodes[chain[targetIdx]].x - u.x
		dy = sNodes[chain[targetIdx]].y - u.y
	} else {
		// Look up to 3 hops back from chain[n-1] for a stable arriving bearing
		v := sNodes[chain[n-1]]
		srcIdx := n - 2
		for i := n - 2; i >= 0 && i >= n-4; i-- {
			srcIdx = i
			if math.Hypot(v.x-sNodes[chain[i]].x, v.y-sNodes[chain[i]].y) >= 70.0 {
				break
			}
		}
		dx = v.x - sNodes[chain[srcIdx]].x
		dy = v.y - sNodes[chain[srcIdx]].y
	}
	mag := math.Hypot(dx, dy)
	if mag < 1e-6 {
		return 0, 0
	}
	return dx / mag, dy / mag
}

// computeCorridorCells returns deduplicated interior spatial cells (at cellSize meters)
// along a chain, excluding endpoints within cellSize of U and V so trails meeting at a
// junction don't falsely register as overlapping.
func computeCorridorCells(sNodes []*superNode, chain []int, cellSize float64) []gridKey {
	if len(chain) < 3 {
		return nil
	}
	u := sNodes[chain[0]]
	v := sNodes[chain[len(chain)-1]]
	endMargin := math.Max(50.0, cellSize*2.0)
	seen := make(map[gridKey]struct{}, len(chain))
	var out []gridKey
	for i := 1; i < len(chain)-1; i++ {
		sn := sNodes[chain[i]]
		if math.Hypot(sn.x-u.x, sn.y-u.y) < endMargin || math.Hypot(sn.x-v.x, sn.y-v.y) < endMargin {
			continue
		}
		gk := gridKey{
			gx: int32(math.Floor(sn.x / cellSize)),
			gy: int32(math.Floor(sn.y / cellSize)),
		}
		if _, ok := seen[gk]; !ok {
			seen[gk] = struct{}{}
			out = append(out, gk)
		}
	}
	return out
}

// computeDirectionalGain calculates elevation gain in both forward (U->V) and reverse (V->U)
// directions using a hysteresis threshold to ignore minor GPS elevation jitter.
func computeDirectionalGain(alts []float64, threshold float64) (float64, float64) {
	if len(alts) < 2 {
		return 0, 0
	}
	calc := func(seq []float64) float64 {
		gain := 0.0
		ref := seq[0]
		for i := 1; i < len(seq); i++ {
			diff := seq[i] - ref
			if diff >= threshold {
				gain += diff
				ref = seq[i]
			} else if diff <= -threshold {
				ref = seq[i]
			}
		}
		if seq[len(seq)-1] > ref {
			gain += seq[len(seq)-1] - ref
		}
		return gain
	}

	gainUV := calc(alts)

	rev := make([]float64, len(alts))
	for i := range alts {
		rev[i] = alts[len(alts)-1-i]
	}
	gainVU := calc(rev)
	return gainUV, gainVU
}

// DetectFrequentStarts scans activities and returns the top starting locations clustered
// within ~250m so the UI can offer quick-pick start points.
func DetectFrequentStarts(dataDir string, limit int) []StartCluster {
	if limit <= 0 {
		limit = 5
	}
	activitiesDir := filepath.Join(dataDir, "activities")
	entries, err := os.ReadDir(activitiesDir)
	if err != nil {
		return nil
	}

	type cluster struct {
		sumLat, sumLon float64
		count          int
		sampleName     string
	}

	type startOnlyFile struct {
		Name        string    `json:"name"`
		Type        string    `json:"type"`
		SportType   string    `json:"sport_type"`
		StartLatLng []float64 `json:"start_latlng"`
	}

	var clusters []*cluster
	const clusterRadiusM = 250.0

	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(activitiesDir, e.Name()))
		if err != nil {
			continue
		}
		var sf startOnlyFile
		if err := json.Unmarshal(data, &sf); err != nil {
			continue
		}
		if len(sf.StartLatLng) < 2 || (sf.StartLatLng[0] == 0 && sf.StartLatLng[1] == 0) {
			continue
		}
		if !matchesRouteSportFilter(sf.Type, sf.SportType, sf.Name, "foot") {
			continue
		}
		lat, lon := sf.StartLatLng[0], sf.StartLatLng[1]
		matched := false
		for _, c := range clusters {
			cLat := c.sumLat / float64(c.count)
			cLon := c.sumLon / float64(c.count)
			if haversineM(lat, lon, cLat, cLon) <= clusterRadiusM {
				c.sumLat += lat
				c.sumLon += lon
				c.count++
				matched = true
				break
			}
		}
		if !matched {
			clusters = append(clusters, &cluster{
				sumLat:     lat,
				sumLon:     lon,
				count:      1,
				sampleName: sf.Name,
			})
		}
	}

	sort.Slice(clusters, func(i, j int) bool {
		return clusters[i].count > clusters[j].count
	})

	if len(clusters) > limit {
		clusters = clusters[:limit]
	}

	out := make([]StartCluster, len(clusters))
	for i, c := range clusters {
		out[i] = StartCluster{
			Lat:        math.Round((c.sumLat/float64(c.count))*1e5) / 1e5,
			Lon:        math.Round((c.sumLon/float64(c.count))*1e5) / 1e5,
			Count:      c.count,
			SampleName: c.sampleName,
		}
	}
	return out
}
