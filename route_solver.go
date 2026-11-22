package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// RouteStep represents a single directed traversal of a GraphEdge in a route.
type RouteStep struct {
	EdgeID  int
	From    int
	To      int
	Forward bool // true if From == Edge.U
}

// RepeatLoopInfo describes a repeated high-elevation loop within a route.
type RepeatLoopInfo struct {
	Laps           int     `json:"laps"`
	LoopDistanceKm float64 `json:"loop_distance_km"`
	LoopGainM      float64 `json:"loop_gain_m"`
}

// RouteResult is a single generated route option ready for UI visualization and GPX export.
type RouteResult struct {
	Rank             int             `json:"rank"`
	Name             string          `json:"name"`
	DistanceM        float64         `json:"distance_m"`
	DistanceKm       float64         `json:"distance_km"`
	ElevationGainM   float64         `json:"elevation_gain_m"`
	ElevationLossM   float64         `json:"elevation_loss_m"`
	VertPerKm        float64         `json:"vert_per_km"`
	OverlapM         float64         `json:"overlap_m"`
	OverlapPct       float64         `json:"overlap_pct"`
	DistinctLabel    string          `json:"distinct_label"` // e.g. "Top Vert", "Distinct", "Variant"
	MaxSimilarityPct float64         `json:"max_similarity_pct"`
	RepeatInfo       *RepeatLoopInfo `json:"repeat_info,omitempty"`
	Coordinates      [][2]float64    `json:"coordinates"`       // [lat, lon] downsampled for map
	ProfileDistKm    []float64       `json:"profile_dist_km"`   // elevation profile X axis
	ProfileEleM      []float64       `json:"profile_ele_m"`     // elevation profile Y axis
	FullPoints       [][3]float64    `json:"-"`                 // full [lat, lon, ele] for GPX export
	edgeSet          map[int]float64 // undirected edgeID -> length (for diversity comparison)
}

// RouteSearchOutput bundles the top routes and graph metadata.
type RouteSearchOutput struct {
	Request         RouteBuildRequest `json:"request"`
	Routes          []RouteResult     `json:"routes"`
	GraphNodes      int               `json:"graph_nodes"`
	GraphEdges      int               `json:"graph_edges"`
	ActivitiesUsed  int               `json:"activities_used"`
	ActivitiesTotal int               `json:"activities_total"`
	SnapDistanceM   float64           `json:"snap_distance_m"`
	CandidatesFound int               `json:"candidates_found"`
	ElapsedMs       int64             `json:"elapsed_ms"`
	NetworkOverlay  []NetworkPolyline `json:"network_overlay,omitempty"`
}

// candidateRoute is an internal lightweight representation during search.
type candidateRoute struct {
	steps      []RouteStep
	length     float64
	gain       float64
	loss       float64
	overlap    float64
	repeatInfo *RepeatLoopInfo
	edgeSet    map[int]float64 // unique undirected edges used -> edge length
}

// dijkstraDistances computes shortest path distance from source to every node in g.
func dijkstraDistances(g *TrailGraph, source int) ([]float64, []int, []int) {
	n := len(g.Nodes)
	dist := make([]float64, n)
	prevNode := make([]int, n)
	prevEdge := make([]int, n)
	for i := range dist {
		dist[i] = math.Inf(1)
		prevNode[i] = -1
		prevEdge[i] = -1
	}
	dist[source] = 0

	visited := make([]bool, n)
	type heapItem struct {
		node int
		d    float64
	}
	h := []heapItem{{node: source, d: 0}}
	push := func(it heapItem) {
		h = append(h, it)
		i := len(h) - 1
		for i > 0 {
			p := (i - 1) / 2
			if h[p].d <= h[i].d {
				break
			}
			h[p], h[i] = h[i], h[p]
			i = p
		}
	}
	pop := func() heapItem {
		top := h[0]
		last := h[len(h)-1]
		h = h[:len(h)-1]
		if len(h) > 0 {
			h[0] = last
			i := 0
			for {
				l, r := 2*i+1, 2*i+2
				smallest := i
				if l < len(h) && h[l].d < h[smallest].d {
					smallest = l
				}
				if r < len(h) && h[r].d < h[smallest].d {
					smallest = r
				}
				if smallest == i {
					break
				}
				h[i], h[smallest] = h[smallest], h[i]
				i = smallest
			}
		}
		return top
	}

	for len(h) > 0 {
		cur := pop()
		u := cur.node
		if visited[u] {
			continue
		}
		visited[u] = true
		for _, eid := range g.Nodes[u].Edges {
			e := &g.Edges[eid]
			v := e.Other(u)
			nd := cur.d + e.Length
			if nd < dist[v] {
				dist[v] = nd
				prevNode[v] = u
				prevEdge[v] = eid
				push(heapItem{node: v, d: nd})
			}
		}
	}
	return dist, prevNode, prevEdge
}

