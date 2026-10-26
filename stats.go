package main

import (
	"fmt"
	"math"
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

// SubBreakdown tracks metrics for a specific sport subtype (e.g. Trail, Road, Treadmill, Gravel, MTB, Ski Touring, etc.)
type SubBreakdown struct {
	Name        string  `json:"name"`
	Count       int     `json:"count"`
	DistanceM   float64 `json:"distance_m"`
	MovingTimeS int     `json:"moving_time_s"`
	ElevationM  float64 `json:"elevation_m"`
}

func (b *SubBreakdown) add(act *ActivityDetail) {
	b.Count++
	b.DistanceM += act.Distance
	b.MovingTimeS += act.MovingTime
	b.ElevationM += act.TotalElevationGain
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

	Breakdowns map[string]*SubBreakdown

	Road  *PeriodStats
	Trail *PeriodStats
}

func newPeriodStats(key, label string, t time.Time) *PeriodStats {
	return &PeriodStats{
		Key:        key,
		Label:      label,
		SortTime:   t,
		Breakdowns: make(map[string]*SubBreakdown),
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

// IsTreadmillRun returns true if the run is indoor / treadmill / virtual.
func IsTreadmillRun(act *ActivityDetail) bool {
	if act.SportType == "VirtualRun" || act.Type == "VirtualRun" {
		return true
	}
	name := strings.ToLower(act.Name)
	return strings.Contains(name, "treadmill") || strings.Contains(name, "zwift run")
}

// IsRideActivity returns true if activity is any form of cycling.
func IsRideActivity(act *ActivityDetail) bool {
	return act.Type == "Ride" || act.SportType == "Ride" ||
		act.SportType == "GravelRide" ||
		act.SportType == "MountainBikeRide" || act.SportType == "EMountainBikeRide" ||
		act.SportType == "VirtualRide" || act.Type == "VirtualRide" ||
		act.SportType == "EBikeRide"
}

// IsGravelRide returns true if activity is gravel cycling.
func IsGravelRide(act *ActivityDetail) bool {
	return act.SportType == "GravelRide"
}

// IsMTBRide returns true if activity is mountain biking.
func IsMTBRide(act *ActivityDetail) bool {
	return act.SportType == "MountainBikeRide" || act.SportType == "EMountainBikeRide"
}

// IsVirtualRide returns true if activity is an indoor virtual ride (Zwift, TrainerRoad, etc.).
func IsVirtualRide(act *ActivityDetail) bool {
	return act.SportType == "VirtualRide" || act.Type == "VirtualRide" || strings.Contains(strings.ToLower(act.Name), "zwift")
}

// IsEBikeRide returns true if activity is an e-bike ride.
func IsEBikeRide(act *ActivityDetail) bool {
	return act.SportType == "EBikeRide"
}

// IsRoadRide returns true if ride is standard outdoor road ride.
func IsRoadRide(act *ActivityDetail) bool {
	return IsRideActivity(act) && !IsGravelRide(act) && !IsMTBRide(act) && !IsVirtualRide(act) && !IsEBikeRide(act)
}

// IsDownhillSki returns true if activity is resort downhill skiing or snowboarding.
func IsDownhillSki(act *ActivityDetail) bool {
	st := strings.ToLower(act.SportType)
	t := strings.ToLower(act.Type)
	return st == "alpineski" || t == "alpineski" ||
		st == "snowboard" || t == "snowboard"
}

// IsSkiTour returns true if activity is backcountry ski touring (skimo / uphill skinning) or nordic skiing.
// Downhill resort skiing (AlpineSki, Snowboard) is strictly excluded.
func IsSkiTour(act *ActivityDetail) bool {
	if IsDownhillSki(act) {
		return false
	}
	st := strings.ToLower(act.SportType)
	t := strings.ToLower(act.Type)
	return st == "backcountryski" || t == "backcountryski" ||
		st == "nordicski" || t == "nordicski" ||
		st == "rollerski" || t == "rollerski"
}

// CalculatePeriodicStats reads activity data (using cache) and computes periodic summary statistics.
func CalculatePeriodicStats(dir string, opts StatsOptions) ([]*PeriodStats, *PeriodStats, error) {
	store := GetGlobalStore(dir)
	periods, total, _, _, err := store.CalculateStats(opts)
	return periods, total, err
}

// CalculatePeriodicStatsFromActivities processes an in-memory slice of activities.
func CalculatePeriodicStatsFromActivities(activities []*ActivityDetail, opts StatsOptions) ([]*PeriodStats, *PeriodStats, []string) {
	if opts.Period == "" {
		opts.Period = "month"
	}
	opts.Period = strings.ToLower(opts.Period)
	if opts.SportType == "" {
		opts.SportType = "cardio"
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

	for _, act := range activities {
		if act == nil {
			continue
		}

		// Downhill resort skiing and snowboarding are ignored (not an accurate measure of endurance fitness)
		if IsDownhillSki(act) {
			continue
		}

		// Filter by Year
		if opts.Year > 0 && act.StartDate.Year() != opts.Year {
			continue
		}

		match := false
		category := ""

		isRun := IsRunActivity(act)
		isTrail := isRun && IsTrailRun(act)
		isTreadmill := isRun && IsTreadmillRun(act)
		isRoad := isRun && !isTrail && !isTreadmill

		isRide := IsRideActivity(act)
		isGravel := isRide && IsGravelRide(act)
		isMTB := isRide && IsMTBRide(act)
		isVirtualRide := isRide && IsVirtualRide(act)
		isEBike := isRide && IsEBikeRide(act)

		isSkiTour := IsSkiTour(act)
		isHike := act.Type == "Hike" || act.SportType == "Hike"
		isWalk := act.Type == "Walk" || act.SportType == "Walk"

		switch opts.SportType {
		case "cardio", "all_cardio":
			if isRun || isHike || isWalk {
				match = true
				category = "Running"
			} else if isRide {
				match = true
				category = "Cycling"
			} else if isSkiTour {
				match = true
				category = "Backcountry Skiing"
			}
		case "run":
			if isRun {
				match = true
				if isTrail {
					category = "Trail"
				} else if isTreadmill {
					category = "Treadmill"
				} else {
					category = "Road"
				}
			}
		case "trail":
			if isTrail {
				match = true
				category = "Trail"
			}
		case "road":
			if isRoad {
				match = true
				category = "Road"
			}
		case "treadmill":
			if isTreadmill {
				match = true
				category = "Treadmill"
			}
		case "ride", "bike", "cycling":
			if isRide {
				match = true
				if isGravel {
					category = "Gravel"
				} else if isMTB {
					category = "MTB"
				} else if isVirtualRide {
					category = "Virtual"
				} else if isEBike {
					category = "E-Bike"
				} else {
					category = "Road"
				}
			}
		case "gravel":
			if isGravel {
				match = true
				category = "Gravel"
			}
		case "mtb":
			if isMTB {
				match = true
				category = "MTB"
			}
		case "virtual_ride":
			if isVirtualRide {
				match = true
				category = "Virtual"
			}
		case "ski_tour", "ski":
			if isSkiTour {
				match = true
				category = "Ski Touring"
			}
		case "hike", "walk":
			if isHike || isWalk {
				match = true
				if isHike {
					category = "Hiking"
				} else {
					category = "Walking"
				}
			}
		case "all":
			match = true
			if isTrail {
				category = "Trail Run"
			} else if isTreadmill {
				category = "Treadmill Run"
			} else if isRoad {
				category = "Road Run"
			} else if isGravel {
				category = "Gravel Cycling"
			} else if isMTB {
				category = "Mountain Biking"
			} else if isVirtualRide {
				category = "Virtual Cycling"
			} else if isEBike {
				category = "E-Bike"
			} else if IsRoadRide(act) {
				category = "Road Cycling"
			} else if isSkiTour {
				category = "Backcountry Skiing"
			} else if isHike {
				category = "Hiking"
			} else if isWalk {
				category = "Walking"
			} else {
				category = act.SportType
				if category == "" {
					category = act.Type
				}
				if category == "" {
					category = "Other"
				}
			}
		default:
			if strings.EqualFold(act.Type, opts.SportType) || strings.EqualFold(act.SportType, opts.SportType) {
				match = true
				category = act.SportType
				if category == "" {
					category = act.Type
				}
			}
		}

		if !match {
			continue
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

		p.add(act)
		totalStats.add(act)

		if category != "" {
			if p.Breakdowns == nil {
				p.Breakdowns = make(map[string]*SubBreakdown)
			}
			bd, exists := p.Breakdowns[category]
			if !exists {
				bd = &SubBreakdown{Name: category}
				p.Breakdowns[category] = bd
			}
			bd.add(act)

			if totalStats.Breakdowns == nil {
				totalStats.Breakdowns = make(map[string]*SubBreakdown)
			}
			totBd, exists := totalStats.Breakdowns[category]
			if !exists {
				totBd = &SubBreakdown{Name: category}
				totalStats.Breakdowns[category] = totBd
			}
			totBd.add(act)
		}

		if isRun {
			if isTrail {
				p.Trail.add(act)
				totalStats.Trail.add(act)
			} else {
				p.Road.add(act)
				totalStats.Road.add(act)
			}
		}
	}

	// Fill missing periods (gaps) with zero periods so bar graphs are visually continuous
	if opts.Year > 0 {
		switch opts.Period {
		case "year":
			key := fmt.Sprintf("%d", opts.Year)
			if _, exists := periodsMap[key]; !exists {
				p := newPeriodStats(key, key, time.Date(opts.Year, 1, 1, 0, 0, 0, 0, time.UTC))
				p.Road = newPeriodStats("ROAD", "Road", p.SortTime)
				p.Trail = newPeriodStats("TRAIL", "Trail", p.SortTime)
				periodsMap[key] = p
			}
		case "week":
			d := time.Date(opts.Year, 1, 1, 0, 0, 0, 0, time.UTC)
			startWeek := d.AddDate(0, 0, -int((d.Weekday()+6)%7))
			dEnd := time.Date(opts.Year, 12, 31, 0, 0, 0, 0, time.UTC)
			endWeek := dEnd.AddDate(0, 0, -int((dEnd.Weekday()+6)%7))
			for curr := startWeek; !curr.After(endWeek); curr = curr.AddDate(0, 0, 7) {
				year, week := curr.ISOWeek()
				key := fmt.Sprintf("%04d-W%02d", year, week)
				endOfWeek := curr.AddDate(0, 0, 6)
				label := fmt.Sprintf("%s (%s - %s)", key, curr.Format("Jan 02"), endOfWeek.Format("Jan 02"))
				if _, exists := periodsMap[key]; !exists {
					p := newPeriodStats(key, label, curr)
					p.Road = newPeriodStats("ROAD", "Road", curr)
					p.Trail = newPeriodStats("TRAIL", "Trail", curr)
					periodsMap[key] = p
				}
			}
		case "month":
			fallthrough
		default:
			for m := 1; m <= 12; m++ {
				curr := time.Date(opts.Year, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
				key := curr.Format("2006-01")
				if _, exists := periodsMap[key]; !exists {
					p := newPeriodStats(key, key, curr)
					p.Road = newPeriodStats("ROAD", "Road", curr)
					p.Trail = newPeriodStats("TRAIL", "Trail", curr)
					periodsMap[key] = p
				}
			}
		}
	} else if len(periodsMap) > 0 {
		// All Years (opts.Year == 0): fill gaps between earliest and latest recorded periods
		var minSortTime, maxSortTime time.Time
		first := true
		for _, p := range periodsMap {
			if first || p.SortTime.Before(minSortTime) {
				minSortTime = p.SortTime
			}
			if first || p.SortTime.After(maxSortTime) {
				maxSortTime = p.SortTime
			}
			first = false
		}

		switch opts.Period {
		case "year":
			minYear := minSortTime.Year()
			maxYear := maxSortTime.Year()
			for y := minYear; y <= maxYear; y++ {
				key := fmt.Sprintf("%d", y)
				if _, exists := periodsMap[key]; !exists {
					t := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
					p := newPeriodStats(key, key, t)
					p.Road = newPeriodStats("ROAD", "Road", t)
					p.Trail = newPeriodStats("TRAIL", "Trail", t)
					periodsMap[key] = p
				}
			}
		case "week":
			startWeek := minSortTime.AddDate(0, 0, -int((minSortTime.Weekday()+6)%7))
			endWeek := maxSortTime.AddDate(0, 0, -int((maxSortTime.Weekday()+6)%7))
			for curr := startWeek; !curr.After(endWeek); curr = curr.AddDate(0, 0, 7) {
				year, week := curr.ISOWeek()
				key := fmt.Sprintf("%04d-W%02d", year, week)
				endOfWeek := curr.AddDate(0, 0, 6)
				label := fmt.Sprintf("%s (%s - %s)", key, curr.Format("Jan 02"), endOfWeek.Format("Jan 02"))
				if _, exists := periodsMap[key]; !exists {
					p := newPeriodStats(key, label, curr)
					p.Road = newPeriodStats("ROAD", "Road", curr)
					p.Trail = newPeriodStats("TRAIL", "Trail", curr)
					periodsMap[key] = p
				}
			}
		case "month":
			fallthrough
		default:
			startMonth := time.Date(minSortTime.Year(), minSortTime.Month(), 1, 0, 0, 0, 0, time.UTC)
			endMonth := time.Date(maxSortTime.Year(), maxSortTime.Month(), 1, 0, 0, 0, 0, time.UTC)
			for curr := startMonth; !curr.After(endMonth); curr = curr.AddDate(0, 1, 0) {
				key := curr.Format("2006-01")
				if _, exists := periodsMap[key]; !exists {
					p := newPeriodStats(key, key, curr)
					p.Road = newPeriodStats("ROAD", "Road", curr)
					p.Trail = newPeriodStats("TRAIL", "Trail", curr)
					periodsMap[key] = p
				}
			}
		}
	}

	// Sort periods chronologically
	var periods []*PeriodStats
	for _, p := range periodsMap {
		periods = append(periods, p)
	}
	sort.Slice(periods, func(i, j int) bool {
		return periods[i].SortTime.Before(periods[j].SortTime)
	})

	// Determine ordered list of categories to display
	var categories []string
	switch opts.SportType {
	case "run":
		categories = []string{"Road", "Trail", "Treadmill"}
	case "ride", "bike", "cycling":
		categories = []string{"Road", "Gravel", "MTB", "Virtual", "E-Bike"}
	case "all":
		for cat := range totalStats.Breakdowns {
			categories = append(categories, cat)
		}
		sort.Strings(categories)
	default:
		for cat := range totalStats.Breakdowns {
			categories = append(categories, cat)
		}
		sort.Strings(categories)
	}

	return periods, totalStats, categories
}

// GetAvailableYears returns a sorted descending list of distinct years with activities.
func GetAvailableYears(dir string) ([]int, error) {
	return GetGlobalStore(dir).GetAvailableYears()
}

// GenerateStatsReport reads all activity files and displays periodic summary statistics.
func GenerateStatsReport(dir string, opts StatsOptions) error {
	periods, totalStats, err := CalculatePeriodicStats(dir, opts)
	if err != nil {
		return err
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
		if p.Count == 0 {
			continue
		}
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
