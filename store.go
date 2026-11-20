package main

import (
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

// ActivityStore maintains an in-memory cache of parsed activities and fitness metrics.
type ActivityStore struct {
	dir string
	mu  sync.RWMutex

	// Track cached items by filename -> parsed ActivityDetail
	cacheMap map[string]*cachedFileItem

	// Aggregated in-memory state
	activities []*ActivityDetail
	years      []int
	runs       []*RunStats
	segments   []*SegmentSummaryStats
	clusters   []*RouteCluster

	lastCheckTime time.Time
}

type cachedFileItem struct {
	modTime         time.Time
	size            int64
	activity        *ActivityDetail
	runStats        *RunStats
	segmentEfforts  []SegmentEffort
	preSegmentDists map[int64]float64
	hrSummary       *ActivityHRSummary
}

var (
	globalStore   *ActivityStore
	globalStoreMu sync.Mutex
)

// GetGlobalStore returns or initializes the global ActivityStore for the directory.
func GetGlobalStore(dir string) *ActivityStore {
	globalStoreMu.Lock()
	defer globalStoreMu.Unlock()

	if globalStore == nil || globalStore.dir != dir {
		globalStore = &ActivityStore{
			dir:      dir,
			cacheMap: make(map[string]*cachedFileItem),
		}
	}
	return globalStore
}

// ReloadIfNeeded re-scans activities directory and updates the cache if files were modified/added.
func (s *ActivityStore) ReloadIfNeeded() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Rate limit directory stat scans to once every 2 seconds unless never loaded
	if len(s.activities) > 0 && time.Since(s.lastCheckTime) < 2*time.Second {
		return nil
	}
	s.lastCheckTime = time.Now()

	activitiesDir := filepath.Join(s.dir, "activities")
	entries, err := os.ReadDir(activitiesDir)
	if err != nil {
		if os.IsNotExist(err) {
			s.activities = nil
			s.years = nil
			s.runs = nil
			s.segments = nil
			s.clusters = nil
			return nil
		}
		return fmt.Errorf("failed to read activities directory: %w", err)
	}

	type fileToLoad struct {
		name    string
		path    string
		modTime time.Time
		size    int64
	}

	var filesToLoad []fileToLoad
	currentFiles := make(map[string]bool)
	hasChanges := false

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		filename := entry.Name()
		currentFiles[filename] = true

		info, err := entry.Info()
		if err != nil {
			continue
		}

		existing, found := s.cacheMap[filename]
		if !found || existing.modTime != info.ModTime() || existing.size != info.Size() {
			hasChanges = true
			filesToLoad = append(filesToLoad, fileToLoad{
				name:    filename,
				path:    filepath.Join(activitiesDir, filename),
				modTime: info.ModTime(),
				size:    info.Size(),
			})
		}
	}

	// Check if any deleted files need removal
	for cachedName := range s.cacheMap {
		if !currentFiles[cachedName] {
			delete(s.cacheMap, cachedName)
			hasChanges = true
		}
	}

	if !hasChanges && len(s.activities) > 0 {
		return nil
	}

	// Parse new / modified files in parallel using worker goroutines
	if len(filesToLoad) > 0 {
		workers := runtime.NumCPU()
		if workers < 2 {
			workers = 2
		}
		if workers > 8 {
			workers = 8
		}

		jobs := make(chan fileToLoad, len(filesToLoad))
		results := make(chan *cachedFileItem, len(filesToLoad))
		var wg sync.WaitGroup

		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for job := range jobs {
					data, err := os.ReadFile(job.path)
					if err != nil {
						continue
					}
					var act ActivityDetail
					if err := json.Unmarshal(data, &act); err != nil {
						continue
					}

					preDists := make(map[int64]float64)
					if act.Streams != nil {
						if distStream, ok := act.Streams["distance"]; ok && len(distStream.Data) > 0 {
							dSlice := getFloatSlice(distStream.Data)
							for _, effort := range act.SegmentEfforts {
								if effort.StartIndex >= 0 && effort.StartIndex < len(dSlice) {
									preDists[effort.ID] = dSlice[effort.StartIndex]
								}
							}
						}
					}

					sport := GetSportCategory(&act)
					var rStats *RunStats
					minTime := 900    // at least 15 min moving time for steady-state endurance
					minDist := 2500.0 // at least 2.5 km (5 km for cycling)
					if sport == "cycling" {
						minDist = 5000.0
					}
					if sport != "" && act.Distance >= minDist && act.MovingTime >= minTime && act.AverageHeartrate >= 80 && act.AverageHeartrate <= 220 {
						rStats = calculateRunStats(&act)
						if rStats != nil {
							rStats.Sport = sport
							// Physiological plausibility checks per sport
							if sport == "running" {
								if act.AverageHeartrate < 90 || rStats.EF > 3.0 || rStats.EF < 0.4 {
									rStats = nil
								}
							} else if sport == "cycling" {
								if rStats.EF > 6.0 || rStats.EF < 0.2 {
									rStats = nil
								}
							} else if sport == "skiing" {
								if rStats.EF > 2.5 || rStats.EF < 0.1 {
									rStats = nil
								}
							}
						}
					}

					hrSummary := computeActivityHRSummary(&act)
					efforts := act.SegmentEfforts
					// Nil out heavy arrays to free memory (streams already processed)
					act.Streams = nil
					act.SegmentEfforts = nil

					results <- &cachedFileItem{
						modTime:         job.modTime,
						size:            job.size,
						activity:        &act,
						runStats:        rStats,
						segmentEfforts:  efforts,
						preSegmentDists: preDists,
						hrSummary:       hrSummary,
					}
				}
			}()
		}

		for _, f := range filesToLoad {
			jobs <- f
		}
		close(jobs)

		wg.Wait()
		close(results)

		for item := range results {
			if item.activity != nil {
				filename := fmt.Sprintf("%d.json", item.activity.ID)
				s.cacheMap[filename] = item
			}
		}
	}

	// Rebuild sorted in-memory lists
	activities := make([]*ActivityDetail, 0, len(s.cacheMap))
	runs := make([]*RunStats, 0, len(s.cacheMap))
	yearsMap := make(map[int]bool)

	for _, item := range s.cacheMap {
		activities = append(activities, item.activity)
		if item.runStats != nil {
			runs = append(runs, item.runStats)
		}
		localYear := item.activity.LocalStartTime().Year()
		if localYear > 1970 {
			yearsMap[localYear] = true
		}
	}

	sort.Slice(activities, func(i, j int) bool {
		return activities[i].StartDate.Before(activities[j].StartDate)
	})

	sort.Slice(runs, func(i, j int) bool {
		return runs[i].Activity.StartDate.Before(runs[j].Activity.StartDate)
	})

	var years []int
	for y := range yearsMap {
		years = append(years, y)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(years)))

	// Calculate prior 7-day training load for each activity
	type priorLoad struct {
		distance float64
		timeS    int
		elev     float64
		count    int
		factor   float64
	}

	priorLoads := make(map[int64]priorLoad, len(activities))

	for i, act := range activities {
		windowStart := act.StartDate.Add(-7 * 24 * time.Hour)
		var pDist, pElev float64
		var pTime, pCount int

		for j := i - 1; j >= 0; j-- {
			prev := activities[j]
			if prev.StartDate.Before(windowStart) {
				break
			}
			pDist += prev.Distance
			pTime += prev.MovingTime
			pElev += prev.TotalElevationGain
			pCount++
		}

		// Flat-equivalent km: Distance + 7.5 * Elevation
		pEffKm := (pDist + (7.5 * pElev)) / 1000.0
		// 0.8% fatigue per 10km prior effective volume, capped at 20%
		pFactor := 1.0 + math.Min(0.20, (pEffKm/10.0)*0.008)
		if pFactor < 1.0 {
			pFactor = 1.0
		}

		priorLoads[act.ID] = priorLoad{
			distance: pDist,
			timeS:    pTime,
			elev:     pElev,
			count:    pCount,
			factor:   pFactor,
		}
	}

	// Update runs with prior load
	for _, r := range runs {
		if pl, ok := priorLoads[r.Activity.ID]; ok {
			r.Prior7DaysDistance = pl.distance
			r.Prior7DaysTime = pl.timeS
			r.Prior7DaysElev = pl.elev
			r.Prior7DaysCount = pl.count
			r.PreRunFatigueFactor = pl.factor
			r.AdjustedEF = r.EF * pl.factor
			if pl.factor > 0 {
				r.AdjustedPaceMinKm = r.AvgPaceMinKm / pl.factor
			}
		}
	}

	// Aggregate segment efforts across all cached activities, partitioned by sport
	segMap := make(map[string]*SegmentSummaryStats)

	for _, item := range s.cacheMap {
		if len(item.segmentEfforts) == 0 {
			continue
		}
		act := item.activity
		sport := GetSportCategory(act)
		if sport == "" {
			continue
		}
		pl := priorLoads[act.ID]

		for _, eff := range item.segmentEfforts {
			segID := eff.Segment.ID
			if segID == 0 {
				segID = eff.ID
			}
			segName := eff.Segment.Name
			if segName == "" {
				segName = eff.Name
			}
			if segName == "" {
				continue
			}

			dist := eff.Distance
			if dist <= 0 {
				dist = eff.Segment.Distance
			}
			movingTime := eff.MovingTime
			if movingTime <= 0 {
				movingTime = eff.ElapsedTime
			}
			if dist <= 0 || movingTime <= 0 {
				continue
			}

			speedMMin := dist / (float64(movingTime) / 60.0)
			paceMinKm := 1000.0 / speedMMin

			avgHR := eff.AverageHeartrate
			if avgHR <= 0 {
				avgHR = act.AverageHeartrate
			}
			// Skip efforts without heart rate data (useless for cardiovascular efficiency analysis)
			if avgHR <= 0 {
				continue
			}
			ef := speedMMin / avgHR

			// Filter out physiological sensor glitches / corrupt artifacts per sport
			if sport == "running" {
				if avgHR < 90.0 || avgHR > 220.0 {
					continue
				}
				if paceMinKm < 1.75 || paceMinKm > 20.0 {
					continue
				}
				if ef > 2.5 || ef < 0.3 {
					continue
				}
			} else if sport == "cycling" {
				if avgHR < 80.0 || avgHR > 220.0 {
					continue
				}
				if ef > 6.0 || ef < 0.2 {
					continue
				}
			} else if sport == "skiing" {
				if avgHR < 80.0 || avgHR > 220.0 {
					continue
				}
				if ef > 2.5 || ef < 0.1 {
					continue
				}
			}

			// In-run fatigue
			preTime := int(eff.StartDate.Sub(act.StartDate).Seconds())
			if preTime < 0 {
				preTime = 0
			}
			preDist := 0.0
			if d, ok := item.preSegmentDists[eff.ID]; ok && d > 0 {
				preDist = d
			} else if act.MovingTime > 0 && preTime > 0 {
				preDist = (act.Distance / float64(act.MovingTime)) * float64(preTime)
				if preDist > act.Distance {
					preDist = act.Distance
				}
			}

			preDistKm := preDist / 1000.0
			preTimeHrs := float64(preTime) / 3600.0
			inRunFactor := 1.0 + math.Min(0.25, (preDistKm*0.007)+(preTimeHrs*0.025))
			if inRunFactor < 1.0 {
				inRunFactor = 1.0
			}

			preRunFactor := pl.factor
			if preRunFactor < 1.0 {
				preRunFactor = 1.0
			}

			totalFactor := inRunFactor * preRunFactor
			fatigueBonusPct := (totalFactor - 1.0) * 100.0

			adjEF := ef * totalFactor
			adjPace := paceMinKm / totalFactor

			effortStat := &SegmentEffortStats{
				ID:                  eff.ID,
				ActivityID:          act.ID,
				ActivityName:        act.Name,
				Sport:               sport,
				SegmentID:           segID,
				SegmentName:         segName,
				StartDate:           eff.StartDate,
				StartDateLocal:      eff.StartDateLocal,
				Timezone:            act.Timezone,
				UtcOffset:           act.UtcOffset,
				Distance:            dist,
				MovingTime:          movingTime,
				ElapsedTime:         eff.ElapsedTime,
				AvgPaceMinKm:        paceMinKm,
				AvgHeartrate:        avgHR,
				MaxHeartrate:        eff.MaxHeartrate,
				PRRank:              eff.PRRank,
				EF:                  ef,
				PreSegmentDistance:  preDist,
				PreSegmentTime:      preTime,
				InRunFatigueFactor:  inRunFactor,
				Prior7DaysDistance:  pl.distance,
				Prior7DaysTime:      pl.timeS,
				Prior7DaysElev:      pl.elev,
				Prior7DaysCount:     pl.count,
				PreRunFatigueFactor: preRunFactor,
				TotalFatigueFactor:  totalFactor,
				FatigueBonusPct:     fatigueBonusPct,
				AdjustedEF:          adjEF,
				AdjustedPaceMinKm:   adjPace,
			}

			segKey := fmt.Sprintf("%s:%d", sport, segID)
			summary, ok := segMap[segKey]
			if !ok {
				summary = &SegmentSummaryStats{
					SegmentID:     segID,
					SegmentName:   segName,
					Sport:         sport,
					Distance:      eff.Segment.Distance,
					AverageGrade:  eff.Segment.AverageGrade,
					MaximumGrade:  eff.Segment.MaximumGrade,
					ElevationHigh: eff.Segment.ElevationHigh,
					ElevationLow:  eff.Segment.ElevationLow,
					ClimbCategory: eff.Segment.ClimbCategory,
					City:          eff.Segment.City,
					State:         eff.Segment.State,
				}
				if summary.Distance <= 0 {
					summary.Distance = dist
				}
				segMap[segKey] = summary
			}
			summary.Efforts = append(summary.Efforts, effortStat)
		}
	}

	// Collect and rank segments per sport (requiring minimum 4 useful efforts)
	var allSegments []*SegmentSummaryStats
	for _, sp := range []string{"running", "cycling", "skiing"} {
		var spSegs []*SegmentSummaryStats
		for _, seg := range segMap {
			if seg.Sport != sp || len(seg.Efforts) < 4 {
				continue
			}
			sort.Slice(seg.Efforts, func(i, j int) bool {
				return seg.Efforts[i].StartDate.Before(seg.Efforts[j].StartDate)
			})

			seg.EffortCount = len(seg.Efforts)
			seg.BestPaceMinKm = seg.Efforts[0].AvgPaceMinKm
			seg.BestAdjustedPace = seg.Efforts[0].AdjustedPaceMinKm
			seg.BestEF = seg.Efforts[0].EF
			seg.BestAdjustedEF = seg.Efforts[0].AdjustedEF

			for _, eff := range seg.Efforts {
				if eff.AvgPaceMinKm > 0 && eff.AvgPaceMinKm < seg.BestPaceMinKm {
					seg.BestPaceMinKm = eff.AvgPaceMinKm
				}
				if eff.AdjustedPaceMinKm > 0 && eff.AdjustedPaceMinKm < seg.BestAdjustedPace {
					seg.BestAdjustedPace = eff.AdjustedPaceMinKm
				}
				if eff.EF > seg.BestEF {
					seg.BestEF = eff.EF
				}
				if eff.AdjustedEF > seg.BestAdjustedEF {
					seg.BestAdjustedEF = eff.AdjustedEF
				}
			}
			spSegs = append(spSegs, seg)
		}

		sort.Slice(spSegs, func(i, j int) bool {
			if len(spSegs[i].Efforts) != len(spSegs[j].Efforts) {
				return len(spSegs[i].Efforts) > len(spSegs[j].Efforts)
			}
			return spSegs[i].SegmentName < spSegs[j].SegmentName
		})

		allSegments = append(allSegments, spSegs...)
	}

	// Cluster routes per sport
	var allClusters []*RouteCluster
	clusterID := 1
	for _, sp := range []string{"running", "cycling", "skiing"} {
		var spRuns []*RunStats
		for _, r := range runs {
			if r.Sport == sp {
				spRuns = append(spRuns, r)
			}
		}
		if len(spRuns) > 0 {
			clusters := clusterRuns(spRuns)
			for _, c := range clusters {
				c.ID = clusterID
				clusterID++
				c.Sport = sp
				allClusters = append(allClusters, c)
			}
		}
	}

	s.activities = activities
	s.runs = runs
	s.segments = allSegments
	s.clusters = allClusters
	s.years = years

	return nil
}