// reconstructShortestPath reconstructs the RouteStep slice from source to target.
func reconstructShortestPath(g *TrailGraph, source, target int, prevNode, prevEdge []int) ([]RouteStep, bool) {
	if source == target {
		return nil, true
	}
	if prevNode[target] == -1 {
		return nil, false
	}
	var rev []RouteStep
	curr := target
	for curr != source {
		p := prevNode[curr]
		eid := prevEdge[curr]
		if p == -1 || eid == -1 {
			return nil, false
		}
		e := &g.Edges[eid]
		rev = append(rev, RouteStep{
			EdgeID:  eid,
			From:    p,
			To:      curr,
			Forward: e.U == p,
		})
		curr = p
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, true
}

// dijkstraWithPenalty computes shortest path from source to target while penalizing avoided edges.
func dijkstraWithPenalty(g *TrailGraph, source, target int, avoidEdges map[int]bool, penaltyFactor float64) ([]RouteStep, bool) {
	n := len(g.Nodes)
	cost := make([]float64, n)
	prevNode := make([]int, n)
	prevEdge := make([]int, n)
	for i := range cost {
		cost[i] = math.Inf(1)
		prevNode[i] = -1
		prevEdge[i] = -1
	}
	cost[source] = 0
	visited := make([]bool, n)

	for iter := 0; iter < n; iter++ {
		u := -1
		best := math.Inf(1)
		for i := 0; i < n; i++ {
			if !visited[i] && cost[i] < best {
				best = cost[i]
				u = i
			}
		}
		if u == -1 || u == target {
			break
		}
		visited[u] = true
		for _, eid := range g.Nodes[u].Edges {
			e := &g.Edges[eid]
			v := e.Other(u)
			w := e.Length
			if avoidEdges[eid] {
				w *= penaltyFactor
			}
			if cost[u]+w < cost[v] {
				cost[v] = cost[u] + w
				prevNode[v] = u
				prevEdge[v] = eid
			}
		}
	}
	return reconstructShortestPath(g, source, target, prevNode, prevEdge)
}

// halfPath is a clean simple path starting from an anchor node used in meet-in-the-middle loop assembly.
type halfPath struct {
	steps         []RouteStep
	endNode       int
	length        float64
	gainForward   float64
	gainReverse   float64
	edgeCounts    map[int]int
	visitedNodes  map[int]bool // intermediate nodes (excluding anchor & endNode)
	corridorCells map[gridKey]struct{}
}

// evalLoopCandidate evaluates a closed sequence of steps for exact length, smoothed elevation
// gain/loss (from the continuous relative elevation profile), and both edge + spatial corridor overlap.
func evalLoopCandidate(g *TrailGraph, steps []RouteStep, repeatInfo *RepeatLoopInfo, exemptOverlapEdges map[int]int) *candidateRoute {
	if len(steps) == 0 {
		return nil
	}
	// Strictly enforce closed, continuous walk
	if steps[0].From != steps[len(steps)-1].To {
		return nil
	}
	for i := 1; i < len(steps); i++ {
		if steps[i].From != steps[i-1].To {
			return nil
		}
	}

	length := 0.0
	counts := make(map[int]int, len(steps))
	edgeSet := make(map[int]float64, len(steps))

	// Build continuous relative elevation sequence along the entire loop
	relAlts := make([]float64, 0, len(steps)*4+1)
	curEle := 0.0
	relAlts = append(relAlts, curEle)

	// Track spatial corridor cells occupied by distinct edges to catch parallel out-and-backs
	corridorOwner := make(map[gridKey]int, len(steps)*3)
	spatialOverlapM := 0.0
	const corridorCellM = 25.0

	for _, st := range steps {
		e := &g.Edges[st.EdgeID]
		length += e.Length
		counts[st.EdgeID]++
		edgeSet[st.EdgeID] = e.Length

		// Append relative elevation profile of this edge
		nPts := len(e.Points)
		if nPts >= 2 {
			if st.Forward {
				base := e.Points[0][2]
				for i := 1; i < nPts; i++ {
					relAlts = append(relAlts, curEle+(e.Points[i][2]-base))
				}
				curEle += e.NetDeltaUV
			} else {
				base := e.Points[nPts-1][2]
				for i := nPts - 2; i >= 0; i-- {
					relAlts = append(relAlts, curEle+(e.Points[i][2]-base))
				}
				curEle -= e.NetDeltaUV
			}
		}

		// Spatial corridor overlap check: only count when a *different* edge enters an already-used cell
		if counts[st.EdgeID] == 1 {
			for _, ck := range e.CorridorCells {
				if owner, exists := corridorOwner[ck]; exists && owner != st.EdgeID {
					if exemptOverlapEdges == nil || exemptOverlapEdges[st.EdgeID] <= 1 {
						spatialOverlapM += corridorCellM
					}
				} else {
					corridorOwner[ck] = st.EdgeID
				}
			}
		}
	}

	// Exact undirected edge-ID overlap
	edgeOverlapM := 0.0
	for eid, cnt := range counts {
		allowed := 1
		if exemptOverlapEdges != nil {
			if ex, ok := exemptOverlapEdges[eid]; ok && ex > allowed {
				allowed = ex
			}
		}
		if cnt > allowed {
			edgeOverlapM += float64(cnt-allowed) * g.Edges[eid].Length
		}
	}

	totalOverlap := edgeOverlapM + spatialOverlapM
	if totalOverlap > length {
		totalOverlap = length
	}

	// Distribute any residual loop-closure elevation drift linearly so Gain == Loss on a closed loop
	if n := len(relAlts); n >= 2 {
		drift := relAlts[n-1] - relAlts[0]
		if math.Abs(drift) > 0.01 {
			denom := float64(n - 1)
			for i := 1; i < n; i++ {
				relAlts[i] -= drift * (float64(i) / denom)
			}
		}
	}

	// Compute clean elevation gain/loss along the continuous loop profile with 1.5m hysteresis
	gain, loss := computeDirectionalGain(relAlts, 1.5)

	return &candidateRoute{
		steps:      steps,
		length:     length,
		gain:       gain,
		loss:       loss,
		overlap:    totalOverlap,
		repeatInfo: repeatInfo,
		edgeSet:    edgeSet,
	}
}

func reverseSteps(steps []RouteStep) []RouteStep {
	n := len(steps)
	rev := make([]RouteStep, n)
	for i := 0; i < n; i++ {
		st := steps[n-1-i]
		rev[i] = RouteStep{
			EdgeID:  st.EdgeID,
			From:    st.To,
			To:      st.From,
			Forward: !st.Forward,
		}
	}
	return rev
}

// isSharpUTurn returns true if transitioning from lastEdge (arriving at u) to nextEdge (leaving u)
// reverses straight back on a parallel line (> ~164 degrees), while still allowing genuine mountain switchbacks.
func isSharpUTurn(g *TrailGraph, u, lastEdgeID, nextEdgeID int) bool {
	if lastEdgeID < 0 {
		return false
	}
	if lastEdgeID == nextEdgeID {
		return true
	}
	ax, ay := g.Edges[lastEdgeID].ArriveDir(u)
	lx, ly := g.Edges[nextEdgeID].LeaveDir(u)
	if (ax == 0 && ay == 0) || (lx == 0 && ly == 0) {
		return false
	}
	dot := ax*lx + ay*ly
	return dot < -0.96
}

// FindTopRoutes runs the anytime multi-strategy route optimizer on TrailGraph.
func FindTopRoutes(ctx context.Context, g *TrailGraph, req RouteBuildRequest, progressFn func(string)) (*RouteSearchOutput, error) {
	startTime := time.Now()
	if g == nil || len(g.Nodes) == 0 || len(g.Edges) == 0 {
		return nil, fmt.Errorf("trail graph is empty")
	}

	minDistM := (req.TargetDistanceKm - req.ToleranceKm) * 1000.0
	if minDistM < 200 {
		minDistM = 200
	}
	maxDistM := (req.TargetDistanceKm + req.ToleranceKm) * 1000.0
	maxOverlapFrac := req.MaxOverlapPct / 100.0

	distToStart, _, _ := dijkstraDistances(g, g.StartNode)

	timeout := time.Duration(req.TimeoutSec) * time.Second
	searchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	workers := req.MaxCPUs
	if workers < 1 {
		workers = defaultRouteWorkers()
	}

	if progressFn != nil {
		progressFn(fmt.Sprintf("Searching for top %d clean loops (%.1f ± %.1f km, max %.0f%% overlap)...",
			req.MaxResults, req.TargetDistanceKm, req.ToleranceKm, req.MaxOverlapPct))
	}

	var mu sync.Mutex
	var allCandidates []*candidateRoute
	edgeHeat := make([]float64, len(g.Edges))

	addCandidate := func(c *candidateRoute) {
		if c == nil || len(c.steps) == 0 {
			return
		}
		// Strictly require every route to start and finish at g.StartNode
		if c.steps[0].From != g.StartNode || c.steps[len(c.steps)-1].To != g.StartNode {
			return
		}
		if c.length < minDistM || c.length > maxDistM {
			return
		}
		if c.overlap/c.length > maxOverlapFrac+1e-9 {
			return
		}
		mu.Lock()
		allCandidates = append(allCandidates, c)
		if len(allCandidates) <= 2500 {
			for eid := range c.edgeSet {
				edgeHeat[eid] += 0.04
			}
		}
		mu.Unlock()
	}

	var wg sync.WaitGroup
	mainBudget := timeout
	if req.AllowRepeatLoops {
		mainBudget = (timeout * 3) / 4
	}
	mainCtx, mainCancel := context.WithTimeout(searchCtx, mainBudget)
	defer mainCancel()

	// Phase 0: Targeted Summit-Seeking Disjoint Loops (finds big climbs even across flat approaches!)
	runSummitSeekingLoops(mainCtx, g, distToStart, minDistM, maxDistM, maxOverlapFrac, addCandidate)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)*7919))

			round := 0
			for mainCtx.Err() == nil {
				round++
				var penalties []float64
				if round >= 2 {
					penalties = make([]float64, len(g.Edges))
					mu.Lock()
					copy(penalties, edgeHeat)
					mu.Unlock()
				}

				// Phase A: Compass-swept Meet-in-the-Middle (primary clean loop generator)
				runMeetInTheMiddleBatch(mainCtx, g, g.StartNode, distToStart, minDistM, maxDistM, maxOverlapFrac, penalties, rng, 200, addCandidate)

				// Phase B: Smooth node-simple guided loop walks
				runGuidedWalkBatch(mainCtx, g, g.StartNode, distToStart, minDistM, maxDistM, maxOverlapFrac, penalties, rng, 180, addCandidate)

				if round >= 25 && len(g.Nodes) < 80 {
					break
				}
			}
		}(w)
	}
	wg.Wait()

	// Phase C: Repeating high-elevation loops (when enabled)
	if req.AllowRepeatLoops && searchCtx.Err() == nil {
		if progressFn != nil {
			progressFn(fmt.Sprintf("Exploring repeating loops with >= %.0fm elevation gain...", req.MinLoopGainM))
		}
		runRepeatingLoopSearch(searchCtx, g, distToStart, minDistM, maxDistM, maxOverlapFrac, req.MinLoopGainM, addCandidate)
	}

	if ctx.Err() != nil && len(allCandidates) == 0 {
		return nil, ctx.Err()
	}

	if len(allCandidates) == 0 {
		return nil, fmt.Errorf("no valid loop routes found for %.1f ± %.1f km with <= %.0f%% overlap (try increasing tolerance, max overlap, or enabling repeating loops)",
			req.TargetDistanceKm, req.ToleranceKm, req.MaxOverlapPct)
	}

	selected := selectDiverseTopRoutes(allCandidates, req.MaxResults)

	results := make([]RouteResult, len(selected))
	for i, sel := range selected {
		results[i] = buildRouteResult(g, sel.cand, i+1, sel.label, sel.maxSimPct)
	}

	out := &RouteSearchOutput{
		Request:         req,
		Routes:          results,
		GraphNodes:      len(g.Nodes),
		GraphEdges:      len(g.Edges),
		ActivitiesUsed:  g.ActivitiesUsed,
		ActivitiesTotal: g.ActivitiesLoaded,
		SnapDistanceM:   math.Round(g.SnapDistanceM*10) / 10,
		CandidatesFound: len(allCandidates),
		ElapsedMs:       time.Since(startTime).Milliseconds(),
		NetworkOverlay:  g.ExportNetworkOverlay(800),
	}

	if progressFn != nil {
		progressFn(fmt.Sprintf("Done! Found %d routes (from %d candidates in %.1fs)",
			len(results), len(allCandidates), float64(out.ElapsedMs)/1000.0))
	}

	return out, nil
}

