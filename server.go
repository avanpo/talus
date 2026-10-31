package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var webFS embed.FS

// SyncDaemon manages continuous background synchronizations and state.
type SyncDaemon struct {
	dir                  string
	syncInterval         time.Duration
	checkUpdatesInterval time.Duration
	noSync               bool

	pendingClientID     string
	pendingClientSecret string

	mu              sync.RWMutex
	started         bool
	configNotifyCh  chan struct{}
	isSyncing       bool
	lastSyncTime    time.Time
	lastSyncErr     string
	lastSyncSummary string
	totalActivities int
	stopCh          chan struct{}
}

var daemon *SyncDaemon

func isConfigured(dir string) bool {
	_, err := LoadConfig(dir)
	return err == nil
}

func newSyncDaemon(dir string, syncInterval, checkUpdatesInterval time.Duration, noSync bool) *SyncDaemon {
	d := &SyncDaemon{
		dir:                  dir,
		syncInterval:         syncInterval,
		checkUpdatesInterval: checkUpdatesInterval,
		noSync:               noSync,
		stopCh:               make(chan struct{}),
		configNotifyCh:       make(chan struct{}, 1),
	}

	// Read initial metadata if available
	meta, err := LoadMetadata(dir)
	if err == nil {
		d.lastSyncTime = meta.LastSyncTime
		d.totalActivities = meta.TotalActivities
	}

	return d
}

func (d *SyncDaemon) NotifyConfigured() {
	d.mu.Lock()
	started := d.started
	d.mu.Unlock()

	if !started {
		d.Start()
		return
	}

	select {
	case d.configNotifyCh <- struct{}{}:
	default:
	}
}

func (d *SyncDaemon) Start() {
	if d.noSync {
		log.Println("[Daemon] Background synchronization disabled (--no-sync)")
		return
	}

	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return
	}
	d.started = true
	d.mu.Unlock()

	go func() {
		// If unconfigured at launch, wait in background until configuration appears
		if !isConfigured(d.dir) {
			log.Println("[Daemon] No config file found. Waiting for Strava authorization...")
			pollTicker := time.NewTicker(5 * time.Second)
			defer pollTicker.Stop()

			configured := false
			for !configured {
				select {
				case <-pollTicker.C:
					if isConfigured(d.dir) {
						configured = true
					}
				case <-d.configNotifyCh:
					if isConfigured(d.dir) {
						configured = true
					}
				case <-d.stopCh:
					return
				}
			}
			log.Println("[Daemon] Configuration detected! Resuming background sync daemon...")
		}

		log.Printf("[Daemon] Starting background sync daemon: sync interval=%v, check updates interval=%v\n",
			d.syncInterval, d.checkUpdatesInterval)

		// Initial sync after a short delay so the HTTP server binds and serves immediately
		select {
		case <-time.After(3 * time.Second):
			d.runSync(SyncOptions{}, "Initial startup sync")
		case <-d.stopCh:
			return
		}

		syncTicker := time.NewTicker(d.syncInterval)
		defer syncTicker.Stop()

		updatesTicker := time.NewTicker(d.checkUpdatesInterval)
		defer updatesTicker.Stop()

		for {
			select {
			case <-syncTicker.C:
				d.runSync(SyncOptions{}, "Periodic incremental sync")
			case <-updatesTicker.C:
				d.runSync(SyncOptions{RecentWindow: 60 * 24 * time.Hour}, "Daily edit & update scan (last 60d)")
			case <-d.stopCh:
				log.Println("[Daemon] Background sync daemon stopped")
				return
			}
		}
	}()
}

func (d *SyncDaemon) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	select {
	case <-d.stopCh:
		// already closed
	default:
		close(d.stopCh)
	}
	d.started = false
}

func (d *SyncDaemon) runSync(opts SyncOptions, reason string) {
	d.mu.Lock()
	if d.isSyncing {
		d.mu.Unlock()
		log.Printf("[Daemon] Sync already in progress, skipping %s\n", reason)
		return
	}
	d.isSyncing = true
	d.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			d.mu.Lock()
			d.isSyncing = false
			d.lastSyncErr = fmt.Sprintf("panic: %v", r)
			d.lastSyncTime = time.Now()
			d.mu.Unlock()
			log.Printf("[Daemon] !!! Panic recovered in %s: %v\n", reason, r)
		}
	}()

	log.Printf("[Daemon] >>> Starting %s...\n", reason)
	startTime := time.Now()
	err := SyncActivitiesWithOptions(d.dir, opts)

	d.mu.Lock()
	defer d.mu.Unlock()
	d.isSyncing = false
	d.lastSyncTime = time.Now()

	if err != nil {
		d.lastSyncErr = err.Error()
		log.Printf("[Daemon] !!! Sync failed (%s): %v (took %v)\n", reason, err, time.Since(startTime).Round(time.Second))
	} else {
		d.lastSyncErr = ""
		meta, metaErr := LoadMetadata(d.dir)
		if metaErr == nil {
			d.totalActivities = meta.TotalActivities
		}
		_ = GetGlobalStore(d.dir).ReloadIfNeeded()
		d.lastSyncSummary = fmt.Sprintf("Completed successfully in %v", time.Since(startTime).Round(time.Second))
		log.Printf("[Daemon] <<< Sync completed successfully (%s, took %v)\n", reason, time.Since(startTime).Round(time.Second))
	}
}

