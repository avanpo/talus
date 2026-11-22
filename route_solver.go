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
	DistinctLabel    string          `json:"distinct_label"` // e.g. "Primary", "Distinct", "Variant"
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
	// Simple O(V^2) or min-heap; since V is typically 50..1000, a binary heap is clean & fast.
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
	// reverse in place
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

// halfPath is a path starting from an anchor node used in meet-in-the-middle loop assembly.
type halfPath struct {
	steps       []RouteStep
	endNode     int
	length      float64
	gainForward float64
	gainReverse float64
	overlap     float64
	edgeCounts  map[int]int
}

// evalLoopCandidate evaluates a closed sequence of steps for exact length, elevation gain/loss, and overlap.
func evalLoopCandidate(g *TrailGraph, steps []RouteStep, repeatInfo *RepeatLoopInfo, exemptOverlapEdges map[int]int) *candidateRoute {
	if len(steps) == 0 {
		return nil
	}
	length := 0.0
	gain := 0.0
	loss := 0.0
	counts := make(map[int]int, len(steps))
	edgeSet := make(map[int]float64, len(steps))

	for _, st := range steps {
		e := &g.Edges[st.EdgeID]
		length += e.Length
		if st.Forward {
			gain += e.GainUV
			loss += e.GainVU
		} else {
			gain += e.GainVU
			loss += e.GainUV
		}
		counts[st.EdgeID]++
		edgeSet[st.EdgeID] = e.Length
	}

	overlap := 0.0
	for eid, cnt := range counts {
		allowed := 1
		if exemptOverlapEdges != nil {
			if ex, ok := exemptOverlapEdges[eid]; ok && ex > allowed {
				allowed = ex
			}
		}
		if cnt > allowed {
			overlap += float64(cnt-allowed) * g.Edges[eid].Length
		}
	}

	return &candidateRoute{
		steps:      steps,
		length:     length,
		gain:       gain,
		loss:       loss,
		overlap:    overlap,
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

	// Bound search time
	timeout := time.Duration(req.TimeoutSec) * time.Second
	searchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	workers := req.MaxCPUs
	if workers < 1 {
		workers = defaultRouteWorkers()
	}

	if progressFn != nil {
		progressFn(fmt.Sprintf("Searching for top %d routes (%.1f ± %.1f km, max %.0f%% overlap)...",
			req.MaxResults, req.TargetDistanceKm, req.ToleranceKm, req.MaxOverlapPct))
	}

	var mu sync.Mutex
	var allCandidates []*candidateRoute
	edgeHeat := make([]float64, len(g.Edges)) // tracks heavily-used edges to diversify later passes

	addCandidate := func(c *candidateRoute) {
		if c == nil {
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
		// Gently increase heat on edges of high-scoring routes to encourage diversity
		if len(allCandidates) <= 2000 {
			for eid := range c.edgeSet {
				edgeHeat[eid] += 0.05
			}
		}
		mu.Unlock()
	}

	// Strategy 1 & 2: Parallel Meet-in-the-Middle + Guided Randomized Walks
	var wg sync.WaitGroup
	// Reserve ~25% of timeout for repeating loop composition if enabled
	mainBudget := timeout
	if req.AllowRepeatLoops {
		mainBudget = (timeout * 3) / 4
	}
	mainCtx, mainCancel := context.WithTimeout(searchCtx, mainBudget)
	defer mainCancel()

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)*7919))

			// Phase A: Meet-in-the-Middle rounds with varying exploration/diversity penalties
			round := 0
			for mainCtx.Err() == nil {
				round++
				// Snapshot edge penalties for diversity on rounds >= 2
				var penalties []float64
				if round >= 2 {
					penalties = make([]float64, len(g.Edges))
					mu.Lock()
					copy(penalties, edgeHeat)
					mu.Unlock()
				}

				// Run a batch of meet-in-the-middle half-paths
				runMeetInTheMiddleBatch(mainCtx, g, g.StartNode, distToStart, minDistM, maxDistM, maxOverlapFrac, penalties, rng, 180, addCandidate)

				// Run a batch of guided full-loop walks
				runGuidedWalkBatch(mainCtx, g, g.StartNode, distToStart, minDistM, maxDistM, maxOverlapFrac, penalties, rng, 200, addCandidate)

				// Cap total rounds if graph is small and we've already explored thoroughly
				if round >= 25 && len(g.Nodes) < 80 {
					break
				}
			}
		}(w)
	}
	wg.Wait()

	// Strategy 3: Repeating high-elevation loops (when enabled)
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

	// Select Top N routes ensuring at least 3 are mostly distinct
	selected := selectDiverseTopRoutes(allCandidates, req.MaxResults)

	// Convert selected candidates into full RouteResult structs with coordinates & elevation profiles
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
		NetworkOverlay:  g.ExportNetworkOverlay(600),
	}

	if progressFn != nil {
		progressFn(fmt.Sprintf("Done! Found %d routes (from %d candidates in %.1fs)",
			len(results), len(allCandidates), float64(out.ElapsedMs)/1000.0))
	}

	return out, nil
}