// runSummitSeekingLoops targets the highest-elevation reachable junctions in the graph and
// constructs edge-disjoint climb + descent loops (StartNode -> Summit -> StartNode), including
// randomized weight perturbations to explore diverse big-climb loops in [minDistM, maxDistM].
func runSummitSeekingLoops(
	ctx context.Context,
	g *TrailGraph,
	distToStart []float64,
	minDistM, maxDistM, maxOverlapFrac float64,
	emit func(*candidateRoute),
) {
	type summitNode struct {
		id  int
		ele float64
	}
	var summits []summitNode
	startEle := g.Nodes[g.StartNode].Ele
	for i := range g.Nodes {
		if i == g.StartNode {
			continue
		}
		d := distToStart[i]
		if math.IsInf(d, 1) || d < 500 || d*2.0 > maxDistM {
			continue
		}
		if len(g.Nodes[i].Edges) < 2 {
			continue
		}
		summits = append(summits, summitNode{id: i, ele: g.Nodes[i].Ele - startEle})
	}
	if len(summits) == 0 {
		return
	}
	sort.Slice(summits, func(i, j int) bool { return summits[i].ele > summits[j].ele })
	// Keep top 25 highest-elevation summits, spatially spaced so we explore multiple ridges/peaks
	var pickedSummits []int
	for _, s := range summits {
		if len(pickedSummits) >= 25 {
			break
		}
		tooClose := false
		for _, prevID := range pickedSummits {
			if math.Hypot(g.Nodes[s.id].X-g.Nodes[prevID].X, g.Nodes[s.id].Y-g.Nodes[prevID].Y) < 400.0 {
				tooClose = true
				break
			}
		}
		if !tooClose {
			pickedSummits = append(pickedSummits, s.id)
		}
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano() + 99991))
	n := len(g.Nodes)

	// Helper: Dijkstra with customizable per-edge multiplier
	weightedPath := func(src, dst int, edgeWeight func(eid int) float64) ([]RouteStep, bool) {
		cost := make([]float64, n)
		prevNode := make([]int, n)
		prevEdge := make([]int, n)
		for i := range cost {
			cost[i] = math.Inf(1)
			prevNode[i] = -1
			prevEdge[i] = -1
		}
		cost[src] = 0
		visited := make([]bool, n)

		for iter := 0; iter < n; iter++ {
			u := -1
			best := math.Inf(1)
			for i := 0; i < n; i++ {
				if !visited[i] && cost[i] < best {
					best = cost[i]
					u = i
				}
			}
			if u == -1 || u == dst {
				break
			}
			visited[u] = true
			for _, eid := range g.Nodes[u].Edges {
				e := &g.Edges[eid]
				v := e.Other(u)
				w := edgeWeight(eid)
				if cost[u]+w < cost[v] {
					cost[v] = cost[u] + w
					prevNode[v] = u
					prevEdge[v] = eid
				}
			}
		}
		return reconstructShortestPath(g, src, dst, prevNode, prevEdge)
	}

	for _, summit := range pickedSummits {
		if ctx.Err() != nil {
			return
		}
		// Try 6 randomized climb/descent pairs per summit (plus shortest disjoint pair)
		for trial := 0; trial < 6; trial++ {
			if ctx.Err() != nil {
				return
			}
			noise := make([]float64, len(g.Edges))
			for i := range noise {
				if trial == 0 {
					noise[i] = 1.0
				} else {
					// Random perturbation favors longer/steeper scenic trails to reach target distance
					noise[i] = 0.5 + rng.Float64()*1.8
				}
			}
			// Outbound: StartNode -> summit (slightly prefer climbing edges)
			pOut, ok := weightedPath(g.StartNode, summit, func(eid int) float64 {
				e := &g.Edges[eid]
				bonus := 1.0 / (1.0 + (e.GainUV+e.GainVU)/math.Max(e.Length, 30.0)*3.0)
				return e.Length * noise[eid] * bonus
			})
			if !ok || len(pOut) == 0 {
				continue
			}

			// Heavily penalize outbound edges + their corridor cells to get a truly disjoint return
			avoidEdges := make(map[int]bool, len(pOut))
			avoidCorr := make(map[gridKey]bool, len(pOut)*3)
			for _, st := range pOut {
				avoidEdges[st.EdgeID] = true
				for _, ck := range g.Edges[st.EdgeID].CorridorCells {
					avoidCorr[ck] = true
				}
			}

			pBack, ok := weightedPath(summit, g.StartNode, func(eid int) float64 {
				e := &g.Edges[eid]
				w := e.Length * noise[eid]
				if avoidEdges[eid] {
					return w * 40.0
				}
				hits := 0
				for _, ck := range e.CorridorCells {
					if avoidCorr[ck] {
						hits++
					}
				}
				if hits > 0 {
					w *= (1.0 + float64(hits)*4.0)
				}
				return w
			})
			if !ok || len(pBack) == 0 {
				continue
			}

			combined := make([]RouteStep, 0, len(pOut)+len(pBack))
			combined = append(combined, pOut...)
			combined = append(combined, pBack...)
			emit(evalLoopCandidate(g, combined, nil, nil))
		}
	}
}