func (d *SyncDaemon) TriggerManualSync() bool {
	d.NotifyConfigured()

	d.mu.Lock()
	if d.isSyncing {
		d.mu.Unlock()
		return false
	}
	d.mu.Unlock()

	go d.runSync(SyncOptions{}, "Manual user-triggered sync")
	return true
}

// ----------------- HTTP Handlers & Data Formats -----------------

func getSystemUnit() string {
	u := strings.TrimSpace(strings.ToLower(os.Getenv("UNIT")))
	if u == "" {
		u = strings.TrimSpace(strings.ToLower(os.Getenv("UNITS")))
	}
	if u == "mi" || u == "mile" || u == "miles" || u == "imperial" {
		return "mi"
	}
	return "km"
}

type BreakdownJSON struct {
	Name          string  `json:"name"`
	Count         int     `json:"count"`
	Distance      float64 `json:"distance"`
	Elevation     float64 `json:"elevation"`
	MovingTimeS   int     `json:"moving_time_s"`
	MovingTimeHrs float64 `json:"moving_time_hrs"`
	MovingTimeFmt string  `json:"moving_time_fmt"`
}

type VolumeRowJSON struct {
	Key           string                   `json:"key"`
	Label         string                   `json:"label"`
	Count         int                      `json:"count"`
	Distance      float64                  `json:"distance"`
	Elevation     float64                  `json:"elevation"`
	MovingTimeS   int                      `json:"moving_time_s"`
	MovingTimeHrs float64                  `json:"moving_time_hrs"`
	MovingTimeFmt string                   `json:"moving_time_fmt"`
	AvgPace       string                   `json:"avg_pace"`
	AvgPaceMinKm  float64                  `json:"avg_pace_min_km"`
	AvgGAP        string                   `json:"avg_gap"`
	AvgGAPMinKm   float64                  `json:"avg_gap_min_km"`
	VertRatio     float64                  `json:"vert_ratio"`
	AvgHR         int                      `json:"avg_hr"`
	Breakdowns    map[string]BreakdownJSON `json:"breakdowns"`
	RoadDistance  float64                  `json:"road_distance"`
	RoadCount     int                      `json:"road_count"`
	TrailDistance float64                  `json:"trail_distance"`
	TrailCount    int                      `json:"trail_count"`
}

type VolumeStatsResponse struct {
	Periods    []VolumeRowJSON `json:"periods"`
	Total      VolumeRowJSON   `json:"total"`
	Years      []int           `json:"years"`
	Period     string          `json:"period"`
	Sport      string          `json:"sport"`
	Unit       string          `json:"unit"`
	Categories []string        `json:"categories"`
}

type ProgressRunJSON struct {
	ID                 int64   `json:"id"`
	Name               string  `json:"name"`
	Date               string  `json:"date"`
	DateFmt            string  `json:"date_fmt"`
	Sport              string  `json:"sport"`
	SportType          string  `json:"sport_type"`
	Distance           float64 `json:"distance"`
	Elevation          float64 `json:"elevation"`
	MovingTimeFmt      string  `json:"moving_time_fmt"`
	AvgPace            string  `json:"avg_pace"`
	AvgPaceMinKm       float64 `json:"avg_pace_min_km"`
	AdjustedPace       string  `json:"adjusted_pace"`
	AdjustedPaceMinKm  float64 `json:"adjusted_pace_min_km"`
	AvgGAP             string  `json:"avg_gap"`
	AvgGAPMinKm        float64 `json:"avg_gap_min_km"`
	AvgHR              int     `json:"avg_hr"`
	EF                 float64 `json:"ef"`
	AdjustedEF         float64 `json:"adjusted_ef"`
	Prior7DaysDistFmt  string  `json:"prior_7days_dist_fmt"`
	Prior7DaysCount    int     `json:"prior_7days_count"`
	PreRunFatiguePct   float64 `json:"pre_run_fatigue_pct"`
	DecouplingPct      float64 `json:"decoupling_pct"`
	HasDecoupling      bool    `json:"has_decoupling"`
}

type ProgressSegmentEffortJSON struct {
	ID                int64   `json:"id"`
	ActivityID        int64   `json:"activity_id"`
	ActivityName      string  `json:"activity_name"`
	Sport             string  `json:"sport"`
	Date              string  `json:"date"`
	DateFmt           string  `json:"date_fmt"`
	Distance          float64 `json:"distance"`
	MovingTimeFmt     string  `json:"moving_time_fmt"`
	MovingTimeS       int     `json:"moving_time_s"`
	AvgPace           string  `json:"avg_pace"`
	AvgPaceMinKm      float64 `json:"avg_pace_min_km"`
	AdjustedPace      string  `json:"adjusted_pace"`
	AdjustedPaceMinKm float64 `json:"adjusted_pace_min_km"`
	AvgHR             int     `json:"avg_hr"`
	MaxHR             int     `json:"max_hr"`
	EF                float64 `json:"ef"`
	AdjustedEF        float64 `json:"adjusted_ef"`
	PreSegmentDistFmt string  `json:"pre_segment_dist_fmt"`
	PreSegmentTimeFmt string  `json:"pre_segment_time_fmt"`
	Prior7DaysDistFmt string  `json:"prior_7days_dist_fmt"`
	Prior7DaysCount   int     `json:"prior_7days_count"`
	InRunFatiguePct   float64 `json:"in_run_fatigue_pct"`
	PreRunFatiguePct  float64 `json:"pre_run_fatigue_pct"`
	TotalFatiguePct   float64 `json:"total_fatigue_pct"`
	PRRank            *int    `json:"pr_rank"`
}

