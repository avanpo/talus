package main

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// MapInfo holds map data for an activity.
type MapInfo struct {
	Polyline string `json:"polyline"`
}

// StreamInfo holds data stream series.
type StreamInfo struct {
	Data []interface{} `json:"data"`
}

// SummarySegment represents a Strava segment summary.
type SummarySegment struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	Distance      float64 `json:"distance"`
	AverageGrade  float64 `json:"average_grade"`
	MaximumGrade  float64 `json:"maximum_grade"`
	ElevationHigh float64 `json:"elevation_high"`
	ElevationLow  float64 `json:"elevation_low"`
	ClimbCategory int     `json:"climb_category"`
	City          string  `json:"city"`
	State         string  `json:"state"`
}

// SegmentEffort represents an effort on a Strava segment.
type SegmentEffort struct {
	ID               int64          `json:"id"`
	Name             string         `json:"name"`
	Segment          SummarySegment `json:"segment"`
	ElapsedTime      int            `json:"elapsed_time"`
	MovingTime       int            `json:"moving_time"`
	StartDate        time.Time      `json:"start_date"`
	Distance         float64        `json:"distance"`
	AverageHeartrate float64        `json:"average_heartrate"`
	MaxHeartrate     float64        `json:"max_heartrate"`
	PRRank           *int           `json:"pr_rank"`
	StartIndex       int            `json:"start_index"`
	EndIndex         int            `json:"end_index"`
}

// ActivityDetail represents the loaded JSON format for activities.
type ActivityDetail struct {
	ID                 int64                 `json:"id"`
	Name               string                `json:"name"`
	Type               string                `json:"type"`
	SportType          string                `json:"sport_type"`
	StartDate          time.Time             `json:"start_date"`
	Distance           float64               `json:"distance"` // in meters
	MovingTime         int                   `json:"moving_time"` // in seconds
	ElapsedTime        int                   `json:"elapsed_time"`
	TotalElevationGain float64               `json:"total_elevation_gain"` // in meters
	AverageHeartrate   float64               `json:"average_heartrate"`
	MaxHeartrate       float64               `json:"max_heartrate"`
	AverageCadence     float64               `json:"average_cadence"`
	AverageSpeed       float64               `json:"average_speed"`
	GearID             string                `json:"gear_id"`
	Map                MapInfo               `json:"map"`
	Streams            map[string]StreamInfo `json:"streams"`
	SegmentEfforts     []SegmentEffort       `json:"segment_efforts"`
}

// IsRunActivity returns true if the activity is a Run or TrailRun.
func IsRunActivity(act *ActivityDetail) bool {
	return act.Type == "Run" || act.SportType == "Run" || act.SportType == "TrailRun"
}

// IsTrailRun returns true if the activity is classified as a trail run.
func IsTrailRun(act *ActivityDetail) bool {
	if act.SportType == "TrailRun" {
		return true
	}
	if act.Type == "Run" && act.Distance > 0 {
		vertRatio := (act.TotalElevationGain / act.Distance) * 1000.0 // meters per km
		if vertRatio >= 25.0 {
			return true
		}
	}
	return false
}

// GetSportCategory returns "running", "cycling", "skiing", or "" for an activity.
func GetSportCategory(act *ActivityDetail) string {
	if IsRunActivity(act) {
		return "running"
	}
	if IsRideActivity(act) {
		return "cycling"
	}
	if IsSkiTour(act) {
		return "skiing"
	}
	return ""
}

// RouteCluster represents a group of runs on a similar route.
type RouteCluster struct {
	ID        int
	Sport     string `json:"sport"`
	Distance  float64
	RouteName string
	Runs      []*RunStats
}