// runMeetInTheMiddleBatch generates clean, node-simple outward paths fanned across compass
// bearings and pairs non-overlapping paths meeting at the same junction to form smooth loops.
func runMeetInTheMiddleBatch(
	ctx context.Context,
	g *TrailGraph,
	anchor int,
	distToAnchor []float64,
	minDistM, maxDistM, maxOverlapFrac float64,
	edgePenalty []float64,
	rng *rand.Rand,
	numWalks int,
	emit func(*candidateRoute),
) {
	halfMin := minDistM * 0.20
	halfMax := maxDistM * 0.65
	maxSteps := int(math.Max(400, maxDistM/20.0))
	anchorX, anchorY := g.Nodes[anchor].X, g.Nodes[anchor].Y

	byEnd := make(map[int][]*halfPath)
	const maxPerNode = 12

	for w := 0; w < numWalks; w++ {
		if ctx.Err() != nil {
			return
		}
		temp := 0.30 + rng.Float64()*1.4
		divWeight := 0.0
		if edgePenalty != nil && w%3 != 0 {
			divWeight = 0.35 + rng.Float64()*1.4
		}
		// Assign a target compass bearing so walks sweep cleanly outward in all directions
		targetAngle := (float64(w%16) / 16.0) * 2.0 * math.Pi
		dirX, dirY := math.Cos(targetAngle), math.Sin(targetAngle)
		useCompass := (w % 4) != 0

		u := anchor
		curDist := 0.0
		curGainFwd := 0.0
		curGainRev := 0.0
		lastEdge := -1
		edgeCounts := make(map[int]int, 32)
		visited := make(map[int]bool, 32)
		visited[anchor] = true
		corridor := make(map[gridKey]struct{}, 48)
		var steps []RouteStep

		for step := 0; step < maxSteps; step++ {
			edges := g.Nodes[u].Edges
			if len(edges) == 0 {
				break
			}

			type choice struct {
				eid   int
				v     int
				score float64
			}
			var choices []choice

			for _, eid := range edges {
				if eid == lastEdge {
					continue
				}
				e := &g.Edges[eid]
				v := e.Other(u)

				// Strict node-simple path (only allow returning to anchor if it closes a valid loop!)
				if v == anchor {
					if curDist+e.Length < minDistM || curDist+e.Length > maxDistM {
						continue
					}
				} else if visited[v] {
					continue
				}

				if edgeCounts[eid] > 0 {
					continue
				}
				// Reject sharp U-turns at junctions
				if isSharpUTurn(g, u, lastEdge, eid) {
					continue
				}

				newDist := curDist + e.Length
				if v != anchor && newDist > halfMax {
					continue
				}
				if newDist+distToAnchor[v] > maxDistM {
					continue
				}

				// Penalize (rather than hard-kill) edges whose corridor touches this half-path,
				// so tight mountain switchbacks remain reachable while parallel folds are discouraged.
				corridorHits := 0
				for _, ck := range e.CorridorCells {
					if _, exists := corridor[ck]; exists {
						corridorHits++
					}
				}

				// Elevation gain score (capped gain rate so no single steep glitch dominates)
				fwdGain := e.GainFrom(u)
				revGain := e.GainFrom(v)
				gainRate := math.Min(0.35, (fwdGain+0.5*revGain)/math.Max(e.Length, 30.0))
				score := 1.0 + gainRate*35.0
				if g.Nodes[v].Ele > g.Nodes[u].Ele {
					score *= 1.25
				}
				if corridorHits > 0 {
					score /= (1.0 + float64(corridorHits)*2.5)
				}

				// Favor smooth forward continuation (small turn angle)
				if lastEdge >= 0 {
					ax, ay := g.Edges[lastEdge].ArriveDir(u)
					lx, ly := e.LeaveDir(u)
					dot := ax*lx + ay*ly
					score *= (1.0 + 0.4*math.Max(0, dot))
				}

				// Compass-sector outward sweep bias
				if useCompass && v != anchor {
					vx := g.Nodes[v].X - anchorX
					vy := g.Nodes[v].Y - anchorY
					vmag := math.Hypot(vx, vy)
					if vmag > 10 {
						align := (vx*dirX + vy*dirY) / vmag
						score *= (1.0 + 0.6*math.Max(0, align))
					}
				}

				// Favor moving outward early
				if distToAnchor[v] > distToAnchor[u] {
					score *= 1.25
				}

				if divWeight > 0 && edgePenalty[eid] > 0 {
					score /= (1.0 + divWeight*edgePenalty[eid])
				}

				choices = append(choices, choice{eid: eid, v: v, score: score})
			}

			if len(choices) == 0 {
				break
			}

			picked := pickWeightedChoice(len(choices), func(i int) float64 {
				return math.Pow(math.Max(choices[i].score, 1e-4), 1.0/temp)
			}, rng)

			ch := choices[picked]
			e := &g.Edges[ch.eid]
			fwd := e.U == u
			steps = append(steps, RouteStep{
				EdgeID:  ch.eid,
				From:    u,
				To:      ch.v,
				Forward: fwd,
			})
			curDist += e.Length
			if fwd {
				curGainFwd += e.GainUV
				curGainRev += e.GainVU
			} else {
				curGainFwd += e.GainVU
				curGainRev += e.GainUV
			}
			edgeCounts[ch.eid]++
			for _, ck := range e.CorridorCells {
				corridor[ck] = struct{}{}
			}
			lastEdge = ch.eid
			u = ch.v

			if u == anchor && curDist >= minDistM && curDist <= maxDistM {
				stCopy := make([]RouteStep, len(steps))
				copy(stCopy, steps)
				emit(evalLoopCandidate(g, stCopy, nil, nil))
				break
			}
			visited[u] = true

			if u != anchor && curDist >= halfMin && curDist <= halfMax {
				list := byEnd[u]
				if len(list) < maxPerNode {
					stCopy := make([]RouteStep, len(steps))
					copy(stCopy, steps)
					ecCopy := make(map[int]int, len(edgeCounts))
					for k, v := range edgeCounts {
						ecCopy[k] = v
					}
					visCopy := make(map[int]bool, len(visited))
					for k := range visited {
						if k != anchor && k != u {
							visCopy[k] = true
						}
					}
					corrCopy := make(map[gridKey]struct{}, len(corridor))
					for k := range corridor {
						corrCopy[k] = struct{}{}
					}
					byEnd[u] = append(list, &halfPath{
						steps:         stCopy,
						endNode:       u,
						length:        curDist,
						gainForward:   curGainFwd,
						gainReverse:   curGainRev,
						edgeCounts:    ecCopy,
						visitedNodes:  visCopy,
						corridorCells: corrCopy,
					})
				}
			}
		}
	}

	// Combine pairs of half-paths meeting at each junction u
	for _, list := range byEnd {
		if ctx.Err() != nil {
			return
		}
		if len(list) < 2 {
			continue
		}
		for i := 0; i < len(list); i++ {
			p1 := list[i]
			for j := i + 1; j < len(list); j++ {
				p2 := list[j]
				totalLen := p1.length + p2.length
				if totalLen < minDistM || totalLen > maxDistM {
					continue
				}

				// Check junction U-turn at meeting node u (joining p1 -> rev(p2))
				last1 := p1.steps[len(p1.steps)-1].EdgeID
				last2 := p2.steps[len(p2.steps)-1].EdgeID
				if last1 == last2 {
					continue
				}
				if isSharpUTurn(g, p1.endNode, last1, last2) {
					continue
				}

				// Quick exact edge + spatial corridor overlap filter
				ov := 0.0
				maxOv := maxOverlapFrac * totalLen
				for eid := range p1.edgeCounts {
					if _, ok := p2.edgeCounts[eid]; ok {
						ov += g.Edges[eid].Length
						if ov > maxOv+1e-6 {
							break
						}
					}
				}
				if ov > maxOv+1e-6 {
					continue
				}

				// Disallow heavy intermediate node sharing (keeps loops clean and non-figure-8)
				sharedMidNodes := 0
				for nID := range p1.visitedNodes {
					if p2.visitedNodes[nID] {
						sharedMidNodes++
						if sharedMidNodes > 2 {
							break
						}
					}
				}
				if sharedMidNodes > 2 {
					continue
				}

				// Evaluate both directions and emit the higher-gain one
				gain12 := p1.gainForward + p2.gainReverse
				gain21 := p2.gainForward + p1.gainReverse
				var combined []RouteStep
				if gain12 >= gain21 {
					combined = make([]RouteStep, 0, len(p1.steps)+len(p2.steps))
					combined = append(combined, p1.steps...)
					combined = append(combined, reverseSteps(p2.steps)...)
				} else {
					combined = make([]RouteStep, 0, len(p1.steps)+len(p2.steps))
					combined = append(combined, p2.steps...)
					combined = append(combined, reverseSteps(p1.steps)...)
				}
				emit(evalLoopCandidate(g, combined, nil, nil))
			}
		}
	}
}