type ProgressSegmentJSON struct {
	ID               int64                       `json:"id"`
	Name             string                      `json:"name"`
	Sport            string                      `json:"sport"`
	Distance         float64                     `json:"distance"`
	AverageGrade     float64                     `json:"average_grade"`
	ClimbCategory    int                         `json:"climb_category"`
	City             string                      `json:"city"`
	EffortCount      int                         `json:"effort_count"`
	BestPace         string                      `json:"best_pace"`
	BestAdjustedPace string                      `json:"best_adjusted_pace"`
	BestEF           float64                     `json:"best_ef"`
	BestAdjustedEF   float64                     `json:"best_adjusted_ef"`
	Efforts          []ProgressSegmentEffortJSON `json:"efforts"`
}

type ProgressClusterJSON struct {
	ID               int               `json:"id"`
	RouteName        string            `json:"route_name"`
	Sport            string            `json:"sport"`
	Distance         float64           `json:"distance"`
	RunCount         int               `json:"run_count"`
	EFImprovementPct float64           `json:"ef_improvement_pct"`
	Runs             []ProgressRunJSON `json:"runs"`
}

type ProgressResponse struct {
	Sport    string                `json:"sport"`
	Runs     []ProgressRunJSON     `json:"runs"`
	Segments []ProgressSegmentJSON `json:"segments"`
	Clusters []ProgressClusterJSON `json:"clusters"`
	Unit     string                `json:"unit"`
	Years    []int                 `json:"years,omitempty"`
}

type DaemonStatusJSON struct {
	IsSyncing            bool   `json:"is_syncing"`
	LastSyncTime         string `json:"last_sync_time"`
	LastSyncAgo          string `json:"last_sync_ago"`
	LastSyncError        string `json:"last_sync_error"`
	LastSyncSummary      string `json:"last_sync_summary"`
	TotalActivities      int    `json:"total_activities"`
	SyncInterval         string `json:"sync_interval"`
	CheckUpdatesInterval string `json:"check_updates_interval"`
}