// RunStats contains calculated metrics for a single run or cardio activity.
type RunStats struct {
	Activity            *ActivityDetail
	Sport               string  `json:"sport"` // "running", "cycling", "skiing"
	AvgPaceMinKm        float64
	EF                  float64 // Efficiency Factor (Speed in m/min / Avg HR)
	DecouplingPct       float64 // Aerobic Decoupling percentage
	HasDecoupling       bool
	DownsampledPath     [][2]float64
	Prior7DaysDistance  float64 // meters
	Prior7DaysTime      int     // seconds
	Prior7DaysElev      float64 // meters
	Prior7DaysCount     int
	PreRunFatigueFactor float64
	AdjustedEF          float64 // EF * PreRunFatigueFactor
	AdjustedPaceMinKm   float64 // AvgPaceMinKm / PreRunFatigueFactor
}

// SegmentEffortStats represents calculated metrics for a single segment effort, including fatigue adjustments.
type SegmentEffortStats struct {
	ID                  int64     `json:"id"`
	ActivityID          int64     `json:"activity_id"`
	ActivityName        string    `json:"activity_name"`
	Sport               string    `json:"sport"` // "running", "cycling", "skiing"
	SegmentID           int64     `json:"segment_id"`
	SegmentName         string    `json:"segment_name"`
	StartDate           time.Time `json:"start_date"`
	Distance            float64   `json:"distance"`     // meters
	MovingTime          int       `json:"moving_time"`  // seconds
	ElapsedTime         int       `json:"elapsed_time"` // seconds
	AvgPaceMinKm        float64   `json:"avg_pace_min_km"`
	AvgHeartrate        float64   `json:"avg_heartrate"`
	MaxHeartrate        float64   `json:"max_heartrate"`
	PRRank              *int      `json:"pr_rank"`
	EF                  float64   `json:"ef"`

	// In-run fatigue
	PreSegmentDistance  float64 `json:"pre_segment_distance"` // meters
	PreSegmentTime      int     `json:"pre_segment_time"`     // seconds
	InRunFatigueFactor  float64 `json:"in_run_fatigue_factor"`

	// Pre-run fatigue (prior 7 days)
	Prior7DaysDistance  float64 `json:"prior_7days_distance"` // meters
	Prior7DaysTime      int     `json:"prior_7days_time"`     // seconds
	Prior7DaysElev      float64 `json:"prior_7days_elevation"`// meters
	Prior7DaysCount     int     `json:"prior_7days_count"`
	PreRunFatigueFactor float64 `json:"pre_run_fatigue_factor"`

	// Total combined fatigue
	TotalFatigueFactor  float64 `json:"total_fatigue_factor"`
	FatigueBonusPct     float64 `json:"fatigue_bonus_pct"`

	// Fatigue-adjusted metrics
	AdjustedEF          float64 `json:"adjusted_ef"`
	AdjustedPaceMinKm   float64 `json:"adjusted_pace_min_km"`
}

// SegmentSummaryStats aggregates all efforts for a given segment.
type SegmentSummaryStats struct {
	SegmentID        int64                 `json:"segment_id"`
	SegmentName      string                `json:"segment_name"`
	Sport            string                `json:"sport"` // "running", "cycling", "skiing"
	Distance         float64               `json:"distance"` // meters
	AverageGrade     float64               `json:"average_grade"`
	MaximumGrade     float64               `json:"maximum_grade"`
	ElevationHigh    float64               `json:"elevation_high"`
	ElevationLow     float64               `json:"elevation_low"`
	ClimbCategory    int                   `json:"climb_category"`
	City             string                `json:"city"`
	State            string                `json:"state"`
	EffortCount      int                   `json:"effort_count"`
	BestPaceMinKm    float64               `json:"best_pace_min_km"`
	BestAdjustedPace float64               `json:"best_adjusted_pace_min_km"`
	BestEF           float64               `json:"best_ef"`
	BestAdjustedEF   float64               `json:"best_adjusted_ef"`
	Efforts          []*SegmentEffortStats `json:"efforts"`
}

// GetProgressAnalysis loads activity data (using cache), clusters them, and calculates fitness metrics.
func GetProgressAnalysis(dir string) ([]*RunStats, []*SegmentSummaryStats, []*RouteCluster, error) {
	return GetGlobalStore(dir).GetProgress()
}