// runGuidedWalkBatch performs clean node-simple full-loop walks that sweep outward and steer back home.
func runGuidedWalkBatch(
	ctx context.Context,
	g *TrailGraph,
	anchor int,
	distToAnchor []float64,
	minDistM, maxDistM, maxOverlapFrac float64,
	edgePenalty []float64,
	rng *rand.Rand,
	numWalks int,
	emit func(*candidateRoute),
) {
	targetDist := (minDistM + maxDistM) / 2.0
	maxOverlapDist := maxOverlapFrac * maxDistM
	maxSteps := int(math.Max(400, maxDistM/20.0))

	for w := 0; w < numWalks; w++ {
		if ctx.Err() != nil {
			return
		}
		temp := 0.3 + rng.Float64()*1.3
		divWeight := 0.0
		if edgePenalty != nil && w%2 == 0 {
			divWeight = 0.4 + rng.Float64()*1.2
		}

		u := anchor
		curDist := 0.0
		curOverlap := 0.0
		lastEdge := -1
		edgeCounts := make(map[int]int, 48)
		visited := make(map[int]bool, 48)
		visited[anchor] = true
		corridor := make(map[gridKey]struct{}, 64)
		var steps []RouteStep

		for step := 0; step < maxSteps; step++ {
			edges := g.Nodes[u].Edges
			if len(edges) == 0 {
				break
			}

			type choice struct {
				eid   int
				v     int
				score float64
				ovAdd float64
			}
			var choices []choice
			progress := curDist / targetDist

			for _, eid := range edges {
				if eid == lastEdge {
					continue
				}
				e := &g.Edges[eid]
				v := e.Other(u)

				if v == anchor {
					if curDist+e.Length < minDistM || curDist+e.Length > maxDistM {
						continue
					}
				} else if visited[v] {
					// Only allow revisiting a node right near home (short cul-de-sac stem) if within overlap budget
					if distToAnchor[v] > maxOverlapDist {
						continue
					}
				}

				if isSharpUTurn(g, u, lastEdge, eid) {
					continue
				}

				newDist := curDist + e.Length
				if newDist+distToAnchor[v] > maxDistM {
					continue
				}

				ovAdd := 0.0
				if edgeCounts[eid] >= 1 {
					ovAdd = e.Length
				} else {
					for _, ck := range e.CorridorCells {
						if _, exists := corridor[ck]; exists {
							ovAdd += 25.0
						}
					}
				}
				if curOverlap+ovAdd > maxOverlapDist {
					continue
				}

				gainRate := math.Min(0.35, e.GainFrom(u)/math.Max(e.Length, 30.0))
				score := 1.0 + gainRate*35.0

				if ovAdd > 0 {
					score *= 0.08
				}

				// Smooth forward continuation bonus
				if lastEdge >= 0 {
					ax, ay := g.Edges[lastEdge].ArriveDir(u)
					lx, ly := e.LeaveDir(u)
					dot := ax*lx + ay*ly
					score *= (1.0 + 0.4*math.Max(0, dot))
				}

				if progress < 0.55 {
					if distToAnchor[v] > distToAnchor[u] {
						score *= 1.3
					}
				} else {
					if v == anchor && newDist >= minDistM && newDist <= maxDistM {
						score *= 15.0
					} else if distToAnchor[v] < distToAnchor[u] {
						score *= 2.2 + (progress-0.55)*4.5
					}
				}

				if divWeight > 0 && edgePenalty[eid] > 0 {
					score /= (1.0 + divWeight*edgePenalty[eid])
				}

				choices = append(choices, choice{eid: eid, v: v, score: score, ovAdd: ovAdd})
			}

			if len(choices) == 0 {
				break
			}

			picked := pickWeightedChoice(len(choices), func(i int) float64 {
				return math.Pow(math.Max(choices[i].score, 1e-4), 1.0/temp)
			}, rng)

			ch := choices[picked]
			e := &g.Edges[ch.eid]
			steps = append(steps, RouteStep{
				EdgeID:  ch.eid,
				From:    u,
				To:      ch.v,
				Forward: e.U == u,
			})
			curDist += e.Length
			curOverlap += ch.ovAdd
			edgeCounts[ch.eid]++
			for _, ck := range e.CorridorCells {
				corridor[ck] = struct{}{}
			}
			lastEdge = ch.eid
			u = ch.v

			if u == anchor && curDist >= minDistM && curDist <= maxDistM {
				emit(evalLoopCandidate(g, steps, nil, nil))
				break
			}
			visited[u] = true
		}
	}
}