// runMeetInTheMiddleBatch generates outward paths from anchor and pairs paths meeting at the same junction.
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
	maxOverlapDist := maxOverlapFrac * maxDistM

	// Map endpoint node -> slice of best/diverse halfPaths reaching that node
	byEnd := make(map[int][]*halfPath)
	const maxPerNode = 10

	for w := 0; w < numWalks; w++ {
		if ctx.Err() != nil {
			return
		}
		// Vary temperature across walks: some greedy for vert, some exploratory
		temp := 0.25 + rng.Float64()*2.0
		divWeight := 0.0
		if edgePenalty != nil && w%3 != 0 {
			divWeight = 0.3 + rng.Float64()*1.5
		}

		u := anchor
		curDist := 0.0
		curGainFwd := 0.0
		curGainRev := 0.0
		curOverlap := 0.0
		lastEdge := -1
		edgeCounts := make(map[int]int, 32)
		var steps []RouteStep

		maxSteps := 120
		for step := 0; step < maxSteps; step++ {
			edges := g.Nodes[u].Edges
			if len(edges) == 0 {
				break
			}

			// Score valid next edges
			type choice struct {
				eid   int
				v     int
				score float64
				ovAdd float64
			}
			var choices []choice
			for _, eid := range edges {
				// Avoid immediate U-turn unless dead-end
				if eid == lastEdge && len(edges) > 1 {
					continue
				}
				e := &g.Edges[eid]
				v := e.Other(u)
				newDist := curDist + e.Length
				if newDist > halfMax {
					continue
				}
				if newDist+distToAnchor[v] > maxDistM {
					continue
				}
				ovAdd := 0.0
				if edgeCounts[eid] >= 1 {
					ovAdd = e.Length
				}
				if curOverlap+ovAdd > maxOverlapDist {
					continue
				}

				// Elevation gain score (consider both directions since half-path may be used forward or reverse)
				fwdGain := e.GainFrom(u)
				revGain := e.GainFrom(v)
				gainRate := (fwdGain + 0.5*revGain) / math.Max(e.Length, 20.0)
				score := 1.0 + gainRate*35.0

				// Favor fresh edges strongly
				if ovAdd > 0 {
					score *= 0.15
				}
				// Favor moving outward early
				if distToAnchor[v] > distToAnchor[u] {
					score *= 1.25
				}
				// Apply diversity penalty from previous rounds
				if divWeight > 0 && edgePenalty[eid] > 0 {
					score /= (1.0 + divWeight*edgePenalty[eid])
				}

				choices = append(choices, choice{eid: eid, v: v, score: score, ovAdd: ovAdd})
			}

			if len(choices) == 0 {
				break
			}

			// Softmax / power-weighted random choice
			picked := pickWeightedChoice(choices[0].score, len(choices), func(i int) float64 {
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
			curOverlap += ch.ovAdd
			edgeCounts[ch.eid]++
			lastEdge = ch.eid
			u = ch.v

			// If we happened to loop right back to anchor within target distance, emit directly!
			if u == anchor && curDist >= minDistM && curDist <= maxDistM {
				stCopy := make([]RouteStep, len(steps))
				copy(stCopy, steps)
				emit(evalLoopCandidate(g, stCopy, nil, nil))
				break
			}

			// Record half-path snapshot at valid junctions
			if u != anchor && curDist >= halfMin && curDist <= halfMax {
				list := byEnd[u]
				if len(list) < maxPerNode {
					stCopy := make([]RouteStep, len(steps))
					copy(stCopy, steps)
					ecCopy := make(map[int]int, len(edgeCounts))
					for k, v := range edgeCounts {
						ecCopy[k] = v
					}
					byEnd[u] = append(list, &halfPath{
						steps:       stCopy,
						endNode:     u,
						length:      curDist,
						gainForward: curGainFwd,
						gainReverse: curGainRev,
						overlap:     curOverlap,
						edgeCounts:  ecCopy,
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
				// Quick overlap check between p1 and p2
				ov := p1.overlap + p2.overlap
				if ov > maxOverlapFrac*totalLen {
					continue
				}
				for eid, c1 := range p1.edgeCounts {
					if c2, ok := p2.edgeCounts[eid]; ok {
						// Combined count is c1 + c2; overlap already counted (c1-1) + (c2-1),
						// so merging adds 1 more overlapping traversal of eid!
						_ = c1
						_ = c2
						ov += g.Edges[eid].Length
						if ov > maxOverlapFrac*totalLen+1e-6 {
							break
						}
					}
				}
				if ov > maxOverlapFrac*totalLen+1e-6 {
					continue
				}

				// Choose the direction (p1 + rev(p2) vs p2 + rev(p1)) with higher elevation gain
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

// runGuidedWalkBatch performs full-loop walks that climb outward and then smoothly steer back home.
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

	for w := 0; w < numWalks; w++ {
		if ctx.Err() != nil {
			return
		}
		temp := 0.3 + rng.Float64()*1.5
		divWeight := 0.0
		if edgePenalty != nil && w%2 == 0 {
			divWeight = 0.4 + rng.Float64()*1.2
		}

		u := anchor
		curDist := 0.0
		curOverlap := 0.0
		lastEdge := -1
		edgeCounts := make(map[int]int, 48)
		var steps []RouteStep

		for step := 0; step < 220; step++ {
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
				if eid == lastEdge && len(edges) > 1 {
					continue
				}
				e := &g.Edges[eid]
				v := e.Other(u)
				newDist := curDist + e.Length
				if newDist+distToAnchor[v] > maxDistM {
					continue
				}
				ovAdd := 0.0
				if edgeCounts[eid] >= 1 {
					ovAdd = e.Length
				}
				if curOverlap+ovAdd > maxOverlapDist {
					continue
				}

				gainRate := e.GainFrom(u) / math.Max(e.Length, 20.0)
				score := 1.0 + gainRate*40.0

				if ovAdd > 0 {
					score *= 0.1
				}

				// Phase guidance: explore outward early, steer home past 60% of target distance
				if progress < 0.55 {
					if distToAnchor[v] > distToAnchor[u] {
						score *= 1.3
					}
					if v == anchor && newDist < minDistM {
						score *= 0.05 // don't return home too early
					}
				} else {
					if v == anchor && newDist >= minDistM && newDist <= maxDistM {
						score *= 12.0 // strongly prefer closing a valid loop!
					} else if distToAnchor[v] < distToAnchor[u] {
						score *= 2.0 + (progress-0.55)*4.0
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

			picked := pickWeightedChoice(choices[0].score, len(choices), func(i int) float64 {
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
			lastEdge = ch.eid
			u = ch.v

			if u == anchor && curDist >= minDistM && curDist <= maxDistM {
				emit(evalLoopCandidate(g, steps, nil, nil))
				break
			}
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

	// Select anchor junctions: always include StartNode, plus top high-degree / high-vert junctions reachable from StartNode
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
		// Score by local elevation gain of incident edges
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

	// Precompute shortest paths from StartNode for stems
	_, prevNodeFromStart, prevEdgeFromStart := dijkstraDistances(g, g.StartNode)

	for _, anchor := range anchorList {
		if ctx.Err() != nil {
			return
		}
		stemDist := distToStart[anchor]
		// Max single-lap length for at least 2 laps: (maxDistM - 2*stemDist) / 2
		maxSingleLap := (maxDistM - 2*stemDist) / 2.0
		if maxSingleLap < 1000.0 {
			continue
		}
		minSingleLap := math.Max(1000.0, maxSingleLap*0.18)

		distToAnchor := distToStart
		if anchor != g.StartNode {
			distToAnchor, _, _ = dijkstraDistances(g, anchor)
		}

		// Harvest qualifying high-vert loops anchored at `anchor`
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

		// Keep top 8 highest-gain-per-km distinct loops at this anchor
		sort.Slice(qualLoops, func(i, j int) bool {
			return (qualLoops[i].gain / qualLoops[i].length) > (qualLoops[j].gain / qualLoops[j].length)
		})
		if len(qualLoops) > 8 {
			qualLoops = qualLoops[:8]
		}

		// Build stem paths if anchor != StartNode
		var stemOut, stemBack []RouteStep
		if anchor != g.StartNode {
			var ok bool
			stemOut, ok = reconstructShortestPath(g, g.StartNode, anchor, prevNodeFromStart, prevEdgeFromStart)
			if !ok {
				continue
			}
			// Try to find a disjoint return path to minimize stem overlap
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
			// Try k = 2, 3, 4, ... laps
			for k := 2; k <= 8; k++ {
				totalLen := stemLen + float64(k)*lp.length
				if totalLen > maxDistM {
					break
				}
				if totalLen >= minDistM && totalLen <= maxDistM {
					// Compose stemOut + k*lp + stemBack
					steps := make([]RouteStep, 0, len(stemOut)+k*len(lp.steps)+len(stemBack))
					steps = append(steps, stemOut...)
					for lap := 0; lap < k; lap++ {
						steps = append(steps, lp.steps...)
					}
					steps = append(steps, stemBack...)

					// Edges in the qualifying high-vert loop are allowed up to k traversals
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

func pickWeightedChoice(_ float64, n int, weightFn func(int) float64, rng *rand.Rand) int {
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

	// 1. Always pick the #1 highest-elevation route
	picked = append(picked, selectedRoute{
		cand:      candidates[0],
		label:     "Top Vert",
		maxSimPct: 0,
	})
	usedIdx[0] = true

	// 2. Pick at least 2 more mostly-distinct routes (so >= 3 mostly distinct overall)
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
					maxSimPct: math.Round(sim * 1000) / 10,
				})
				usedIdx[i] = true
			}
		}
		if len(picked) >= distinctTarget {
			break
		}
	}

	// 3. Fill remaining slots up to maxResults with best remaining non-clone routes (< 88% similar)
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
					maxSimPct: math.Round(sim * 1000) / 10,
				})
				usedIdx[i] = true
			}
		}
		if len(picked) >= maxResults {
			break
		}
	}

	// Re-sort final results by elevation gain descending while preserving badges
	sort.Slice(picked, func(i, j int) bool {
		return picked[i].cand.gain > picked[j].cand.gain
	})
	if len(picked) > 0 {
		picked[0].label = "Top Vert"
	}

	return picked
}

// buildRouteResult expands a candidateRoute's edge steps into full coordinates, elevation profile, and summary metrics.
func buildRouteResult(g *TrailGraph, c *candidateRoute, rank int, distinctLabel string, maxSimPct float64) RouteResult {
	var fullPts [][3]float64
	for _, st := range c.steps {
		e := &g.Edges[st.EdgeID]
		nPts := len(e.Points)
		if st.Forward {
			startIdx := 0
			if len(fullPts) > 0 {
				startIdx = 1 // avoid duplicating junction vertex
			}
			for i := startIdx; i < nPts; i++ {
				fullPts = append(fullPts, e.Points[i])
			}
		} else {
			startIdx := nPts - 1
			if len(fullPts) > 0 {
				startIdx = nPts - 2
			}
			for i := startIdx; i >= 0; i-- {
				fullPts = append(fullPts, e.Points[i])
			}
		}
	}

	// Build cumulative distance and elevation profile
	cumDist := make([]float64, len(fullPts))
	rawEle := make([]float64, len(fullPts))
	for i, pt := range fullPts {
		rawEle[i] = pt[2]
		if i > 0 {
			d := haversineM(fullPts[i-1][0], fullPts[i-1][1], pt[0], pt[1])
			cumDist[i] = cumDist[i-1] + d
		}
	}

	// Downsample coordinates for crisp map rendering (up to 600 pts)
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

	// Downsample elevation profile for Chart.js (up to 200 pts)
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
