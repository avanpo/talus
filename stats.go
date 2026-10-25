package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// StatsOptions specifies the filters and options for running a periodic stats report.
type StatsOptions struct {
	Period      string // "week", "month", "year" (default "month")
	Year        int    // 0 = all years, or specific year e.g. 2026
	SportType   string // "all", "run", "road", "trail", "ride", "hike", "walk" (default "run")
	Unit        string // "km" or "mi" (default "km")
	ShowSubtype bool   // whether to show road vs trail breakdown rows under each period
}

// PeriodStats accumulates metrics for a specific time period.
type PeriodStats struct {
	Key          string
	Label        string
	SortTime     time.Time
	Count        int
	DistanceM    float64
	MovingTimeS  int
	ElevationM   float64
	HRWeightSum  float64 // HR * moving_time
	HRDurationS  int     // total moving_time with valid HR
	GAPDistanceM float64 // Equivalent flat distance: D + 7.5 * ElevationGain

	Road  *PeriodStats
	Trail *PeriodStats
}

func newPeriodStats(key, label string, t time.Time) *PeriodStats {
	return &PeriodStats{
		Key:      key,
		Label:    label,
		SortTime: t,
	}
}

func (s *PeriodStats) add(act *ActivityDetail) {
	s.Count++
	s.DistanceM += act.Distance
	s.MovingTimeS += act.MovingTime
	s.ElevationM += act.TotalElevationGain
	// Minetti / trail equivalent distance: 1m ascent ~= 7.5m flat distance
	s.GAPDistanceM += act.Distance + (7.5 * act.TotalElevationGain)

	if act.AverageHeartrate > 0 && act.MovingTime > 0 {
		s.HRWeightSum += act.AverageHeartrate * float64(act.MovingTime)
		s.HRDurationS += act.MovingTime
	}
}