// runRepeatingLoopSearch discovers high-elevation sub-loops (gain >= minLoopGainM) and composes
// multi-lap routes (both anchored at start and lollipop stem + repeated loop at a reachable junction).
func runRepeatingLoopSearch(
	ctx context.Context,
	g *TrailGraph,
	distToStart []float64,
	minDistM, maxDistM, maxOverlapFrac, minLoopGainM float64,
	emit func(*candidateRoute),
) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano() + 31337))

	type anchorScore struct {
		node  int
		score float64
	}
	var anchors []anchorScore
	for i := range g.Nodes {
		if i == g.StartNode {
			continue
		}
		d := distToStart[i]
		if math.IsInf(d, 1) || d > maxDistM*0.35 {
			continue
		}
		if len(g.Nodes[i].Edges) < 2 {
			continue
		}
		localGain := 0.0
		for _, eid := range g.Nodes[i].Edges {
			e := &g.Edges[eid]
			localGain += math.Max(e.GainUV, e.GainVU)
		}
		anchors = append(anchors, anchorScore{node: i, score: localGain + float64(len(g.Nodes[i].Edges))*25.0})
	}
	sort.Slice(anchors, func(i, j int) bool { return anchors[i].score > anchors[j].score })
	if len(anchors) > 10 {
		anchors = anchors[:10]
	}

	anchorList := append([]int{g.StartNode}, make([]int, 0, len(anchors))...)
	for _, a := range anchors {
		anchorList = append(anchorList, a.node)
	}

	_, prevNodeFromStart, prevEdgeFromStart := dijkstraDistances(g, g.StartNode)

	for _, anchor := range anchorList {
		if ctx.Err() != nil {
			return
		}
		stemDist := distToStart[anchor]
		maxSingleLap := (maxDistM - 2*stemDist) / 2.0
		if maxSingleLap < 1000.0 {
			continue
		}
		minSingleLap := math.Max(1000.0, maxSingleLap*0.18)

		distToAnchor := distToStart
		if anchor != g.StartNode {
			distToAnchor, _, _ = dijkstraDistances(g, anchor)
		}

		var qualLoops []*candidateRoute
		collectLoop := func(c *candidateRoute) {
			if c != nil && c.gain >= minLoopGainM && c.length >= minSingleLap && c.length <= maxSingleLap {
				if c.overlap/c.length <= maxOverlapFrac+1e-9 {
					qualLoops = append(qualLoops, c)
				}
			}
		}

		runMeetInTheMiddleBatch(ctx, g, anchor, distToAnchor, minSingleLap, maxSingleLap, maxOverlapFrac, nil, rng, 150, collectLoop)
		runGuidedWalkBatch(ctx, g, anchor, distToAnchor, minSingleLap, maxSingleLap, maxOverlapFrac, nil, rng, 150, collectLoop)

		if len(qualLoops) == 0 {
			continue
		}

		sort.Slice(qualLoops, func(i, j int) bool {
			return (qualLoops[i].gain / qualLoops[i].length) > (qualLoops[j].gain / qualLoops[j].length)
		})
		if len(qualLoops) > 8 {
			qualLoops = qualLoops[:8]
		}

		var stemOut, stemBack []RouteStep
		if anchor != g.StartNode {
			var ok bool
			stemOut, ok = reconstructShortestPath(g, g.StartNode, anchor, prevNodeFromStart, prevEdgeFromStart)
			if !ok {
				continue
			}
			avoid := make(map[int]bool, len(stemOut))
			for _, st := range stemOut {
				avoid[st.EdgeID] = true
			}
			stemBack, ok = dijkstraWithPenalty(g, anchor, g.StartNode, avoid, 20.0)
			if !ok {
				stemBack = reverseSteps(stemOut)
			}
		}

		stemLen := 0.0
		for _, st := range stemOut {
			stemLen += g.Edges[st.EdgeID].Length
		}
		for _, st := range stemBack {
			stemLen += g.Edges[st.EdgeID].Length
		}

		for _, lp := range qualLoops {
			for k := 2; k <= 8; k++ {
				totalLen := stemLen + float64(k)*lp.length
				if totalLen > maxDistM {
					break
				}
				if totalLen >= minDistM && totalLen <= maxDistM {
					steps := make([]RouteStep, 0, len(stemOut)+k*len(lp.steps)+len(stemBack))
					steps = append(steps, stemOut...)
					for lap := 0; lap < k; lap++ {
						steps = append(steps, lp.steps...)
					}
					steps = append(steps, stemBack...)

					exempt := make(map[int]int, len(lp.edgeSet))
					for _, st := range lp.steps {
						exempt[st.EdgeID] += k
					}
					rInfo := &RepeatLoopInfo{
						Laps:           k,
						LoopDistanceKm: math.Round((lp.length/1000.0)*100) / 100,
						LoopGainM:      math.Round(lp.gain),
					}
					emit(evalLoopCandidate(g, steps, rInfo, exempt))
				}
			}
		}
	}
}

func pickWeightedChoice(n int, weightFn func(int) float64, rng *rand.Rand) int {
	total := 0.0
	weights := make([]float64, n)
	for i := 0; i < n; i++ {
		w := weightFn(i)
		if math.IsNaN(w) || math.IsInf(w, 0) || w < 0 {
			w = 0
		}
		weights[i] = w
		total += w
	}
	if total <= 0 {
		return rng.Intn(n)
	}
	r := rng.Float64() * total
	acc := 0.0
	for i, w := range weights {
		acc += w
		if r <= acc {
			return i
		}
	}
	return n - 1
}