// AnalyzeActivities loads all activity JSON files, clusters them, calculates fitness metrics, and displays a report.
func AnalyzeActivities(dir string) error {
	runs, segments, clusters, err := GetProgressAnalysis(dir)
	if err != nil {
		return err
	}

	if len(runs) == 0 {
		fmt.Println("No run activities with valid heart rate and GPS data found in the backup directory.")
		return nil
	}

	// Display Segment & Route Report
	printReport(segments, clusters)

	return nil
}

func calculateRunStats(act *ActivityDetail) *RunStats {
	stats := &RunStats{Activity: act}

	// Pace calculation
	speedMMin := act.Distance / (float64(act.MovingTime) / 60.0)
	stats.AvgPaceMinKm = 1000.0 / speedMMin

	// Efficiency Factor (EF): For running, normalize terrain with Grade-Adjusted Pace (Minetti cost: +7.5m flat per 1m climb)
	effectiveDist := act.Distance
	if IsRunActivity(act) && act.TotalElevationGain > 0 {
		effectiveDist += (7.5 * act.TotalElevationGain)
	}
	effSpeedMMin := effectiveDist / (float64(act.MovingTime) / 60.0)
	stats.EF = effSpeedMMin / act.AverageHeartrate

	// Decode and downsample polyline
	if act.Map.Polyline != "" {
		if coords, err := decodePolyline(act.Map.Polyline); err == nil {
			stats.DownsampledPath = downsamplePath(coords, 15)
		}
	}

	// Calculate Decoupling from Streams (steady-state 1st half vs 2nd half)
	if act.Streams != nil {
		hrStream, okHR := act.Streams["heartrate"]
		timeStream, okTime := act.Streams["time"]
		distStream, okDist := act.Streams["distance"]

		if okHR && okTime && okDist && len(hrStream.Data) > 1 {
			hr := getFloatSlice(hrStream.Data)
			t := getFloatSlice(timeStream.Data)
			d := getFloatSlice(distStream.Data)

			if len(hr) == len(t) && len(hr) == len(d) {
				totalDistance := d[len(d)-1]
				halfDistance := totalDistance / 2.0

				// Find midpoint index in stream based on distance
				midIdx := 0
				for i, distVal := range d {
					if distVal >= halfDistance {
						midIdx = i
						break
					}
				}

				if midIdx > 0 && midIdx < len(d)-1 {
					// First half stats
					d1 := d[midIdx] - d[0]
					t1 := t[midIdx] - t[0]
					hr1 := averageFloat(hr[:midIdx+1])

					// Second half stats
					d2 := d[len(d)-1] - d[midIdx]
					t2 := t[len(t)-1] - t[midIdx]
					hr2 := averageFloat(hr[midIdx:])

					// If altitude stream is available, adjust each half for vertical gain to prevent terrain-induced false drift
					if altStream, okAlt := act.Streams["altitude"]; okAlt && len(altStream.Data) == len(d) && IsRunActivity(act) {
						alt := getFloatSlice(altStream.Data)
						var elevGain1, elevGain2 float64
						for k := 1; k <= midIdx; k++ {
							if diff := alt[k] - alt[k-1]; diff > 0 {
								elevGain1 += diff
							}
						}
						for k := midIdx + 1; k < len(alt); k++ {
							if diff := alt[k] - alt[k-1]; diff > 0 {
								elevGain2 += diff
							}
						}
						d1 += 7.5 * elevGain1
						d2 += 7.5 * elevGain2
					}

					if t1 > 0 && t2 > 0 && hr1 > 0 && hr2 > 0 {
						speed1 := d1 / (t1 / 60.0)
						speed2 := d2 / (t2 / 60.0)
						ef1 := speed1 / hr1
						ef2 := speed2 / hr2

						if ef1 > 0 {
							drift := ((ef1 - ef2) / ef1) * 100.0
							// Only mark as valid decoupling if within plausible physiological bounds
							// (extreme values > 35% or < -20% reflect non-steady interval rests or anomalies)
							if drift >= -20.0 && drift <= 35.0 {
								stats.DecouplingPct = drift
								stats.HasDecoupling = true
							}
						}
					}
				}
			}
		}
	}

	return stats
}