// GenerateStatsReport reads all activity files and displays periodic summary statistics.
func GenerateStatsReport(dir string, opts StatsOptions) error {
	activitiesDir := filepath.Join(dir, "activities")
	files, err := os.ReadDir(activitiesDir)
	if err != nil {
		return fmt.Errorf("failed to read activities directory: %w", err)
	}

	if opts.Period == "" {
		opts.Period = "month"
	}
	opts.Period = strings.ToLower(opts.Period)
	if opts.SportType == "" {
		opts.SportType = "run"
	}
	opts.SportType = strings.ToLower(opts.SportType)
	if opts.Unit == "" {
		opts.Unit = "km"
	}
	opts.Unit = strings.ToLower(opts.Unit)

	periodsMap := make(map[string]*PeriodStats)
	totalStats := newPeriodStats("TOTAL", "TOTAL", time.Time{})
	totalStats.Road = newPeriodStats("ROAD", "Road", time.Time{})
	totalStats.Trail = newPeriodStats("TRAIL", "Trail", time.Time{})

	for _, file := range files {
		if filepath.Ext(file.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(activitiesDir, file.Name()))
		if err != nil {
			continue
		}

		var act ActivityDetail
		if err := json.Unmarshal(data, &act); err != nil {
			continue
		}

		// Filter by Year
		if opts.Year > 0 && act.StartDate.Year() != opts.Year {
			continue
		}

		isRun := IsRunActivity(&act)
		isTrail := isRun && IsTrailRun(&act)
		isRoad := isRun && !isTrail
		isRide := act.Type == "Ride" || act.SportType == "Ride" || act.SportType == "GravelRide" || act.SportType == "MountainBikeRide" || act.SportType == "EMountainBikeRide" || act.SportType == "EBikeRide"
		isGravel := act.SportType == "GravelRide"
		isMTB := act.SportType == "MountainBikeRide" || act.SportType == "EMountainBikeRide"

		switch opts.SportType {
		case "run":
			if !isRun {
				continue
			}
		case "road":
			if !isRoad {
				continue
			}
		case "trail":
			if !isTrail {
				continue
			}
		case "ride", "bike":
			if !isRide {
				continue
			}
		case "gravel":
			if !isGravel {
				continue
			}
		case "mtb":
			if !isMTB {
				continue
			}
		case "hike":
			if act.Type != "Hike" && act.SportType != "Hike" {
				continue
			}
		case "walk":
			if act.Type != "Walk" && act.SportType != "Walk" {
				continue
			}
		case "all":
			// accept all activities
		default:
			if !strings.EqualFold(act.Type, opts.SportType) && !strings.EqualFold(act.SportType, opts.SportType) {
				continue
			}
		}

		// Determine period key
		var key, label string
		var sortTime time.Time

		switch opts.Period {
		case "year":
			key = fmt.Sprintf("%d", act.StartDate.Year())
			label = key
			sortTime = time.Date(act.StartDate.Year(), 1, 1, 0, 0, 0, 0, time.UTC)

		case "week":
			year, week := act.StartDate.ISOWeek()
			key = fmt.Sprintf("%04d-W%02d", year, week)
			// Compute start of week (Monday)
			startOfWeek := act.StartDate.AddDate(0, 0, -int((act.StartDate.Weekday()+6)%7))
			endOfWeek := startOfWeek.AddDate(0, 0, 6)
			label = fmt.Sprintf("%s (%s - %s)", key, startOfWeek.Format("Jan 02"), endOfWeek.Format("Jan 02"))
			sortTime = startOfWeek

		case "month":
			fallthrough
		default:
			key = act.StartDate.Format("2006-01")
			label = key
			sortTime = time.Date(act.StartDate.Year(), act.StartDate.Month(), 1, 0, 0, 0, 0, time.UTC)
		}

		p, exists := periodsMap[key]
		if !exists {
			p = newPeriodStats(key, label, sortTime)
			p.Road = newPeriodStats("ROAD", "Road", sortTime)
			p.Trail = newPeriodStats("TRAIL", "Trail", sortTime)
			periodsMap[key] = p
		}

		p.add(&act)
		totalStats.add(&act)

		if isRun {
			if isTrail {
				p.Trail.add(&act)
				totalStats.Trail.add(&act)
			} else {
				p.Road.add(&act)
				totalStats.Road.add(&act)
			}
		}
	}

	if totalStats.Count == 0 {
		if opts.Year > 0 {
			fmt.Printf("No matching activities found for year %d (sport=%s).\nTip: Use '--all-years' or '--year 0' to view all years, or '--year <YYYY>' for a specific year.\n",
				opts.Year, opts.SportType)
		} else {
			fmt.Printf("No matching activities found for filter: period=%s, sport=%s.\n",
				opts.Period, opts.SportType)
		}
		return nil
	}

	// Sort periods chronologically
	var periods []*PeriodStats
	for _, p := range periodsMap {
		periods = append(periods, p)
	}
	sort.Slice(periods, func(i, j int) bool {
		return periods[i].SortTime.Before(periods[j].SortTime)
	})

	// Print formatted report
	printStatsTable(periods, totalStats, opts)

	return nil
}

func printStatsTable(periods []*PeriodStats, total *PeriodStats, opts StatsOptions) {
	titlePeriod := strings.ToUpper(opts.Period)
	yearSubtitle := "ALL YEARS"
	if opts.Year > 0 {
		yearSubtitle = fmt.Sprintf("%d", opts.Year)
	}
	titleSport := strings.ToUpper(opts.SportType)

	distUnit := "km"
	elevUnit := "m"
	vertLabel := "Vert/10k"
	if opts.Unit == "mi" {
		distUnit = "mi"
		elevUnit = "ft"
		vertLabel = "Vert/10mi"
	}

	fmt.Println()
	fmt.Println("========================================================================================================")
	title := fmt.Sprintf("%s STATS (%s, %s)", titlePeriod, titleSport, yearSubtitle)
	padding := (104 - len(title)) / 2
	if padding < 0 {
		padding = 0
	}
	fmt.Printf("%s%s\n", strings.Repeat(" ", padding), title)
	fmt.Println("========================================================================================================")
	fmt.Printf("%-24s  %5s  %11s  %11s  %11s  %9s  %9s  %10s  %7s\n",
		"Period", "Count", "Distance", "Elevation", "Time", "Avg Pace", "Avg GAP", vertLabel, "Avg HR")
	fmt.Println("--------------------------------------------------------------------------------------------------------")

	showBreakdown := opts.ShowSubtype && (opts.SportType == "run" || opts.SportType == "all")

	for _, p := range periods {
		printStatsRow(p.Label, p, opts.Unit, distUnit, elevUnit, false)
		if showBreakdown && p.Count > 0 {
			if p.Road != nil && p.Road.Count > 0 {
				printStatsRow("  ├─ Road", p.Road, opts.Unit, distUnit, elevUnit, true)
			}
			if p.Trail != nil && p.Trail.Count > 0 {
				prefix := "  └─ Trail"
				if p.Road == nil || p.Road.Count == 0 {
					prefix = "  ├─ Trail"
				}
				printStatsRow(prefix, p.Trail, opts.Unit, distUnit, elevUnit, true)
			}
		}
	}

	fmt.Println("--------------------------------------------------------------------------------------------------------")
	printStatsRow("TOTAL", total, opts.Unit, distUnit, elevUnit, false)
	if showBreakdown && total.Count > 0 {
		if total.Road != nil && total.Road.Count > 0 {
			printStatsRow("  ├─ Road", total.Road, opts.Unit, distUnit, elevUnit, true)
		}
		if total.Trail != nil && total.Trail.Count > 0 {
			printStatsRow("  └─ Trail", total.Trail, opts.Unit, distUnit, elevUnit, true)
		}
	}
	fmt.Println("========================================================================================================")
	fmt.Println()
}