func handleStatsAPI(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	period := q.Get("period")
	if period == "" {
		period = "month"
	}
	sport := q.Get("sport")
	if sport == "" {
		sport = "cardio"
	}
	unit := q.Get("unit")
	if unit == "" {
		unit = getSystemUnit()
	}
	year := 0
	if yStr := q.Get("year"); yStr != "" {
		if y, err := strconv.Atoi(yStr); err == nil {
			year = y
		}
	}

	opts := StatsOptions{
		Period:    period,
		Year:      year,
		SportType: sport,
		Unit:      unit,
	}

	store := GetGlobalStore(daemon.dir)
	periods, total, years, categories, err := store.CalculateStats(opts)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error calculating stats: %v", err), http.StatusInternalServerError)
		return
	}

	distMult := 1.0 / 1000.0
	elevMult := 1.0
	if unit == "mi" {
		distMult = 1.0 / 1609.344
		elevMult = 3.28084
	}

	formatRow := func(p *PeriodStats) VolumeRowJSON {
		if p == nil {
			return VolumeRowJSON{}
		}

		dist := p.DistanceM * distMult
		elev := p.ElevationM * elevMult

		avgPaceStr := "-"
		var paceMinKm float64
		if p.DistanceM > 0 && p.MovingTimeS > 0 {
			speedMMin := p.DistanceM / (float64(p.MovingTimeS) / 60.0)
			paceMinKm = 1000.0 / speedMMin
			if unit == "mi" {
				paceMinMi := 1609.344 / speedMMin
				avgPaceStr = formatPace(paceMinMi) + "/mi"
			} else {
				avgPaceStr = formatPace(paceMinKm) + "/km"
			}
		}

		avgGAPStr := "-"
		var gapMinKm float64
		if p.GAPDistanceM > 0 && p.MovingTimeS > 0 {
			speedGAPMMin := p.GAPDistanceM / (float64(p.MovingTimeS) / 60.0)
			gapMinKm = 1000.0 / speedGAPMMin
			if unit == "mi" {
				gapMinMi := 1609.344 / speedGAPMMin
				avgGAPStr = formatPace(gapMinMi) + "/mi"
			} else {
				avgGAPStr = formatPace(gapMinKm) + "/km"
			}
		}

		vertRatio := 0.0
		if p.DistanceM > 0 {
			if unit == "mi" {
				distMi := p.DistanceM / 1609.344
				vertRatio = (p.ElevationM * 3.28084) / distMi * 10.0
			} else {
				distKm := p.DistanceM / 1000.0
				vertRatio = p.ElevationM / distKm * 10.0
			}
		}

		avgHR := 0
		if p.HRDurationS > 0 {
			avgHR = int(math.Round(p.HRWeightSum / float64(p.HRDurationS)))
		}

		bdMap := make(map[string]BreakdownJSON)
		if p.Breakdowns != nil {
			for cat, bd := range p.Breakdowns {
				catDist := bd.DistanceM * distMult
				catElev := bd.ElevationM * elevMult
				catHrs := float64(bd.MovingTimeS) / 3600.0
				bdMap[cat] = BreakdownJSON{
					Name:          bd.Name,
					Count:         bd.Count,
					Distance:      math.Round(catDist*10) / 10,
					Elevation:     math.Round(catElev),
					MovingTimeS:   bd.MovingTimeS,
					MovingTimeHrs: math.Round(catHrs*10) / 10,
					MovingTimeFmt: formatDurationShort(bd.MovingTimeS),
				}
			}
		}

		movingHrs := math.Round((float64(p.MovingTimeS)/3600.0)*10) / 10

		row := VolumeRowJSON{
			Key:           p.Key,
			Label:         p.Label,
			Count:         p.Count,
			Distance:      math.Round(dist*10) / 10,
			Elevation:     math.Round(elev),
			MovingTimeS:   p.MovingTimeS,
			MovingTimeHrs: movingHrs,
			MovingTimeFmt: formatDurationShort(p.MovingTimeS),
			AvgPace:       avgPaceStr,
			AvgPaceMinKm:  math.Round(paceMinKm*100) / 100,
			AvgGAP:        avgGAPStr,
			AvgGAPMinKm:   math.Round(gapMinKm*100) / 100,
			VertRatio:     math.Round(vertRatio),
			AvgHR:         avgHR,
			Breakdowns:    bdMap,
		}

		if p.Road != nil {
			row.RoadDistance = math.Round(p.Road.DistanceM*distMult*10) / 10
			row.RoadCount = p.Road.Count
		}
		if p.Trail != nil {
			row.TrailDistance = math.Round(p.Trail.DistanceM*distMult*10) / 10
			row.TrailCount = p.Trail.Count
		}

		return row
	}

	var rows []VolumeRowJSON
	for _, p := range periods {
		rows = append(rows, formatRow(p))
	}

	totalRow := formatRow(total)
	totalRow.Label = "Total"
	totalRow.Key = "TOTAL"

	resp := VolumeStatsResponse{
		Periods:    rows,
		Total:      totalRow,
		Years:      years,
		Period:     period,
		Sport:      sport,
		Unit:       unit,
		Categories: categories,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func handleProgressAPI(w http.ResponseWriter, r *http.Request) {
	unit := r.URL.Query().Get("unit")
	if unit == "" {
		unit = getSystemUnit()
	}

	year := 0
	if yStr := r.URL.Query().Get("year"); yStr != "" {
		if y, err := strconv.Atoi(yStr); err == nil && y > 0 {
			year = y
		}
	}

	sport := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sport")))
	if sport == "" || sport == "run" || sport == "running" {
		sport = "running"
	} else if sport == "ride" || sport == "bike" || sport == "cycling" {
		sport = "cycling"
	} else if sport == "ski" || sport == "ski_tour" || sport == "backcountry_ski" || sport == "skiing" {
		sport = "skiing"
	}

	distMult := 1.0 / 1000.0
	elevMult := 1.0
	paceUnit := "/km"
	paceDist := 1000.0
	if unit == "mi" {
		distMult = 1.0 / 1609.344
		elevMult = 3.28084
		paceUnit = "/mi"
		paceDist = 1609.344
	}

	runs, segments, clusters, err := GetProgressAnalysis(daemon.dir)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error calculating progress: %v", err), http.StatusInternalServerError)
		return
	}

	store := GetGlobalStore(daemon.dir)
	availableYears, _ := store.GetAvailableYears()

	formatRun := func(r *RunStats) ProgressRunJSON {
		dist := r.Activity.Distance * distMult
		elev := r.Activity.TotalElevationGain * elevMult

		avgPaceStr := "-"
		var paceMinKm float64
		if r.Activity.Distance > 0 && r.Activity.MovingTime > 0 {
			speedMMin := r.Activity.Distance / (float64(r.Activity.MovingTime) / 60.0)
			paceMinKm = 1000.0 / speedMMin
			avgPaceStr = formatPace(paceDist/speedMMin) + paceUnit
		}

		adjPaceStr := "-"
		if r.AdjustedPaceMinKm > 0 {
			if unit == "mi" {
				adjPaceStr = formatPace(r.AdjustedPaceMinKm*1.609344) + paceUnit
			} else {
				adjPaceStr = formatPace(r.AdjustedPaceMinKm) + paceUnit
			}
		}

		gapDist := r.Activity.Distance + (7.5 * r.Activity.TotalElevationGain)
		avgGAPStr := "-"
		var gapMinKm float64
		if gapDist > 0 && r.Activity.MovingTime > 0 {
			speedGAPMMin := gapDist / (float64(r.Activity.MovingTime) / 60.0)
			gapMinKm = 1000.0 / speedGAPMMin
			avgGAPStr = formatPace(paceDist/speedGAPMMin) + paceUnit
		}

		priorDistStr := "-"
		if r.Prior7DaysCount > 0 {
			priorDistStr = fmt.Sprintf("%.1f %s", r.Prior7DaysDistance*distMult, unit)
		}

		preRunFatiguePct := 0.0
		if r.PreRunFatigueFactor > 1.0 {
			preRunFatiguePct = math.Round((r.PreRunFatigueFactor-1.0)*1000.0) / 10.0
		}

		return ProgressRunJSON{
			ID:                r.Activity.ID,
			Name:              r.Activity.Name,
			Date:              r.Activity.StartDate.Format(time.RFC3339),
			DateFmt:           r.Activity.StartDate.Format("Jan 02, 2006"),
			Sport:             r.Sport,
			SportType:         r.Activity.SportType,
			Distance:          math.Round(dist*10) / 10,
			Elevation:         math.Round(elev),
			MovingTimeFmt:     formatDurationShort(r.Activity.MovingTime),
			AvgPace:           avgPaceStr,
			AvgPaceMinKm:      math.Round(paceMinKm*100) / 100,
			AdjustedPace:      adjPaceStr,
			AdjustedPaceMinKm: math.Round(r.AdjustedPaceMinKm*100) / 100,
			AvgGAP:            avgGAPStr,
			AvgGAPMinKm:       math.Round(gapMinKm*100) / 100,
			AvgHR:             int(math.Round(r.Activity.AverageHeartrate)),
			EF:                math.Round(r.EF*1000) / 1000,
			AdjustedEF:        math.Round(r.AdjustedEF*1000) / 1000,
			Prior7DaysDistFmt: priorDistStr,
			Prior7DaysCount:   r.Prior7DaysCount,
			PreRunFatiguePct:  preRunFatiguePct,
			DecouplingPct:     math.Round(r.DecouplingPct*10) / 10,
			HasDecoupling:     r.HasDecoupling,
		}
	}

	var runsJSON []ProgressRunJSON
	for _, run := range runs {
		if sport != "all" && run.Sport != sport {
			continue
		}
		if year > 0 && run.Activity.StartDate.Year() != year {
			continue
		}
		runsJSON = append(runsJSON, formatRun(run))
	}

	var segmentsJSON []ProgressSegmentJSON
	for _, seg := range segments {
		if sport != "all" && seg.Sport != sport {
			continue
		}
		var effortsJSON []ProgressSegmentEffortJSON
		for _, eff := range seg.Efforts {
			if year > 0 && eff.StartDate.Year() != year {
				continue
			}
			effDist := eff.Distance * distMult
			rawPaceStr := "-"
			if eff.AvgPaceMinKm > 0 {
				if unit == "mi" {
					rawPaceStr = formatPace(eff.AvgPaceMinKm*1.609344) + paceUnit
				} else {
					rawPaceStr = formatPace(eff.AvgPaceMinKm) + paceUnit
				}
			}
			adjPaceStr := "-"
			if eff.AdjustedPaceMinKm > 0 {
				if unit == "mi" {
					adjPaceStr = formatPace(eff.AdjustedPaceMinKm*1.609344) + paceUnit
				} else {
					adjPaceStr = formatPace(eff.AdjustedPaceMinKm) + paceUnit
				}
			}

			preDistStr := fmt.Sprintf("%.1f %s", eff.PreSegmentDistance*distMult, unit)
			priorDistStr := fmt.Sprintf("%.1f %s", eff.Prior7DaysDistance*distMult, unit)

			effortsJSON = append(effortsJSON, ProgressSegmentEffortJSON{
				ID:                eff.ID,
				ActivityID:        eff.ActivityID,
				ActivityName:      eff.ActivityName,
				Sport:             eff.Sport,
				Date:              eff.StartDate.Format(time.RFC3339),
				DateFmt:           eff.StartDate.Format("Jan 02, 2006"),
				Distance:          math.Round(effDist*100) / 100,
				MovingTimeFmt:     formatDurationShort(eff.MovingTime),
				MovingTimeS:       eff.MovingTime,
				AvgPace:           rawPaceStr,
				AvgPaceMinKm:      math.Round(eff.AvgPaceMinKm*100) / 100,
				AdjustedPace:      adjPaceStr,
				AdjustedPaceMinKm: math.Round(eff.AdjustedPaceMinKm*100) / 100,
				AvgHR:             int(math.Round(eff.AvgHeartrate)),
				MaxHR:             int(math.Round(eff.MaxHeartrate)),
				EF:                math.Round(eff.EF*1000) / 1000,
				AdjustedEF:        math.Round(eff.AdjustedEF*1000) / 1000,
				PreSegmentDistFmt: preDistStr,
				PreSegmentTimeFmt: formatDurationShort(eff.PreSegmentTime),
				Prior7DaysDistFmt: priorDistStr,
				Prior7DaysCount:   eff.Prior7DaysCount,
				InRunFatiguePct:   math.Round((eff.InRunFatigueFactor-1.0)*1000.0) / 10.0,
				PreRunFatiguePct:  math.Round((eff.PreRunFatigueFactor-1.0)*1000.0) / 10.0,
				TotalFatiguePct:   math.Round(eff.FatigueBonusPct*10.0) / 10.0,
				PRRank:            eff.PRRank,
			})
		}

		if len(effortsJSON) == 0 {
			continue
		}

		bestRawPaceStr := "-"
		bestAdjPaceStr := "-"
		bestEF := 0.0
		bestAdjustedEF := 0.0
		var bestRawPaceMinKm float64
		var bestAdjPaceMinKm float64

		for _, eff := range effortsJSON {
			if eff.AvgPaceMinKm > 0 && (bestRawPaceMinKm == 0 || eff.AvgPaceMinKm < bestRawPaceMinKm) {
				bestRawPaceMinKm = eff.AvgPaceMinKm
			}
			if eff.AdjustedPaceMinKm > 0 && (bestAdjPaceMinKm == 0 || eff.AdjustedPaceMinKm < bestAdjPaceMinKm) {
				bestAdjPaceMinKm = eff.AdjustedPaceMinKm
			}
			if eff.EF > bestEF {
				bestEF = eff.EF
			}
			if eff.AdjustedEF > bestAdjustedEF {
				bestAdjustedEF = eff.AdjustedEF
			}
		}

		if bestRawPaceMinKm > 0 {
			if unit == "mi" {
				bestRawPaceStr = formatPace(bestRawPaceMinKm*1.609344) + paceUnit
			} else {
				bestRawPaceStr = formatPace(bestRawPaceMinKm) + paceUnit
			}
		}
		if bestAdjPaceMinKm > 0 {
			if unit == "mi" {
				bestAdjPaceStr = formatPace(bestAdjPaceMinKm*1.609344) + paceUnit
			} else {
				bestAdjPaceStr = formatPace(bestAdjPaceMinKm) + paceUnit
			}
		}

		segmentsJSON = append(segmentsJSON, ProgressSegmentJSON{
			ID:               seg.SegmentID,
			Name:             seg.SegmentName,
			Sport:            seg.Sport,
			Distance:         math.Round(seg.Distance*distMult*100) / 100,
			AverageGrade:     math.Round(seg.AverageGrade*10) / 10,
			ClimbCategory:    seg.ClimbCategory,
			City:             seg.City,
			EffortCount:      len(effortsJSON),
			BestPace:         bestRawPaceStr,
			BestAdjustedPace: bestAdjPaceStr,
			BestEF:           math.Round(bestEF*1000) / 1000,
			BestAdjustedEF:   math.Round(bestAdjustedEF*1000) / 1000,
			Efforts:          effortsJSON,
		})
	}

	var clustersJSON []ProgressClusterJSON
	for _, cluster := range clusters {
		if sport != "all" && cluster.Sport != sport {
			continue
		}
		var clusterRunsJSON []ProgressRunJSON
		for _, cr := range cluster.Runs {
			if year > 0 && cr.Activity.StartDate.Year() != year {
				continue
			}
			clusterRunsJSON = append(clusterRunsJSON, formatRun(cr))
		}
		if len(clusterRunsJSON) < 1 {
			continue
		}

		firstRun := clusterRunsJSON[0]
		lastRun := clusterRunsJSON[len(clusterRunsJSON)-1]
		efDiff := lastRun.EF - firstRun.EF
		efImprovementPct := 0.0
		if firstRun.EF > 0 {
			efImprovementPct = math.Round(((efDiff/firstRun.EF)*100.0)*10) / 10
		}

		clustersJSON = append(clustersJSON, ProgressClusterJSON{
			ID:               cluster.ID,
			RouteName:        cluster.RouteName,
			Sport:            cluster.Sport,
			Distance:         math.Round(cluster.Distance*distMult*10) / 10,
			RunCount:         len(clusterRunsJSON),
			EFImprovementPct: efImprovementPct,
			Runs:             clusterRunsJSON,
		})
	}

	resp := ProgressResponse{
		Sport:    sport,
		Runs:     runsJSON,
		Segments: segmentsJSON,
		Clusters: clustersJSON,
		Unit:     unit,
		Years:    availableYears,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func handleStatusAPI(w http.ResponseWriter, r *http.Request) {
	daemon.mu.RLock()
	defer daemon.mu.RUnlock()

	lastSyncTimeStr := ""
	lastSyncAgo := "Never"
	if !daemon.lastSyncTime.IsZero() {
		lastSyncTimeStr = daemon.lastSyncTime.Format(time.RFC3339)
		duration := time.Since(daemon.lastSyncTime)
		if duration < time.Minute {
			lastSyncAgo = "Just now"
		} else if duration < time.Hour {
			lastSyncAgo = fmt.Sprintf("%dm ago", int(duration.Minutes()))
		} else if duration < 24*time.Hour {
			lastSyncAgo = fmt.Sprintf("%dh ago", int(duration.Hours()))
		} else {
			lastSyncAgo = fmt.Sprintf("%dd ago", int(duration.Hours()/24))
		}
	}

	status := DaemonStatusJSON{
		IsSyncing:            daemon.isSyncing,
		LastSyncTime:         lastSyncTimeStr,
		LastSyncAgo:          lastSyncAgo,
		LastSyncError:        daemon.lastSyncErr,
		LastSyncSummary:      daemon.lastSyncSummary,
		TotalActivities:      daemon.totalActivities,
		SyncInterval:         daemon.syncInterval.String(),
		CheckUpdatesInterval: daemon.checkUpdatesInterval.String(),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status)
}

func handleTriggerSyncAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	started := daemon.TriggerManualSync()
	w.Header().Set("Content-Type", "application/json")
	if started {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "started", "message": "Sync started in background"})
	} else {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "in_progress", "message": "Sync already running"})
	}
}