func decodePolyline(encoded string) ([][2]float64, error) {
	var coords [][2]float64
	var index, lat, lng int

	for index < len(encoded) {
		var shift, result int
		for {
			if index >= len(encoded) {
				return nil, fmt.Errorf("invalid polyline encoding")
			}
			b := int(encoded[index]) - 63
			index++
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		var dlat int
		if (result & 1) != 0 {
			dlat = ^(result >> 1)
		} else {
			dlat = result >> 1
		}
		lat += dlat

		shift, result = 0, 0
		for {
			if index >= len(encoded) {
				return nil, fmt.Errorf("invalid polyline encoding")
			}
			b := int(encoded[index]) - 63
			index++
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		var dlng int
		if (result & 1) != 0 {
			dlng = ^(result >> 1)
		} else {
			dlng = result >> 1
		}
		lng += dlng

		coords = append(coords, [2]float64{
			float64(lat) / 1e5,
			float64(lng) / 1e5,
		})
	}
	return coords, nil
}

func downsamplePath(coords [][2]float64, numPoints int) [][2]float64 {
	if len(coords) == 0 {
		return nil
	}
	if len(coords) < numPoints {
		// Simple fallback if there are too few points
		res := make([][2]float64, numPoints)
		for i := 0; i < numPoints; i++ {
			idx := (i * len(coords)) / numPoints
			res[i] = coords[idx]
		}
		return res
	}

	cumDist := make([]float64, len(coords))
	var totalDist float64
	for i := 1; i < len(coords); i++ {
		dy := coords[i][0] - coords[i-1][0]
		dx := (coords[i][1] - coords[i-1][1]) * math.Cos(coords[i-1][0]*math.Pi/180.0)
		dist := math.Sqrt(dx*dx + dy*dy)
		totalDist += dist
		cumDist[i] = totalDist
	}

	sampled := make([][2]float64, numPoints)
	sampled[0] = coords[0]
	sampled[numPoints-1] = coords[len(coords)-1]

	for i := 1; i < numPoints-1; i++ {
		target := float64(i) * totalDist / float64(numPoints-1)
		idx := 1
		for idx < len(coords) && cumDist[idx] < target {
			idx++
		}
		if idx >= len(coords) {
			sampled[i] = coords[len(coords)-1]
			continue
		}

		dPrev := cumDist[idx-1]
		dNext := cumDist[idx]
		fraction := 0.0
		if dNext != dPrev {
			fraction = (target - dPrev) / (dNext - dPrev)
		}

		lat := coords[idx-1][0] + fraction*(coords[idx][0]-coords[idx-1][0])
		lng := coords[idx-1][1] + fraction*(coords[idx][1]-coords[idx-1][1])
		sampled[i] = [2]float64{lat, lng}
	}
	return sampled
}

func pathDistance(path1, path2 [][2]float64) float64 {
	if len(path1) != len(path2) || len(path1) == 0 {
		return 999999.0
	}
	var sum float64
	for i := 0; i < len(path1); i++ {
		dy := path1[i][0] - path2[i][0]
		dx := (path1[i][1] - path2[i][1]) * math.Cos(path1[i][0]*math.Pi/180.0)
		distMeters := math.Sqrt(dx*dx+dy*dy) * 111000.0
		sum += distMeters
	}
	return sum / float64(len(path1))
}

func pathDistanceBiDirectional(path1, path2 [][2]float64) float64 {
	n := len(path1)
	if n != len(path2) || n == 0 {
		return 999999.0
	}

	var sum1, sum2 float64
	for i := 0; i < n; i++ {
		// Forward comparison: path1[i] vs path2[i]
		dy1 := path1[i][0] - path2[i][0]
		dx1 := (path1[i][1] - path2[i][1]) * math.Cos(path1[i][0]*math.Pi/180.0)
		sum1 += math.Sqrt(dx1*dx1 + dy1*dy1)

		// Reverse comparison: path1[i] vs path2[n - 1 - i]
		revIdx := n - 1 - i
		dy2 := path1[i][0] - path2[revIdx][0]
		dx2 := (path1[i][1] - path2[revIdx][1]) * math.Cos(path1[i][0]*math.Pi/180.0)
		sum2 += math.Sqrt(dx2*dx2 + dy2*dy2)
	}

	d1 := (sum1 / float64(n)) * 111000.0
	d2 := (sum2 / float64(n)) * 111000.0

	if d1 < d2 {
		return d1
	}
	return d2
}

func clusterRuns(runs []*RunStats) []*RouteCluster {
	var clusters []*RouteCluster

	for _, run := range runs {
		matchedClusterIdx := -1
		minDist := 150.0 // 150 meters similarity threshold

		for i, cluster := range clusters {
			// 1. Distance check: must be within 15% distance difference
			distDiffPct := math.Abs(run.Activity.Distance-cluster.Distance) / cluster.Distance
			if distDiffPct > 0.15 {
				continue
			}

			// 2. Spatial proximity check on start/end coordinates before running full path comparison
			if len(run.DownsampledPath) > 0 && len(cluster.Runs[0].DownsampledPath) > 0 {
				p1 := run.DownsampledPath
				p2 := cluster.Runs[0].DownsampledPath
				n2 := len(p2)

				// Fast bounding check: start of p1 vs start of p2 (forward) or end of p2 (reverse)
				// 0.008 degrees is ~800m. If starts are far apart in both directions, routes cannot match.
				fwdLatDiff := math.Abs(p1[0][0] - p2[0][0])
				fwdLngDiff := math.Abs(p1[0][1] - p2[0][1])
				revLatDiff := math.Abs(p1[0][0] - p2[n2-1][0])
				revLngDiff := math.Abs(p1[0][1] - p2[n2-1][1])

				if (fwdLatDiff > 0.008 || fwdLngDiff > 0.008) && (revLatDiff > 0.008 || revLngDiff > 0.008) {
					continue
				}

				dist := pathDistanceBiDirectional(p1, p2)
				if dist < minDist {
					minDist = dist
					matchedClusterIdx = i
				}
			}
		}

		if matchedClusterIdx != -1 {
			clusters[matchedClusterIdx].Runs = append(clusters[matchedClusterIdx].Runs, run)
		} else {
			// Create a new cluster
			newCluster := &RouteCluster{
				ID:        len(clusters) + 1,
				Distance:  run.Activity.Distance,
				RouteName: fmt.Sprintf("Route %d (~%.1f km)", len(clusters)+1, run.Activity.Distance/1000.0),
				Runs:      []*RunStats{run},
			}
			clusters = append(clusters, newCluster)
		}
	}

	return clusters
}

func printReport(segments []*SegmentSummaryStats, clusters []*RouteCluster) {
	if len(segments) > 0 {
		fmt.Println("\n================================================================================")
		fmt.Println("                      REPEATED SEGMENT PROGRESSION ANALYSIS                     ")
		fmt.Println("================================================================================")

		for _, seg := range segments {
			if len(seg.Efforts) < 2 {
				continue
			}

			fmt.Printf("\nSegment: %s (~%.1f km, Grade: %.1f%%, Efforts: %d)\n",
				seg.SegmentName, seg.Distance/1000.0, seg.AverageGrade, len(seg.Efforts))
			fmt.Println("--------------------------------------------------------------------------------")
			fmt.Printf("%-10s  %-8s  %-10s  %-10s  %-10s  %-8s  %-12s  %-10s\n",
				"Date", "Time", "Raw Pace", "Adj Pace", "Avg HR", "Raw EF", "Adj EF", "Fatigue Load")

			for _, effort := range seg.Efforts {
				dateStr := effort.StartDate.Format("2006-01-02")
				timeStr := formatDuration(effort.MovingTime)
				rawPaceStr := formatPace(effort.AvgPaceMinKm)
				adjPaceStr := formatPace(effort.AdjustedPaceMinKm)
				hrStr := fmt.Sprintf("%.0f", effort.AvgHeartrate)
				rawEFStr := fmt.Sprintf("%.3f", effort.EF)
				adjEFStr := fmt.Sprintf("%.3f", effort.AdjustedEF)
				fatigueStr := fmt.Sprintf("+%.1f%%", effort.FatigueBonusPct)

				fmt.Printf("%-10s  %-8s  %-10s  %-10s  %-10s  %-8s  %-12s  %-10s\n",
					dateStr, timeStr, rawPaceStr, adjPaceStr, hrStr, rawEFStr, adjEFStr, fatigueStr)
			}
		}
	}

	// Sort clusters by number of runs descending so the most frequent routes appear first
	sort.Slice(clusters, func(i, j int) bool {
		return len(clusters[i].Runs) > len(clusters[j].Runs)
	})

	fmt.Println("\n================================================================================")
	fmt.Println("                       RUNNING FITNESS & ROUTE ANALYSIS                         ")
	fmt.Println("================================================================================")

	for _, cluster := range clusters {
		// Only display routes run more than once
		if len(cluster.Runs) < 2 {
			continue
		}

		fmt.Printf("\n%s (Total Runs: %d)\n", cluster.RouteName, len(cluster.Runs))
		fmt.Println("--------------------------------------------------------------------------------")
		fmt.Printf("%-10s  %-8s  %-8s  %-10s  %-10s  %-10s  %-10s\n",
			"Date", "Dist(km)", "Time", "Pace(/km)", "Avg HR(bpm)", "EF(m/beat)", "Decouple")

		for _, run := range cluster.Runs {
			dateStr := run.Activity.StartDate.Format("2006-01-02")
			distKm := run.Activity.Distance / 1000.0
			duration := formatDuration(run.Activity.MovingTime)
			paceStr := formatPace(run.AvgPaceMinKm)
			hrStr := fmt.Sprintf("%.0f", run.Activity.AverageHeartrate)
			efStr := fmt.Sprintf("%.3f", run.EF)

			decoupleStr := "N/A"
			if run.HasDecoupling {
				decoupleStr = fmt.Sprintf("%+.1f%%", run.DecouplingPct)
			}

			fmt.Printf("%-10s  %-8.2f  %-8s  %-10s  %-10s  %-10s  %-10s\n",
				dateStr, distKm, duration, paceStr, hrStr, efStr, decoupleStr)
		}

		// Trend analysis for this cluster
		firstRun := cluster.Runs[0]
		lastRun := cluster.Runs[len(cluster.Runs)-1]
		efDiff := lastRun.EF - firstRun.EF
		efImprovementPct := (efDiff / firstRun.EF) * 100.0

		fmt.Printf("Trend: Efficiency change: %+.1f%% ", efImprovementPct)
		if efImprovementPct > 3.0 {
			fmt.Println("[+] (Cardiovascular fitness is improving on this route)")
		} else if efImprovementPct < -3.0 {
			fmt.Println("[-] (Cardiovascular efficiency is down on this route)")
		} else {
			fmt.Println("[=] (Efficiency remains stable)")
		}
	}
	fmt.Println("================================================================================")
	fmt.Println()
}

func getFloatSlice(data []interface{}) []float64 {
	res := make([]float64, len(data))
	for i, v := range data {
		if f, ok := v.(float64); ok {
			res[i] = f
		}
	}
	return res
}

func averageFloat(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	var sum float64
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

func formatDuration(secs int) string {
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func formatPace(paceDecimal float64) string {
	min := int(paceDecimal)
	sec := int((paceDecimal - float64(min)) * 60)
	return fmt.Sprintf("%d:%02d", min, sec)
}