func printStatsRow(label string, s *PeriodStats, unit, distUnit, elevUnit string, isSubRow bool) {
	dist := s.DistanceM / 1000.0
	elev := s.ElevationM
	if unit == "mi" {
		dist = s.DistanceM / 1609.34
		elev = s.ElevationM * 3.28084
	}

	distStr := fmt.Sprintf("%.1f %s", dist, distUnit)
	elevStr := fmt.Sprintf("%s %s", formatComma(int(math.Round(elev))), elevUnit)
	timeStr := formatDurationShort(s.MovingTimeS)

	// Pace calculation (min/km or min/mi)
	paceStr := "-"
	if dist > 0 && s.MovingTimeS > 0 {
		paceDecimal := (float64(s.MovingTimeS) / 60.0) / dist
		paceStr = formatPace(paceDecimal) + "/" + unit
	}

	// GAP calculation
	gapStr := "-"
	if s.GAPDistanceM > 0 && s.MovingTimeS > 0 {
		gapDist := s.GAPDistanceM / 1000.0
		if unit == "mi" {
			gapDist = s.GAPDistanceM / 1609.34
		}
		if gapDist > 0 {
			gapDecimal := (float64(s.MovingTimeS) / 60.0) / gapDist
			gapStr = formatPace(gapDecimal) + "/" + unit
		}
	}

	// Vert Ratio (Elevation per 10km / 10mi)
	vertRatioStr := "-"
	if dist > 0 {
		ratio := (elev / dist) * 10.0
		vertRatioStr = fmt.Sprintf("%.0f %s", ratio, elevUnit)
	}

	// Avg Heart Rate
	hrStr := "-"
	if s.HRDurationS > 0 && s.HRWeightSum > 0 {
		avgHR := s.HRWeightSum / float64(s.HRDurationS)
		hrStr = fmt.Sprintf("%.0f bpm", avgHR)
	}

	countStr := fmt.Sprintf("%d", s.Count)
	if isSubRow {
		countStr = fmt.Sprintf("(%d)", s.Count)
	}

	fmt.Printf("%-24s  %5s  %11s  %11s  %11s  %9s  %9s  %10s  %7s\n",
		label, countStr, distStr, elevStr, timeStr, paceStr, gapStr, vertRatioStr, hrStr)
}

func formatDurationShort(secs int) string {
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dm", h, m)
	}
	return fmt.Sprintf("%dm %02ds", m, s)
}

func formatComma(n int) string {
	in := strconv.Itoa(n)
	if len(in) <= 3 {
		return in
	}
	var out []byte
	rem := len(in) % 3
	if rem > 0 {
		out = append(out, in[:rem]...)
		if len(in) > rem {
			out = append(out, ',')
		}
	}
	for i := rem; i < len(in); i += 3 {
		out = append(out, in[i:i+3]...)
		if i+3 < len(in) {
			out = append(out, ',')
		}
	}
	return string(out)
}