// routeSimilarity returns the fraction [0..1] of shared undirected trail length between two candidates.
func routeSimilarity(a, b *candidateRoute) float64 {
	if a == nil || b == nil || len(a.edgeSet) == 0 || len(b.edgeSet) == 0 {
		return 0
	}
	var lenA, lenB, shared float64
	for eid, l := range a.edgeSet {
		lenA += l
		if _, ok := b.edgeSet[eid]; ok {
			shared += l
		}
	}
	for _, l := range b.edgeSet {
		lenB += l
	}
	denom := math.Min(lenA, lenB)
	if denom <= 0 {
		return 0
	}
	return shared / denom
}

type selectedRoute struct {
	cand      *candidateRoute
	label     string
	maxSimPct float64
}

// selectDiverseTopRoutes selects up to maxResults routes, prioritizing highest elevation gain
// while ensuring at least 3 of the results are mostly distinct (<= 45% shared trail where possible).
func selectDiverseTopRoutes(candidates []*candidateRoute, maxResults int) []selectedRoute {
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if math.Abs(candidates[i].gain-candidates[j].gain) > 0.5 {
			return candidates[i].gain > candidates[j].gain
		}
		return candidates[i].overlap < candidates[j].overlap
	})

	var picked []selectedRoute
	usedIdx := make([]bool, len(candidates))

	maxSimToPicked := func(c *candidateRoute) float64 {
		maxSim := 0.0
		for _, p := range picked {
			s := routeSimilarity(c, p.cand)
			if s > maxSim {
				maxSim = s
			}
		}
		return maxSim
	}

	picked = append(picked, selectedRoute{
		cand:      candidates[0],
		label:     "Top Vert",
		maxSimPct: 0,
	})
	usedIdx[0] = true

	distinctTarget := 3
	if maxResults < distinctTarget {
		distinctTarget = maxResults
	}
	for _, thresh := range []float64{0.45, 0.60, 0.75} {
		for i, c := range candidates {
			if len(picked) >= distinctTarget {
				break
			}
			if usedIdx[i] {
				continue
			}
			sim := maxSimToPicked(c)
			if sim <= thresh {
				label := "Distinct"
				if sim > 0.55 {
					label = "Semi-Distinct"
				}
				picked = append(picked, selectedRoute{
					cand:      c,
					label:     label,
					maxSimPct: math.Round(sim*1000) / 10,
				})
				usedIdx[i] = true
			}
		}
		if len(picked) >= distinctTarget {
			break
		}
	}

	for _, thresh := range []float64{0.85, 0.95} {
		for i, c := range candidates {
			if len(picked) >= maxResults {
				break
			}
			if usedIdx[i] {
				continue
			}
			sim := maxSimToPicked(c)
			if sim <= thresh {
				label := "Variant"
				if sim <= 0.45 {
					label = "Distinct"
				}
				picked = append(picked, selectedRoute{
					cand:      c,
					label:     label,
					maxSimPct: math.Round(sim*1000) / 10,
				})
				usedIdx[i] = true
			}
		}
		if len(picked) >= maxResults {
			break
		}
	}

	sort.Slice(picked, func(i, j int) bool {
		return picked[i].cand.gain > picked[j].cand.gain
	})
	if len(picked) > 0 {
		picked[0].label = "Top Vert"
	}

	return picked
}

