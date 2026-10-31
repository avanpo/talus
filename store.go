package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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
		if item.activity.StartDate.Year() > 1970 {
			yearsMap[item.activity.StartDate.Year()] = true
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

		if len(spSegs) > 50 {
			spSegs = spSegs[:50]
		}
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

// GetRecentActivities returns the most recent N activities sorted newest first, along with the total count.
func (s *ActivityStore) GetRecentActivities(limit int) ([]*ActivityDetail, int, error) {
	if err := s.ReloadIfNeeded(); err != nil {
		return nil, 0, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	total := len(s.activities)
	if total == 0 {
		return nil, 0, nil
	}

	if limit <= 0 || limit > total {
		limit = total
	}

	res := make([]*ActivityDetail, limit)
	for i := 0; i < limit; i++ {
		res[i] = s.activities[total-1-i]
	}
	return res, total, nil
}