type SetupPageData struct {
	ClientID     string
	ClientSecret string
	Error        string
}

func handleSetupPage(w http.ResponseWriter, r *http.Request) {
	if isConfigured(daemon.dir) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	var tmpl *template.Template
	var err error
	if _, statErr := os.Stat("web/setup.html"); statErr == nil {
		tmpl, err = template.ParseFiles("web/setup.html")
	} else {
		tmpl, err = template.ParseFS(webFS, "web/setup.html")
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	data := SetupPageData{
		ClientID:     os.Getenv("STRAVA_CLIENT_ID"),
		ClientSecret: os.Getenv("STRAVA_CLIENT_SECRET"),
		Error:        r.URL.Query().Get("error"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	_ = tmpl.Execute(w, data)
}

func handleSetupAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	clientID := strings.TrimSpace(r.FormValue("client_id"))
	clientSecret := strings.TrimSpace(r.FormValue("client_secret"))

	if clientID == "" || clientSecret == "" {
		http.Redirect(w, r, "/setup?error=Please+provide+both+Client+ID+and+Client+Secret", http.StatusSeeOther)
		return
	}

	daemon.mu.Lock()
	daemon.pendingClientID = clientID
	daemon.pendingClientSecret = clientSecret
	daemon.mu.Unlock()

	redirectURI := fmt.Sprintf("http://%s/callback", r.Host)
	authURL := fmt.Sprintf("https://www.strava.com/oauth/authorize?client_id=%s&redirect_uri=%s&response_type=code&scope=activity:read_all",
		url.QueryEscape(clientID), url.QueryEscape(redirectURI))

	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

func handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		errMsg := r.URL.Query().Get("error")
		if errMsg == "" {
			errMsg = "Authorization code missing in callback"
		}
		http.Redirect(w, r, "/setup?error="+url.QueryEscape(errMsg), http.StatusSeeOther)
		return
	}

	daemon.mu.RLock()
	clientID := daemon.pendingClientID
	clientSecret := daemon.pendingClientSecret
	daemon.mu.RUnlock()

	if clientID == "" || clientSecret == "" {
		clientID = os.Getenv("STRAVA_CLIENT_ID")
		clientSecret = os.Getenv("STRAVA_CLIENT_SECRET")
	}

	if clientID == "" || clientSecret == "" {
		http.Redirect(w, r, "/setup?error=Credentials+missing.+Please+re-enter+your+Client+ID+and+Secret", http.StatusSeeOther)
		return
	}

	err := exchangeAndSaveToken(daemon.dir, clientID, clientSecret, code)
	if err != nil {
		http.Redirect(w, r, "/setup?error="+url.QueryEscape("Authorization failed: "+err.Error()), http.StatusSeeOther)
		return
	}

	log.Println("[Server] Strava authorization successful! Configured .talus-config.json")

	// Trigger daemon start or wake up background loop now that credentials exist
	daemon.NotifyConfigured()

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func serveHTMLFile(w http.ResponseWriter, relPath string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")

	// Read from disk first if available so UI updates reflect immediately without restarting server
	if data, err := os.ReadFile(relPath); err == nil {
		_, _ = w.Write(data)
		return
	}

	// Fallback to embedded filesystem
	f, err := webFS.Open(relPath)
	if err != nil {
		http.Error(w, relPath+" not found", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	_, _ = io.Copy(w, f)
}

func handleVolumePage(w http.ResponseWriter, r *http.Request) {
	if !isConfigured(daemon.dir) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	serveHTMLFile(w, "web/index.html")
}

func handleActivitiesPage(w http.ResponseWriter, r *http.Request) {
	if !isConfigured(daemon.dir) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	serveHTMLFile(w, "web/activities.html")
}

func handleProgressPage(w http.ResponseWriter, r *http.Request) {
	if !isConfigured(daemon.dir) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}

	serveHTMLFile(w, "web/progress.html")
}

type ActivityItemJSON struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	Type           string  `json:"type"`
	SportType      string  `json:"sport_type"`
	StartDate      string  `json:"start_date"`
	StartDateFmt   string  `json:"start_date_fmt"`
	Distance       float64 `json:"distance"`
	DistanceFmt    string  `json:"distance_fmt"`
	Elevation      float64 `json:"elevation"`
	ElevationFmt   string  `json:"elevation_fmt"`
	ElapsedTime    int     `json:"elapsed_time"`
	ElapsedTimeFmt string  `json:"elapsed_time_fmt"`
	MovingTime     int     `json:"moving_time"`
	MovingTimeFmt  string  `json:"moving_time_fmt"`
	Shoe           string  `json:"shoe"`
	ShoeRelevant   bool    `json:"shoe_relevant"`
}

type ActivitiesResponseJSON struct {
	Unit       string             `json:"unit"`
	Activities []ActivityItemJSON `json:"activities"`
	Total      int                `json:"total"`
}

func formatSportType(sportType, actType string) string {
	st := sportType
	if st == "" {
		st = actType
	}
	if st == "" {
		return "Activity"
	}
	switch st {
	case "TrailRun":
		return "Trail Run"
	case "VirtualRun":
		return "Virtual Run"
	case "MountainBikeRide":
		return "Mountain Bike"
	case "GravelRide":
		return "Gravel Ride"
	case "EBikeRide":
		return "E-Bike Ride"
	case "VirtualRide":
		return "Virtual Ride"
	case "BackcountrySki":
		return "Backcountry Ski"
	case "NordicSki":
		return "Nordic Ski"
	case "AlpineSki":
		return "Alpine Ski"
	case "WeightTraining":
		return "Weight Training"
	default:
		var sb strings.Builder
		for i, r := range st {
			if i > 0 && r >= 'A' && r <= 'Z' {
				sb.WriteRune(' ')
			}
			sb.WriteRune(r)
		}
		return sb.String()
	}
}

func formatDurationExact(secs int) string {
	if secs <= 0 {
		return "0s"
	}
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	if h > 0 {
		if s > 0 {
			return fmt.Sprintf("%dh %02dm %02ds", h, m, s)
		}
		return fmt.Sprintf("%dh %02dm", h, m)
	}
	if s > 0 {
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	return fmt.Sprintf("%dm", m)
}

func handleActivitiesAPI(w http.ResponseWriter, r *http.Request) {
	limit := 10
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		if l, err := strconv.Atoi(lStr); err == nil && l > 0 {
			limit = l
		}
	}

	sport := r.URL.Query().Get("sport")

	unit := r.URL.Query().Get("unit")
	if unit == "" {
		unit = getSystemUnit()
	}

	store := GetGlobalStore(daemon.dir)
	activities, total, err := store.GetRecentActivities(limit, sport)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error retrieving activities: %v", err), http.StatusInternalServerError)
		return
	}

	items := make([]ActivityItemJSON, 0, len(activities))
	for _, act := range activities {
		var dist float64
		var distFmt string
		var elev float64
		var elevFmt string

		if unit == "mi" {
			dist = act.Distance / 1609.344
			distFmt = fmt.Sprintf("%.2f mi", dist)
			elev = act.TotalElevationGain * 3.28084
			elevFmt = fmt.Sprintf("%s ft", formatComma(int(math.Round(elev))))
		} else {
			dist = act.Distance / 1000.0
			distFmt = fmt.Sprintf("%.2f km", dist)
			elev = act.TotalElevationGain
			elevFmt = fmt.Sprintf("%s m", formatComma(int(math.Round(elev))))
		}

		elapsedSecs := act.ElapsedTime
		if elapsedSecs == 0 {
			elapsedSecs = act.MovingTime
		}

		sportName := formatSportType(act.SportType, act.Type)
		shoeRel := IsShoeRelevant(act)
		shoeName := act.ShoeName()

		items = append(items, ActivityItemJSON{
			ID:             act.ID,
			Name:           act.Name,
			Type:           act.Type,
			SportType:      sportName,
			StartDate:      act.StartDate.Format(time.RFC3339),
			StartDateFmt:   act.StartDate.Format("Jan 02, 2006"),
			Distance:       math.Round(dist*100) / 100,
			DistanceFmt:    distFmt,
			Elevation:      math.Round(elev),
			ElevationFmt:   elevFmt,
			ElapsedTime:    elapsedSecs,
			ElapsedTimeFmt: formatDurationExact(elapsedSecs),
			MovingTime:     act.MovingTime,
			MovingTimeFmt:  formatDurationShort(act.MovingTime),
			Shoe:           shoeName,
			ShoeRelevant:   shoeRel,
		})
	}

	resp := ActivitiesResponseJSON{
		Unit:       unit,
		Activities: items,
		Total:      total,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// StartWebServer starts the HTTP server and sync daemon.
func StartWebServer(dir string, port int, syncInterval, checkUpdatesInterval time.Duration, noSync bool) error {
	daemon = newSyncDaemon(dir, syncInterval, checkUpdatesInterval, noSync)
	daemon.Start()
	defer daemon.Stop()

	mux := http.NewServeMux()
	mux.HandleFunc("/setup", handleSetupPage)
	mux.HandleFunc("/setup/auth", handleSetupAuth)
	mux.HandleFunc("/callback", handleOAuthCallback)
	mux.HandleFunc("/", handleVolumePage)
	mux.HandleFunc("/activities", handleActivitiesPage)
	mux.HandleFunc("/progress", handleProgressPage)
	mux.HandleFunc("/api/stats", handleStatsAPI)
	mux.HandleFunc("/api/progress", handleProgressAPI)
	mux.HandleFunc("/api/activities", handleActivitiesAPI)
	mux.HandleFunc("/api/status", handleStatusAPI)
	mux.HandleFunc("/api/sync", handleTriggerSyncAPI)

	addr := fmt.Sprintf(":%d", port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// Handle graceful shutdown on SIGINT/SIGTERM
	stopSignal := make(chan os.Signal, 1)
	signal.Notify(stopSignal, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-stopSignal
		log.Println("[Server] Shutting down web server...")
		_ = srv.Close()
	}()

	log.Printf("[Server] talus web dashboard running at http://localhost:%d\n", port)
	log.Printf("[Server] Data directory: %s\n", dir)

	// Warm up memory cache in background on startup
	go func() {
		startTime := time.Now()
		if err := GetGlobalStore(dir).ReloadIfNeeded(); err == nil {
			store := GetGlobalStore(dir)
			store.mu.RLock()
			count := len(store.activities)
			store.mu.RUnlock()
			log.Printf("[Cache] Indexed %d activities in-memory in %v\n", count, time.Since(startTime).Round(time.Millisecond))
		}
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("web server error: %w", err)
	}

	return nil
}