// buildRouteResult expands a candidateRoute's edge steps into continuous coordinates, elevation profile, and summary metrics.
func buildRouteResult(g *TrailGraph, c *candidateRoute, rank int, distinctLabel string, maxSimPct float64) RouteResult {
	var fullPts [][3]float64
	// Anchor absolute elevation at StartNode's reference altitude and accumulate continuous relative deltas
	curEle := g.Nodes[g.StartNode].Ele

	for _, st := range c.steps {
		e := &g.Edges[st.EdgeID]
		nPts := len(e.Points)
		if nPts < 2 {
			continue
		}
		if st.Forward {
			base := e.Points[0][2]
			startIdx := 0
			if len(fullPts) > 0 {
				startIdx = 1
			}
			for i := startIdx; i < nPts; i++ {
				pt := e.Points[i]
				fullPts = append(fullPts, [3]float64{pt[0], pt[1], curEle + (pt[2] - base)})
			}
			curEle += e.NetDeltaUV
		} else {
			base := e.Points[nPts-1][2]
			startIdx := nPts - 1
			if len(fullPts) > 0 {
				startIdx = nPts - 2
			}
			for i := startIdx; i >= 0; i-- {
				pt := e.Points[i]
				fullPts = append(fullPts, [3]float64{pt[0], pt[1], curEle + (pt[2] - base)})
			}
			curEle -= e.NetDeltaUV
		}
	}

	// Distribute any residual loop-closure elevation drift linearly so chart & GPX close cleanly at start altitude
	if n := len(fullPts); n >= 2 {
		drift := fullPts[n-1][2] - fullPts[0][2]
		if math.Abs(drift) > 0.01 {
			denom := float64(n - 1)
			for i := 1; i < n; i++ {
				fullPts[i][2] -= drift * (float64(i) / denom)
			}
		}
	}

	cumDist := make([]float64, len(fullPts))
	rawEle := make([]float64, len(fullPts))
	for i, pt := range fullPts {
		rawEle[i] = pt[2]
		if i > 0 {
			d := haversineM(fullPts[i-1][0], fullPts[i-1][1], pt[0], pt[1])
			cumDist[i] = cumDist[i-1] + d
		}
	}

	maxMapPts := 600
	coords := make([][2]float64, 0, minInt(len(fullPts), maxMapPts))
	if len(fullPts) <= maxMapPts {
		for _, pt := range fullPts {
			coords = append(coords, [2]float64{pt[0], pt[1]})
		}
	} else {
		for i := 0; i < maxMapPts; i++ {
			idx := (i * (len(fullPts) - 1)) / (maxMapPts - 1)
			coords = append(coords, [2]float64{fullPts[idx][0], fullPts[idx][1]})
		}
	}

	maxProfPts := 200
	profDist := make([]float64, 0, minInt(len(fullPts), maxProfPts))
	profEle := make([]float64, 0, minInt(len(fullPts), maxProfPts))
	if len(fullPts) <= maxProfPts {
		for i := range fullPts {
			profDist = append(profDist, math.Round((cumDist[i]/1000.0)*100)/100)
			profEle = append(profEle, math.Round(rawEle[i]*10)/10)
		}
	} else {
		for i := 0; i < maxProfPts; i++ {
			idx := (i * (len(fullPts) - 1)) / (maxProfPts - 1)
			profDist = append(profDist, math.Round((cumDist[idx]/1000.0)*100)/100)
			profEle = append(profEle, math.Round(rawEle[idx]*10)/10)
		}
	}

	distKm := c.length / 1000.0
	vertPerKm := 0.0
	if distKm > 0 {
		vertPerKm = c.gain / distKm
	}
	overlapPct := 0.0
	if c.length > 0 {
		overlapPct = (c.overlap / c.length) * 100.0
	}

	name := fmt.Sprintf("Option #%d — %.1f km / +%.0fm", rank, distKm, c.gain)
	if c.repeatInfo != nil {
		name = fmt.Sprintf("Option #%d — %.1f km / +%.0fm (%dx loop)", rank, distKm, c.gain, c.repeatInfo.Laps)
	}

	return RouteResult{
		Rank:             rank,
		Name:             name,
		DistanceM:        math.Round(c.length),
		DistanceKm:       math.Round(distKm*100) / 100,
		ElevationGainM:   math.Round(c.gain),
		ElevationLossM:   math.Round(c.loss),
		VertPerKm:        math.Round(vertPerKm*10) / 10,
		OverlapM:         math.Round(c.overlap),
		OverlapPct:       math.Round(overlapPct*10) / 10,
		DistinctLabel:    distinctLabel,
		MaxSimilarityPct: maxSimPct,
		RepeatInfo:       c.repeatInfo,
		Coordinates:      coords,
		ProfileDistKm:    profDist,
		ProfileEleM:      profEle,
		FullPoints:       fullPts,
		edgeSet:          c.edgeSet,
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- GPX 1.1 Generation ---

type gpxRoot struct {
	XMLName xml.Name `xml:"gpx"`
	Version string   `xml:"version,attr"`
	Creator string   `xml:"creator,attr"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    gpxMeta  `xml:"metadata"`
	Trk     gpxTrk   `xml:"trk"`
}

type gpxMeta struct {
	Name string `xml:"name"`
	Time string `xml:"time"`
}

type gpxTrk struct {
	Name string    `xml:"name"`
	Seg  gpxTrkSeg `xml:"trkseg"`
}

type gpxTrkSeg struct {
	Pts []gpxTrkPt `xml:"trkpt"`
}

type gpxTrkPt struct {
	Lat float64 `xml:"lat,attr"`
	Lon float64 `xml:"lon,attr"`
	Ele float64 `xml:"ele"`
}

// GenerateRouteGPX serializes a RouteResult into a standard GPX 1.1 XML document.
func GenerateRouteGPX(route *RouteResult) ([]byte, error) {
	if route == nil || len(route.FullPoints) == 0 {
		return nil, fmt.Errorf("route has no track points")
	}
	pts := make([]gpxTrkPt, len(route.FullPoints))
	for i, p := range route.FullPoints {
		pts[i] = gpxTrkPt{
			Lat: math.Round(p[0]*1e6) / 1e6,
			Lon: math.Round(p[1]*1e6) / 1e6,
			Ele: math.Round(p[2]*10) / 10,
		}
	}
	doc := gpxRoot{
		Version: "1.1",
		Creator: "talus route builder",
		Xmlns:   "http://www.topografix.com/GPX/1/1",
		Meta: gpxMeta{
			Name: route.Name,
			Time: time.Now().UTC().Format(time.RFC3339),
		},
		Trk: gpxTrk{
			Name: route.Name,
			Seg:  gpxTrkSeg{Pts: pts},
		},
	}
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}

// --- Async Route Builder Job Manager (for Web UI) ---

// RouteJobStatus represents the current state of the on-demand route builder job.
type RouteJobStatus struct {
	State      string             `json:"state"` // "idle", "running", "done", "error", "cancelled"
	Progress   string             `json:"progress"`
	StartedAt  time.Time          `json:"started_at,omitempty"`
	ElapsedSec float64            `json:"elapsed_sec"`
	Error      string             `json:"error,omitempty"`
	Output     *RouteSearchOutput `json:"output,omitempty"`
}

// RouteJobManager runs at most one route-building job at a time with cancellation support.
type RouteJobManager struct {
	mu        sync.RWMutex
	state     string
	progress  string
	startedAt time.Time
	finished  time.Time
	errMsg    string
	output    *RouteSearchOutput
	cancelFn  context.CancelFunc
}

var (
	globalRouteMgr     *RouteJobManager
	globalRouteMgrOnce sync.Once
)

// GetRouteJobManager returns the singleton RouteJobManager.
func GetRouteJobManager() *RouteJobManager {
	globalRouteMgrOnce.Do(func() {
		globalRouteMgr = &RouteJobManager{state: "idle"}
	})
	return globalRouteMgr
}

// Start launches a new route-building job if none is currently running.
func (m *RouteJobManager) Start(dataDir string, req RouteBuildRequest) error {
	if err := req.Normalize(); err != nil {
		return err
	}

	m.mu.Lock()
	if m.state == "running" {
		m.mu.Unlock()
		return fmt.Errorf("a route builder job is already running; cancel it or wait for it to finish")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.state = "running"
	m.progress = "Starting route builder..."
	m.startedAt = time.Now()
	m.finished = time.Time{}
	m.errMsg = ""
	m.output = nil
	m.cancelFn = cancel
	m.mu.Unlock()

	updateProgress := func(msg string) {
		m.mu.Lock()
		if m.state == "running" {
			m.progress = msg
		}
		m.mu.Unlock()
	}

	go func() {
		defer cancel()
		graph, err := BuildTrailGraph(ctx, dataDir, req, updateProgress)
		if err != nil {
			m.finishError(ctx, err)
			return
		}
		out, err := FindTopRoutes(ctx, graph, req, updateProgress)
		if err != nil {
			m.finishError(ctx, err)
			return
		}
		m.mu.Lock()
		m.state = "done"
		m.progress = fmt.Sprintf("Generated %d routes (%d candidates evaluated)", len(out.Routes), out.CandidatesFound)
		m.finished = time.Now()
		m.output = out
		m.cancelFn = nil
		m.mu.Unlock()
	}()

	return nil
}

func (m *RouteJobManager) finishError(ctx context.Context, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finished = time.Now()
	m.cancelFn = nil
	if ctx.Err() == context.Canceled {
		m.state = "cancelled"
		m.progress = "Cancelled by user"
		m.errMsg = ""
		return
	}
	m.state = "error"
	m.errMsg = err.Error()
	m.progress = "Failed"
}

// Cancel stops a currently running route-building job.
func (m *RouteJobManager) Cancel() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == "running" && m.cancelFn != nil {
		m.cancelFn()
		return true
	}
	return false
}

// Status returns a snapshot of the current job status.
func (m *RouteJobManager) Status() RouteJobStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	elapsed := 0.0
	if !m.startedAt.IsZero() {
		if m.state == "running" {
			elapsed = time.Since(m.startedAt).Seconds()
		} else if !m.finished.IsZero() {
			elapsed = m.finished.Sub(m.startedAt).Seconds()
		}
	}

	return RouteJobStatus{
		State:      m.state,
		Progress:   m.progress,
		StartedAt:  m.startedAt,
		ElapsedSec: math.Round(elapsed*10) / 10,
		Error:      m.errMsg,
		Output:     m.output,
	}
}

// GetRouteByRank retrieves a route by 1-based rank from the last completed job for GPX download.
func (m *RouteJobManager) GetRouteByRank(rank int) (*RouteResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.output == nil || len(m.output.Routes) == 0 {
		return nil, fmt.Errorf("no generated routes available")
	}
	for i := range m.output.Routes {
		if m.output.Routes[i].Rank == rank {
			r := m.output.Routes[i]
			return &r, nil
		}
	}
	return nil, fmt.Errorf("route rank %d not found", rank)
}