// CalculateStats evaluates periodic rollups on cached activities in memory.
func (s *ActivityStore) CalculateStats(opts StatsOptions) ([]*PeriodStats, *PeriodStats, []int, []string, error) {
	if err := s.ReloadIfNeeded(); err != nil {
		return nil, nil, nil, nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	periods, total, categories := CalculatePeriodicStatsFromActivities(s.activities, opts)
	return periods, total, s.years, categories, nil
}

// GetProgress returns the cached run stats, segment stats, and route clusters in memory.
func (s *ActivityStore) GetProgress() ([]*RunStats, []*SegmentSummaryStats, []*RouteCluster, error) {
	if err := s.ReloadIfNeeded(); err != nil {
		return nil, nil, nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.runs, s.segments, s.clusters, nil
}

// GetAvailableYears returns the list of cached activity years.
func (s *ActivityStore) GetAvailableYears() ([]int, error) {
	if err := s.ReloadIfNeeded(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.years, nil
}

// GetActivities returns activities matching the sport and name query filters,
// sorted newest first, along with the total count of matching activities.
// If limit <= 0, all matching activities are returned.
func (s *ActivityStore) GetActivities(limit int, sport string, query string) ([]*ActivityDetail, int, error) {
	if err := s.ReloadIfNeeded(); err != nil {
		return nil, 0, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	total := len(s.activities)
	if total == 0 {
		return nil, 0, nil
	}

	query = strings.TrimSpace(strings.ToLower(query))

	matchedTotal := 0
	var res []*ActivityDetail

	for i := total - 1; i >= 0; i-- {
		act := s.activities[i]
		if MatchesSportFilter(act, sport) {
			if query != "" && !strings.Contains(strings.ToLower(act.Name), query) {
				continue
			}
			matchedTotal++
			if limit <= 0 || len(res) < limit {
				res = append(res, act)
			}
		}
	}

	return res, matchedTotal, nil
}

// GetRecentActivities returns the most recent N activities matching the sport filter,
// sorted newest first, along with the total count of matching activities.
func (s *ActivityStore) GetRecentActivities(limit int, sport string) ([]*ActivityDetail, int, error) {
	return s.GetActivities(limit, sport, "")
}

// GetActivityDetailByID retrieves complete activity details by activity ID.
// It loads full data including streams, splits, and segment efforts from disk when available,
// falling back to the in-memory cache.
func (s *ActivityStore) GetActivityDetailByID(id int64) (*ActivityDetail, error) {
	if err := s.ReloadIfNeeded(); err != nil {
		return nil, err
	}

	// Try reading directly from disk first to get the complete raw data
	filePath := filepath.Join(s.dir, "activities", fmt.Sprintf("%d.json", id))
	if data, err := os.ReadFile(filePath); err == nil {
		var act ActivityDetail
		if err := json.Unmarshal(data, &act); err == nil {
			return &act, nil
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	filename := fmt.Sprintf("%d.json", id)
	if item, ok := s.cacheMap[filename]; ok && item.activity != nil {
		actCopy := *item.activity
		if len(item.segmentEfforts) > 0 && len(actCopy.SegmentEfforts) == 0 {
			actCopy.SegmentEfforts = item.segmentEfforts
		}
		return &actCopy, nil
	}

	for _, a := range s.activities {
		if a.ID == id {
			return a, nil
		}
	}

	return nil, fmt.Errorf("activity %d not found", id)
}

// YoYCTLPoint represents a sampled fitness (CTL) point on a shared day-of-year axis.
type YoYCTLPoint struct {
	DayOfYear int     `json:"day"`
	Date      string  `json:"date"`
	CTL       float64 `json:"ctl"`
}

// BestRecordJSON holds all-time vs. selected-season bests for a milestone.
type BestRecordJSON struct {
	Label            string `json:"label"`
	AllTimeVal       string `json:"all_time_val"`
	AllTimeDate      string `json:"all_time_date"`
	AllTimeActID     int64  `json:"all_time_activity_id"`
	AllTimeActName   string `json:"all_time_activity_name"`
	SeasonVal        string `json:"season_val,omitempty"`
	SeasonDate       string `json:"season_date,omitempty"`
	SeasonActID      int64  `json:"season_activity_id,omitempty"`
	SeasonActName    string `json:"season_activity_name,omitempty"`
}

// ShoeStatJSON summarizes mileage and surface breakdown for a single pair of shoes.
type ShoeStatJSON struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Primary       bool    `json:"primary"`
	TotalDistance float64 `json:"total_distance"`
	RoadDistance  float64 `json:"road_distance"`
	TrailDistance float64 `json:"trail_distance"`
	ActivityCount int     `json:"activity_count"`
	LastUsed      string  `json:"last_used"`
	LastUsedFmt   string  `json:"last_used_fmt"`
}

// YoYVolumePoint represents cumulative distance and elevation on a day-of-year axis.
type YoYVolumePoint struct {
	DayOfYear int     `json:"day"`
	Distance  float64 `json:"distance"`
	Elevation float64 `json:"elevation"`
}

// YoYVolumeResponse holds per-year cumulative volume series.
type YoYVolumeResponse struct {
	Sport  string                    `json:"sport"`
	Unit   string                    `json:"unit"`
	Series map[int][]YoYVolumePoint  `json:"series"`
	Years  []int                     `json:"years"`
}

// GetHRZoneData returns available years, activity HR summaries, the aggregated BPM histogram,
// and per-month BPM histograms ("YYYY-MM" -> bpm -> secs) filtered by sport and year (0 for all years).
func (s *ActivityStore) GetHRZoneData(sport string, year int) ([]int, []*ActivityHRSummary, map[int]int, map[string]map[int]int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	yearsCopy := make([]int, len(s.years))
	copy(yearsCopy, s.years)

	var summaries []*ActivityHRSummary
	histogram := make(map[int]int)
	monthly := make(map[string]map[int]int)

	for _, item := range s.cacheMap {
		if item.activity == nil || item.hrSummary == nil {
			continue
		}
		locTime := item.activity.LocalStartTime()
		if year > 0 && locTime.Year() != year {
			continue
		}
		if sport != "all" && GetSportCategory(item.activity) != sport {
			continue
		}

		summaries = append(summaries, item.hrSummary)
		monthKey := locTime.Format("2006-01")
		mHist := monthly[monthKey]
		if mHist == nil {
			mHist = make(map[int]int)
			monthly[monthKey] = mHist
		}
		for bpm, secs := range item.hrSummary.BPMSeconds {
			histogram[bpm] += secs
			mHist[bpm] += secs
		}
	}

	return yearsCopy, summaries, histogram, monthly
}

// GetTrainingLoad computes the daily HR TRIMP + CTL (42d) / ATL (7d) / TSB time series
// and a sampled year-over-year CTL comparison map, using hrMaxOverride if > 0.
func (s *ActivityStore) GetTrainingLoad(sport string, year int, hrMaxOverride float64) ([]TrainingLoadPoint, map[int][]YoYCTLPoint) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	hrMax := hrMaxOverride
	if hrMax <= 0 {
		var hrSummaries []*ActivityHRSummary
		for _, item := range s.cacheMap {
			if item.activity == nil {
				continue
			}
			if sport != "all" && GetSportCategory(item.activity) != sport {
				continue
			}
			if item.hrSummary != nil {
				hrSummaries = append(hrSummaries, item.hrSummary)
			}
		}
		_, meta := EstimateHRZones(hrSummaries)
		hrMax = float64(meta.EstimatedHRMax)
		if hrMax <= 0 {
			hrMax = 190.0
		}
	}

	dailyTRIMP := make(map[string]float64)
	for _, item := range s.cacheMap {
		if item.activity == nil {
			continue
		}
		if sport != "all" && GetSportCategory(item.activity) != sport {
			continue
		}
		trimp := computeActivityTRIMP(item.activity, item.hrSummary, hrMax)
		if trimp <= 0 {
			continue
		}
		dayKey := item.activity.LocalStartTime().Format("2006-01-02")
		dailyTRIMP[dayKey] += trimp
	}

	allPts := ComputeTrainingLoadSeries(dailyTRIMP, 0)
	if len(allPts) == 0 {
		return nil, nil
	}

	var filtered []TrainingLoadPoint
	yoy := make(map[int][]YoYCTLPoint)
	for idx, p := range allPts {
		t, err := time.Parse("2006-01-02", p.Date)
		if err != nil {
			continue
		}
		yr := t.Year()
		if year == 0 || yr == year {
			filtered = append(filtered, p)
		}
		doy := t.YearDay()
		isLastOfYear := idx == len(allPts)-1
		if !isLastOfYear {
			if nextT, err2 := time.Parse("2006-01-02", allPts[idx+1].Date); err2 == nil && nextT.Year() != yr {
				isLastOfYear = true
			}
		}
		if doy == 1 || doy%7 == 0 || isLastOfYear {
			yoy[yr] = append(yoy[yr], YoYCTLPoint{
				DayOfYear: doy,
				Date:      p.Date,
				CTL:       p.CTL,
			})
		}
	}

	return filtered, yoy
}

// GetRunningBests returns all-time and selected-season running milestones (BestEfforts + Longest Run + Biggest Vert).
func (s *ActivityStore) GetRunningBests(year int, unit string) []BestRecordJSON {
	s.mu.RLock()
	defer s.mu.RUnlock()

	distMult := 1.0 / 1000.0
	distUnit := "km"
	elevMult := 1.0
	elevUnit := "m"
	if unit == "mi" {
		distMult = 1.0 / 1609.344
		distUnit = "mi"
		elevMult = 3.28084
		elevUnit = "ft"
	}

	effortSpecs := []struct {
		key   string
		label string
	}{
		{"1k", "1K"},
		{"1 mile", "1 Mile"},
		{"5k", "5K"},
		{"10k", "10K"},
		{"half-marathon", "Half Marathon"},
		{"marathon", "Marathon"},
	}

	type effortBest struct {
		secs    int
		dateFmt string
		actID   int64
		actName string
	}
	allTimeEff := make(map[string]effortBest)
	seasonEff := make(map[string]effortBest)

	type maxRec struct {
		val     float64
		dateFmt string
		actID   int64
		actName string
	}
	var allTimeDist, seasonDist maxRec
	var allTimeVert, seasonVert maxRec

	for _, act := range s.activities {
		if !IsRunActivity(act) {
			continue
		}
		loc := act.LocalStartTime()
		inSeason := year > 0 && loc.Year() == year
		dateFmt := loc.Format("Jan 02, 2006")

		if act.Distance > allTimeDist.val {
			allTimeDist = maxRec{act.Distance, dateFmt, act.ID, act.Name}
		}
		if inSeason && act.Distance > seasonDist.val {
			seasonDist = maxRec{act.Distance, dateFmt, act.ID, act.Name}
		}

		if act.TotalElevationGain > allTimeVert.val {
			allTimeVert = maxRec{act.TotalElevationGain, dateFmt, act.ID, act.Name}
		}
		if inSeason && act.TotalElevationGain > seasonVert.val {
			seasonVert = maxRec{act.TotalElevationGain, dateFmt, act.ID, act.Name}
		}

		for _, be := range act.BestEfforts {
			secs := be.MovingTime
			if secs <= 0 {
				secs = be.ElapsedTime
			}
			if secs <= 0 {
				continue
			}
			k := strings.ToLower(strings.TrimSpace(be.Name))
			if cur, ok := allTimeEff[k]; !ok || secs < cur.secs {
				allTimeEff[k] = effortBest{secs, dateFmt, act.ID, act.Name}
			}
			if inSeason {
				if cur, ok := seasonEff[k]; !ok || secs < cur.secs {
					seasonEff[k] = effortBest{secs, dateFmt, act.ID, act.Name}
				}
			}
		}
	}

	var out []BestRecordJSON
	for _, spec := range effortSpecs {
		at, ok := allTimeEff[spec.key]
		if !ok {
			continue
		}
		rec := BestRecordJSON{
			Label:          spec.label,
			AllTimeVal:     formatDurationShort(at.secs),
			AllTimeDate:    at.dateFmt,
			AllTimeActID:   at.actID,
			AllTimeActName: at.actName,
		}
		if se, okS := seasonEff[spec.key]; okS {
			rec.SeasonVal = formatDurationShort(se.secs)
			rec.SeasonDate = se.dateFmt
			rec.SeasonActID = se.actID
			rec.SeasonActName = se.actName
		}
		out = append(out, rec)
	}

	if allTimeDist.val > 0 {
		rec := BestRecordJSON{
			Label:          "Longest Run",
			AllTimeVal:     fmt.Sprintf("%.1f %s", allTimeDist.val*distMult, distUnit),
			AllTimeDate:    allTimeDist.dateFmt,
			AllTimeActID:   allTimeDist.actID,
			AllTimeActName: allTimeDist.actName,
		}
		if seasonDist.val > 0 {
			rec.SeasonVal = fmt.Sprintf("%.1f %s", seasonDist.val*distMult, distUnit)
			rec.SeasonDate = seasonDist.dateFmt
			rec.SeasonActID = seasonDist.actID
			rec.SeasonActName = seasonDist.actName
		}
		out = append(out, rec)
	}

	if allTimeVert.val > 0 {
		rec := BestRecordJSON{
			Label:          "Biggest Vert",
			AllTimeVal:     fmt.Sprintf("%.0f %s", allTimeVert.val*elevMult, elevUnit),
			AllTimeDate:    allTimeVert.dateFmt,
			AllTimeActID:   allTimeVert.actID,
			AllTimeActName: allTimeVert.actName,
		}
		if seasonVert.val > 0 {
			rec.SeasonVal = fmt.Sprintf("%.0f %s", seasonVert.val*elevMult, elevUnit)
			rec.SeasonDate = seasonVert.dateFmt
			rec.SeasonActID = seasonVert.actID
			rec.SeasonActName = seasonVert.actName
		}
		out = append(out, rec)
	}

	return out
}

// GetShoeStats aggregates mileage, road/trail breakdown, and last-used date per shoe.
func (s *ActivityStore) GetShoeStats(unit string) []ShoeStatJSON {
	s.mu.RLock()
	defer s.mu.RUnlock()

	distMult := 1.0 / 1000.0
	if unit == "mi" {
		distMult = 1.0 / 1609.344
	}

	type acc struct {
		id         string
		name       string
		primary    bool
		totalM     float64
		roadM      float64
		trailM     float64
		count      int
		lastUsed   time.Time
	}
	byShoe := make(map[string]*acc)

	for _, act := range s.activities {
		if !IsShoeRelevant(act) {
			continue
		}
		shoeID := act.GearID
		shoeName := ""
		primary := false
		if act.Gear != nil {
			if act.Gear.ID != "" {
				shoeID = act.Gear.ID
			}
			shoeName = strings.TrimSpace(act.Gear.Name)
			primary = act.Gear.Primary
		}
		if shoeID == "" && shoeName == "" {
			continue
		}
		key := shoeID
		if key == "" {
			key = shoeName
		}
		a := byShoe[key]
		if a == nil {
			a = &acc{id: shoeID, name: shoeName}
			byShoe[key] = a
		}
		if a.name == "" && shoeName != "" {
			a.name = shoeName
		}
		if primary {
			a.primary = true
		}
		a.totalM += act.Distance
		if strings.EqualFold(act.SportType, "TrailRun") {
			a.trailM += act.Distance
		} else {
			a.roadM += act.Distance
		}
		a.count++
		loc := act.LocalStartTime()
		if loc.After(a.lastUsed) {
			a.lastUsed = loc
		}
	}

	out := make([]ShoeStatJSON, 0, len(byShoe))
	for _, a := range byShoe {
		name := a.name
		if name == "" {
			name = a.id
		}
		out = append(out, ShoeStatJSON{
			ID:            a.id,
			Name:          name,
			Primary:       a.primary,
			TotalDistance: math.Round(a.totalM*distMult*10) / 10,
			RoadDistance:  math.Round(a.roadM*distMult*10) / 10,
			TrailDistance: math.Round(a.trailM*distMult*10) / 10,
			ActivityCount: a.count,
			LastUsed:      a.lastUsed.Format("2006-01-02"),
			LastUsedFmt:   a.lastUsed.Format("Jan 02, 2006"),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].LastUsed != out[j].LastUsed {
			return out[i].LastUsed > out[j].LastUsed
		}
		return out[i].TotalDistance > out[j].TotalDistance
	})

	return out
}

// GetYoYVolume returns cumulative distance and elevation by day-of-year (1..366) for every year.
func (s *ActivityStore) GetYoYVolume(sport string, unit string) YoYVolumeResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	distMult := 1.0 / 1000.0
	elevMult := 1.0
	if unit == "mi" {
		distMult = 1.0 / 1609.344
		elevMult = 3.28084
	}

	// Accumulate daily totals per year
	type dayTotals struct {
		dist float64
		elev float64
	}
	byYearDay := make(map[int]map[int]dayTotals)
	maxDayByYear := make(map[int]int)
	now := time.Now()
	curYear := now.Year()
	curDOY := now.YearDay()

	for _, act := range s.activities {
		if sport != "" && sport != "all" {
			cat := GetSportCategory(act)
			if sport == "run" || sport == "running" {
				if cat != "running" {
					continue
				}
			} else if sport == "ride" || sport == "cycling" {
				if cat != "cycling" {
					continue
				}
			} else if sport == "ski" || sport == "skiing" {
				if cat != "skiing" {
					continue
				}
			} else if !strings.EqualFold(cat, sport) && !strings.EqualFold(act.SportType, sport) {
				continue
			}
		}
		loc := act.LocalStartTime()
		yr := loc.Year()
		doy := loc.YearDay()
		if byYearDay[yr] == nil {
			byYearDay[yr] = make(map[int]dayTotals)
		}
		cur := byYearDay[yr][doy]
		cur.dist += act.Distance
		cur.elev += act.TotalElevationGain
		byYearDay[yr][doy] = cur
		if doy > maxDayByYear[yr] {
			maxDayByYear[yr] = doy
		}
	}

	var years []int
	for yr := range byYearDay {
		years = append(years, yr)
	}
	sort.Ints(years)

	series := make(map[int][]YoYVolumePoint, len(years))
	for _, yr := range years {
		endDOY := 365
		if yr%4 == 0 && (yr%100 != 0 || yr%400 == 0) {
			endDOY = 366
		}
		if yr == curYear {
			endDOY = curDOY
			if maxDayByYear[yr] > endDOY {
				endDOY = maxDayByYear[yr]
			}
		}

		var cumDist, cumElev float64
		var pts []YoYVolumePoint
		for d := 1; d <= endDOY; d++ {
			if dt, ok := byYearDay[yr][d]; ok {
				cumDist += dt.dist
				cumElev += dt.elev
			}
			if d == 1 || d%7 == 0 || d == endDOY {
				pts = append(pts, YoYVolumePoint{
					DayOfYear: d,
					Distance:  math.Round(cumDist*distMult*10) / 10,
					Elevation: math.Round(cumElev * elevMult),
				})
			}
		}
		series[yr] = pts
	}

	return YoYVolumeResponse{
		Sport:  sport,
		Unit:   unit,
		Series: series,
		Years:  years,
	}
}
