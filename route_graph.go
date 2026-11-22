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
	SnapRadiusM      float64 `json:"snap_radius_m"`      // default 25.0
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
		r.SnapRadiusM = 25.0
	}
	if r.SnapRadiusM < 10.0 {
		r.SnapRadiusM = 10.0
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
	ID       int
	U        int
	V        int
	Length   float64      // meters
	GainUV   float64      // elevation gain going U -> V (meters)
	GainVU   float64      // elevation gain going V -> U (meters)
	RunCount int          // unique activity count supporting this segment
	Points   [][3]float64 // ordered [lat, lon, ele] from U to V
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

// TrailGraph is the personal trail network built from historical GPS traces.
type TrailGraph struct {
	OriginLat        float64
	OriginLon        float64
	StartNode        int
	SnapDistanceM    float64
	Nodes            []GraphNode
	Edges            []GraphEdge
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
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	SportType   string    `json:"sport_type"`
	StartLatLng []float64 `json:"start_latlng"`
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

func makeGridEdgeKey(u, v gridKey) gridEdgeKey {
	if u.gx < v.gx || (u.gx == v.gx && u.gy < v.gy) {
		return gridEdgeKey{a: u, b: v}
	}
	return gridEdgeKey{a: v, b: u}
}

type cellAccum struct {
	sumX   float64
	sumY   float64
	count  int
	sumAlt float64
	altCnt int
}

type activityGridExtract struct {
	actID int64
	cells map[gridKey]cellAccum
	edges map[gridEdgeKey]struct{}
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
// a simplified undirected trail graph filtered by min_runs.
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
		maxRadiusQ:  int(math.Ceil(maxRadiusM / 1000.0)), // bucket to 1km so slight dist changes reuse cache
		snapRadiusQ: int(math.Round(req.SnapRadiusM)),
		minRuns:     req.MinRuns,
		sportFilter: req.SportFilter,
	}
	// Use the rounded-up radius for actual filtering so cached graph always covers the request
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
	cellSize := req.SnapRadiusM
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
				ext := parseActivityForGrid(path, proj, maxRadiusSq, cellSize, req.SportFilter)
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
	globalEdgeRuns := make(map[gridEdgeKey]int, 8192)
	activitiesUsed := 0

	for ext := range results {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		activitiesUsed++
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
		for ek := range ext.edges {
			globalEdgeRuns[ek]++
		}
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if activitiesUsed == 0 {
		return nil, fmt.Errorf("no matching %s activities with GPS streams found within %.1f km of (%.5f, %.5f)",
			req.SportFilter, effectiveRadiusM/1000.0, req.StartLat, req.StartLon)
	}

	if progressFn != nil {
		progressFn(fmt.Sprintf("Building trail graph from %d nearby activities (min %d runs/segment)...", activitiesUsed, req.MinRuns))
	}

	// Filter edges by min_runs and build raw adjacency
	rawAdj := make(map[gridKey][]gridKey)
	keptEdges := make(map[gridEdgeKey]int)
	for ek, cnt := range globalEdgeRuns {
		if cnt < req.MinRuns {
			continue
		}
		if _, ok := globalCells[ek.a]; !ok {
			continue
		}
		if _, ok := globalCells[ek.b]; !ok {
			continue
		}
		keptEdges[ek] = cnt
		rawAdj[ek.a] = append(rawAdj[ek.a], ek.b)
		rawAdj[ek.b] = append(rawAdj[ek.b], ek.a)
	}

	if len(keptEdges) == 0 {
		return nil, fmt.Errorf("no trail segments met the minimum %d runs requirement within %.1f km (try lowering min_runs)",
			req.MinRuns, effectiveRadiusM/1000.0)
	}

	// Find nearest surviving cell to start coordinates (0, 0)
	var startCell gridKey
	bestStartDistSq := math.Inf(1)
	for k := range rawAdj {
		acc := globalCells[k]
		cx := acc.sumX / float64(acc.count)
		cy := acc.sumY / float64(acc.count)
		d2 := cx*cx + cy*cy
		if d2 < bestStartDistSq {
			bestStartDistSq = d2
			startCell = k
		}
	}
	snapDistM := math.Sqrt(bestStartDistSq)
	if snapDistM > 2000.0 {
		return nil, fmt.Errorf("nearest trail segment with >=%d runs is %.0fm away from start point (max snap 2000m)",
			req.MinRuns, snapDistM)
	}

	// BFS from startCell to keep only the connected component reachable from start
	reachable := make(map[gridKey]bool)
	queue := []gridKey{startCell}
	reachable[startCell] = true
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		for _, nb := range rawAdj[curr] {
			if !reachable[nb] {
				reachable[nb] = true
				queue = append(queue, nb)
			}
		}
	}

	// Contract degree-2 chains into clean GraphEdges between junctions
	graph := contractGridToGraph(proj, startCell, snapDistM, reachable, rawAdj, globalCells, keptEdges)
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
	alts := act.Streams.Altitude.Data
	hasAlt := len(alts) == len(latlng)

	// Quick coarse check: sample every ~20th point to see if activity ever enters bounding box
	near := false
	step := len(latlng) / 40
	if step < 1 {
		step = 1
	}
	// Expand coarse check radius slightly so we don't miss an activity clipping the edge
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

	ext := &activityGridExtract{
		actID: act.ID,
		cells: make(map[gridKey]cellAccum, 128),
		edges: make(map[gridEdgeKey]struct{}, 128),
	}

	var prevKey gridKey
	var prevX, prevY float64
	hasPrev := false

	const maxStepJumpM = 150.0 // ignore GPS pause/resume teleports

	for i, pt := range latlng {
		x, y := proj.toXY(pt[0], pt[1])
		if x*x+y*y > maxRadiusSq {
			hasPrev = false
			continue
		}

		gk := gridKey{
			gx: int32(math.Floor(x / cellSize)),
			gy: int32(math.Floor(y / cellSize)),
		}

		acc := ext.cells[gk]
		acc.sumX += x
		acc.sumY += y
		acc.count++
		if hasAlt && !math.IsNaN(alts[i]) {
			acc.sumAlt += alts[i]
			acc.altCnt++
		}
		ext.cells[gk] = acc

		if hasPrev {
			dx := x - prevX
			dy := y - prevY
			dist := math.Hypot(dx, dy)
			if dist <= maxStepJumpM && gk != prevKey {
				ek := makeGridEdgeKey(prevKey, gk)
				ext.edges[ek] = struct{}{}
			}
		}

		prevKey = gk
		prevX = x
		prevY = y
		hasPrev = true
	}

	if len(ext.edges) == 0 {
		return nil
	}
	return ext
}

// contractGridToGraph simplifies chains of degree-2 grid cells into trail segments between junctions.
func contractGridToGraph(
	proj localProj,
	startCell gridKey,
	snapDistM float64,
	reachable map[gridKey]bool,
	rawAdj map[gridKey][]gridKey,
	cells map[gridKey]*cellAccum,
	edgeRuns map[gridEdgeKey]int,
) *TrailGraph {
	// Identify junction cells: startCell or any cell with degree != 2
	isJunction := make(map[gridKey]bool)
	isJunction[startCell] = true
	for k := range reachable {
		deg := 0
		for _, nb := range rawAdj[k] {
			if reachable[nb] {
				deg++
			}
		}
		if deg != 2 {
			isJunction[k] = true
		}
	}

	// Assign node IDs to junctions
	cellToNode := make(map[gridKey]int, len(isJunction))
	var nodes []GraphNode

	addNode := func(k gridKey) int {
		if id, ok := cellToNode[k]; ok {
			return id
		}
		acc := cells[k]
		cx := acc.sumX / float64(acc.count)
		cy := acc.sumY / float64(acc.count)
		ele := 0.0
		if acc.altCnt > 0 {
			ele = acc.sumAlt / float64(acc.altCnt)
		}
		lat, lon := proj.toLatLon(cx, cy)
		id := len(nodes)
		cellToNode[k] = id
		nodes = append(nodes, GraphNode{
			ID:  id,
			Lat: lat,
			Lon: lon,
			Ele: ele,
			X:   cx,
			Y:   cy,
		})
		return id
	}

	startNodeID := addNode(startCell)
	for k := range isJunction {
		addNode(k)
	}

	visitedEdge := make(map[gridEdgeKey]bool)
	var edges []GraphEdge

	buildChainEdge := func(uCell, firstNext gridKey) {
		ek0 := makeGridEdgeKey(uCell, firstNext)
		if visitedEdge[ek0] {
			return
		}
		chain := []gridKey{uCell, firstNext}
		visitedEdge[ek0] = true
		minRuns := edgeRuns[ek0]

		prev := uCell
		curr := firstNext
		for !isJunction[curr] {
			var next gridKey
			found := false
			for _, nb := range rawAdj[curr] {
				if !reachable[nb] || nb == prev {
					continue
				}
				next = nb
				found = true
				break
			}
			if !found {
				break
			}
			ek := makeGridEdgeKey(curr, next)
			if visitedEdge[ek] {
				break
			}
			visitedEdge[ek] = true
			if rc := edgeRuns[ek]; rc < minRuns {
				minRuns = rc
			}
			chain = append(chain, next)
			prev = curr
			curr = next
		}

		// Ensure endpoint is a node
		uID := addNode(uCell)
		vID := addNode(curr)

		// Compute polyline, length, and smoothed elevation gain in both directions
		pts := make([][3]float64, len(chain))
		rawAlts := make([]float64, len(chain))
		length := 0.0
		var prevX, prevY float64
		for i, ck := range chain {
			acc := cells[ck]
			cx := acc.sumX / float64(acc.count)
			cy := acc.sumY / float64(acc.count)
			ele := 0.0
			if acc.altCnt > 0 {
				ele = acc.sumAlt / float64(acc.altCnt)
			}
			lat, lon := proj.toLatLon(cx, cy)
			pts[i] = [3]float64{lat, lon, ele}
			rawAlts[i] = ele
			if i > 0 {
				length += math.Hypot(cx-prevX, cy-prevY)
			}
			prevX, prevY = cx, cy
		}

		// Smooth altitudes slightly to suppress GPS elevation noise
		smoothed := smoothAltitudes(rawAlts)
		for i := range pts {
			pts[i][2] = smoothed[i]
		}
		gainUV, gainVU := computeDirectionalGain(smoothed, 1.5)

		edgeID := len(edges)
		edges = append(edges, GraphEdge{
			ID:       edgeID,
			U:        uID,
			V:        vID,
			Length:   length,
			GainUV:   gainUV,
			GainVU:   gainVU,
			RunCount: minRuns,
			Points:   pts,
		})
		nodes[uID].Edges = append(nodes[uID].Edges, edgeID)
		if vID != uID {
			nodes[vID].Edges = append(nodes[vID].Edges, edgeID)
		}
	}

	// Trace chains starting from every junction
	for k := range isJunction {
		for _, nb := range rawAdj[k] {
			if !reachable[nb] {
				continue
			}
			buildChainEdge(k, nb)
		}
	}

	return &TrailGraph{
		OriginLat:     proj.lat0,
		OriginLon:     proj.lon0,
		StartNode:     startNodeID,
		SnapDistanceM: snapDistM,
		Nodes:         nodes,
		Edges:         edges,
	}
}

// smoothAltitudes applies a 3-point weighted moving average [0.25, 0.5, 0.25] keeping endpoints fixed.
func smoothAltitudes(alts []float64) []float64 {
	n := len(alts)
	if n <= 2 {
		out := make([]float64, n)
		copy(out, alts)
		return out
	}
	out := make([]float64, n)
	out[0] = alts[0]
	out[n-1] = alts[n-1]
	for i := 1; i < n-1; i++ {
		out[i] = 0.25*alts[i-1] + 0.5*alts[i] + 0.25*alts[i+1]
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
		// Account for any net positive gain at the final endpoint if not yet flushed
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

// DetectFrequentStarts scans cached activities in ActivityStore (or disk) and returns
// the top starting locations clustered within ~250m so the UI can offer quick-pick start points.
func DetectFrequentStarts(dataDir string, limit int) []StartCluster {
	if limit <= 0 {
		limit = 5
	}
	activitiesDir := filepath.Join(dataDir, "activities")
	entries, err := os.ReadDir(activitiesDir)
	if err != nil {
		return nil
	}

	type startRec struct {
		lat, lon float64
		name     string
	}
	type cluster struct {
		sumLat, sumLon float64
		count          int
		sampleName     string
	}

	// Quick lightweight struct that only reads start_latlng (no streams!) so this is very fast
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
