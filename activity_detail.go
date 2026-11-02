package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type SplitItemJSON struct {
	Split               int     `json:"split"`
	Distance            float64 `json:"distance"`
	DistanceFmt         string  `json:"distance_fmt"`
	ElapsedTime         int     `json:"elapsed_time"`
	MovingTime          int     `json:"moving_time"`
	MovingTimeFmt       string  `json:"moving_time_fmt"`
	Pace                string  `json:"pace"`
	ElevationDiff       float64 `json:"elevation_diff"`
	ElevationDiffFmt    string  `json:"elevation_diff_fmt"`
	AverageHeartrate    float64 `json:"average_heartrate"`
}

type SegmentEffortItemJSON struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	SegmentID        int64   `json:"segment_id"`
	Distance         float64 `json:"distance"`
	DistanceFmt      string  `json:"distance_fmt"`
	MovingTime       int     `json:"moving_time"`
	MovingTimeFmt    string  `json:"moving_time_fmt"`
	ElapsedTime      int     `json:"elapsed_time"`
	ElapsedTimeFmt   string  `json:"elapsed_time_fmt"`
	Pace             string  `json:"pace"`
	AverageGrade     float64 `json:"average_grade"`
	AverageGradeFmt  string  `json:"average_grade_fmt"`
	AverageHeartrate float64 `json:"average_heartrate"`
	MaxHeartrate     float64 `json:"max_heartrate"`
	PRRank           *int    `json:"pr_rank"`
}

type BestEffortItemJSON struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Distance    float64 `json:"distance"`
	DistanceFmt string  `json:"distance_fmt"`
	Time        int     `json:"time"`
	TimeFmt     string  `json:"time_fmt"`
	PRRank      *int    `json:"pr_rank"`
}

type LapItemJSON struct {
	LapIndex         int     `json:"lap_index"`
	Name             string  `json:"name"`
	Distance         float64 `json:"distance"`
	DistanceFmt      string  `json:"distance_fmt"`
	MovingTime       int     `json:"moving_time"`
	MovingTimeFmt    string  `json:"moving_time_fmt"`
	ElapsedTime      int     `json:"elapsed_time"`
	ElapsedTimeFmt   string  `json:"elapsed_time_fmt"`
	PaceOrSpeed      string  `json:"pace_or_speed"`
	AverageHeartrate float64 `json:"average_heartrate"`
	Elevation        float64 `json:"elevation"`
	ElevationFmt     string  `json:"elevation_fmt"`
}

type TelemetryStreamsJSON struct {
	Points    int       `json:"points"`
	Distance  []float64 `json:"distance"`
	Altitude  []float64 `json:"altitude"`
	Heartrate []float64 `json:"heartrate,omitempty"`
	Speed     []float64 `json:"speed,omitempty"`
	Pace      []float64 `json:"pace,omitempty"`
	Cadence   []float64 `json:"cadence,omitempty"`
	Watts     []float64 `json:"watts,omitempty"`
	Time      []int     `json:"time"`
}

type ActivityDetailResponseJSON struct {
	ID                   int64                   `json:"id"`
	Name                 string                  `json:"name"`
	Description          string                  `json:"description"`
	Type                 string                  `json:"type"`
	SportType            string                  `json:"sport_type"`
	StartDate            string                  `json:"start_date"`
	StartDateFmt         string                  `json:"start_date_fmt"`
	StartDateFull        string                  `json:"start_date_full"`
	Unit                 string                  `json:"unit"`
	Distance             float64                 `json:"distance"`
	DistanceFmt          string                  `json:"distance_fmt"`
	MovingTime           int                     `json:"moving_time"`
	MovingTimeFmt        string                  `json:"moving_time_fmt"`
	ElapsedTime          int                     `json:"elapsed_time"`
	ElapsedTimeFmt       string                  `json:"elapsed_time_fmt"`
	Elevation            float64                 `json:"elevation"`
	ElevationFmt         string                  `json:"elevation_fmt"`
	ElevHigh             float64                 `json:"elev_high"`
	ElevHighFmt          string                  `json:"elev_high_fmt"`
	ElevLow              float64                 `json:"elev_low"`
	ElevLowFmt           string                  `json:"elev_low_fmt"`
	AvgPaceFmt           string                  `json:"avg_pace_fmt"`
	AvgSpeedFmt          string                  `json:"avg_speed_fmt"`
	MaxPaceFmt           string                  `json:"max_pace_fmt"`
	MaxSpeedFmt          string                  `json:"max_speed_fmt"`
	AverageHeartrate     float64                 `json:"average_heartrate"`
	MaxHeartrate         float64                 `json:"max_heartrate"`
	AverageCadence       float64                 `json:"average_cadence"`
	AverageWatts         float64                 `json:"average_watts"`
	MaxWatts             float64                 `json:"max_watts"`
	WeightedAverageWatts float64                 `json:"weighted_average_watts"`
	Kilojoules           float64                 `json:"kilojoules"`
	Calories             float64                 `json:"calories"`
	EfficiencyFactor     float64                 `json:"efficiency_factor"`
	EfficiencyFactorFmt  string                  `json:"efficiency_factor_fmt"`
	AverageTemp          float64                 `json:"average_temp"`
	AverageTempFmt       string                  `json:"average_temp_fmt"`
	DeviceName           string                  `json:"device_name"`
	GearName             string                  `json:"gear_name"`
	GearID               string                  `json:"gear_id"`
	Shoe                 string                  `json:"shoe"`
	ShoeRelevant         bool                    `json:"shoe_relevant"`
	StravaURL            string                  `json:"strava_url"`
	Polyline             string                  `json:"polyline"`
	Coordinates          [][2]float64            `json:"coordinates"`
	Splits               []SplitItemJSON         `json:"splits"`
	Segments             []SegmentEffortItemJSON `json:"segments"`
	BestEfforts          []BestEffortItemJSON    `json:"best_efforts"`
	Laps                 []LapItemJSON           `json:"laps"`
	Streams              *TelemetryStreamsJSON   `json:"streams,omitempty"`
}

func downsampleFloatSlice(s []float64, maxPoints int) []float64 {
	if len(s) <= maxPoints || maxPoints <= 0 {
		return s
	}
	res := make([]float64, maxPoints)
	n := len(s)
	for i := 0; i < maxPoints; i++ {
		idx := (i * (n - 1)) / (maxPoints - 1)
		res[i] = s[idx]
	}
	return res
}

func downsampleIntSlice(s []int, maxPoints int) []int {
	if len(s) <= maxPoints || maxPoints <= 0 {
		return s
	}
	res := make([]int, maxPoints)
	n := len(s)
	for i := 0; i < maxPoints; i++ {
		idx := (i * (n - 1)) / (maxPoints - 1)
		res[i] = s[idx]
	}
	return res
}

func handleActivityDetailAPI(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimSpace(r.URL.Query().Get("id"))
	if idStr == "" {
		http.Error(w, "Missing 'id' query parameter", http.StatusBadRequest)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "Invalid 'id' query parameter", http.StatusBadRequest)
		return
	}

	unit := strings.TrimSpace(r.URL.Query().Get("unit"))
	if unit == "" {
		unit = getSystemUnit()
	}

	store := GetGlobalStore(daemon.dataDir)
	act, err := store.GetActivityDetailByID(id)
	if err != nil {
		http.Error(w, fmt.Sprintf("Activity %d not found: %v", id, err), http.StatusNotFound)
		return
	}

	var dist float64
	var distFmt string
	var elev float64
	var elevFmt string
	var elevHigh float64
	var elevHighFmt string
	var elevLow float64
	var elevLowFmt string

	distScale := 1000.0 // meters per km
	distUnit := "km"
	elevUnit := "m"
	paceUnit := "/km"
	speedUnit := "km/h"

	if unit == "mi" {
		distScale = 1609.344
		distUnit = "mi"
		elevUnit = "ft"
		paceUnit = "/mi"
		speedUnit = "mph"

		dist = act.Distance / 1609.344
		distFmt = fmt.Sprintf("%.2f mi", dist)
		elev = act.TotalElevationGain * 3.28084
		elevFmt = fmt.Sprintf("%s %s", formatComma(int(math.Round(elev))), elevUnit)
		if act.ElevHigh > 0 {
			elevHigh = act.ElevHigh * 3.28084
			elevHighFmt = fmt.Sprintf("%s %s", formatComma(int(math.Round(elevHigh))), elevUnit)
		}
		if act.ElevLow > 0 {
			elevLow = act.ElevLow * 3.28084
			elevLowFmt = fmt.Sprintf("%s %s", formatComma(int(math.Round(elevLow))), elevUnit)
		}
	} else {
		dist = act.Distance / 1000.0
		distFmt = fmt.Sprintf("%.2f km", dist)
		elev = act.TotalElevationGain
		elevFmt = fmt.Sprintf("%s %s", formatComma(int(math.Round(elev))), elevUnit)
		if act.ElevHigh > 0 {
			elevHigh = act.ElevHigh
			elevHighFmt = fmt.Sprintf("%s %s", formatComma(int(math.Round(elevHigh))), elevUnit)
		}
		if act.ElevLow > 0 {
			elevLow = act.ElevLow
			elevLowFmt = fmt.Sprintf("%s %s", formatComma(int(math.Round(elevLow))), elevUnit)
		}
	}

	elapsedSecs := act.ElapsedTime
	if elapsedSecs == 0 {
		elapsedSecs = act.MovingTime
	}

	movingSecs := act.MovingTime
	if movingSecs == 0 {
		movingSecs = act.ElapsedTime
	}

	// Pace and Speed calculation
	var avgPaceFmt, avgSpeedFmt, maxPaceFmt, maxSpeedFmt string
	if movingSecs > 0 && act.Distance > 0 {
		speedMMin := act.Distance / (float64(movingSecs) / 60.0)
		paceMin := distScale / speedMMin
		avgPaceFmt = formatPace(paceMin) + paceUnit

		speedVal := (act.Distance / distScale) / (float64(movingSecs) / 3600.0)
		avgSpeedFmt = fmt.Sprintf("%.1f %s", speedVal, speedUnit)
	}

	if act.MaxSpeed > 0 {
		var maxSpeedVal float64
		if unit == "mi" {
			maxSpeedVal = act.MaxSpeed * 2.23694
		} else {
			maxSpeedVal = act.MaxSpeed * 3.6
		}
		maxSpeedFmt = fmt.Sprintf("%.1f %s", maxSpeedVal, speedUnit)
		if maxSpeedVal > 0 {
			maxPaceFmt = formatPace(60.0/maxSpeedVal) + paceUnit
		}
	}

	// Efficiency Factor (EF)
	var ef float64
	var efFmt string
	if act.AverageHeartrate >= 80 && movingSecs >= 60 && act.Distance > 0 {
		effectiveDist := act.Distance
		if IsRunActivity(act) && act.TotalElevationGain > 0 {
			effectiveDist += (7.5 * act.TotalElevationGain)
		}
		effSpeedMMin := effectiveDist / (float64(movingSecs) / 60.0)
		ef = effSpeedMMin / act.AverageHeartrate
		efFmt = fmt.Sprintf("%.2f", ef)
	}

	// Temperature
	var tempFmt string
	if act.AverageTemp != 0 {
		if unit == "mi" {
			tempFmt = fmt.Sprintf("%.0f °F", act.AverageTemp*9.0/5.0+32.0)
		} else {
			tempFmt = fmt.Sprintf("%.0f °C", act.AverageTemp)
		}
	}

	// Polyline and GPS coordinates
	polyline := act.Map.Polyline
	if polyline == "" {
		polyline = act.Map.SummaryPolyline
	}
	var coords [][2]float64
	if polyline != "" {
		if decoded, err := decodePolyline(polyline); err == nil && len(decoded) > 0 {
			if len(decoded) > 800 {
				coords = downsamplePath(decoded, 600)
			} else {
				coords = decoded
			}
		}
	}

	// Splits
	var splitsList []SplitDetail
	if unit == "mi" && len(act.SplitsStandard) > 0 {
		splitsList = act.SplitsStandard
	} else if len(act.SplitsMetric) > 0 {
		splitsList = act.SplitsMetric
	} else if len(act.SplitsStandard) > 0 {
		splitsList = act.SplitsStandard
	}

	splitsJSON := make([]SplitItemJSON, 0, len(splitsList))
	for _, sp := range splitsList {
		var sDist float64
		var sDistFmt string
		var sElevDiff float64
		var sElevFmt string
		var sPace string

		if unit == "mi" {
			sDist = sp.Distance / 1609.344
			sDistFmt = fmt.Sprintf("%.2f mi", sDist)
			sElevDiff = sp.ElevationDifference * 3.28084
			if sElevDiff >= 0 {
				sElevFmt = fmt.Sprintf("+%d ft", int(math.Round(sElevDiff)))
			} else {
				sElevFmt = fmt.Sprintf("%d ft", int(math.Round(sElevDiff)))
			}
		} else {
			sDist = sp.Distance / 1000.0
			sDistFmt = fmt.Sprintf("%.2f km", sDist)
			sElevDiff = sp.ElevationDifference
			if sElevDiff >= 0 {
				sElevFmt = fmt.Sprintf("+%d m", int(math.Round(sElevDiff)))
			} else {
				sElevFmt = fmt.Sprintf("%d m", int(math.Round(sElevDiff)))
			}
		}

		sMovingSecs := sp.MovingTime
		if sMovingSecs == 0 {
			sMovingSecs = sp.ElapsedTime
		}

		if sMovingSecs > 0 && sp.Distance > 0 {
			sSpeedMMin := sp.Distance / (float64(sMovingSecs) / 60.0)
			sPaceMin := distScale / sSpeedMMin
			sPace = formatPace(sPaceMin) + paceUnit
		} else if sp.AverageSpeed > 0 {
			paceSecsPerMeter := 1.0 / sp.AverageSpeed
			paceMin := (paceSecsPerMeter * distScale) / 60.0
			sPace = formatPace(paceMin) + paceUnit
		}

		splitsJSON = append(splitsJSON, SplitItemJSON{
			Split:            sp.Split,
			Distance:         math.Round(sDist*100) / 100,
			DistanceFmt:      sDistFmt,
			ElapsedTime:      sp.ElapsedTime,
			MovingTime:       sMovingSecs,
			MovingTimeFmt:    formatDurationExact(sMovingSecs),
			Pace:             sPace,
			ElevationDiff:    math.Round(sElevDiff),
			ElevationDiffFmt: sElevFmt,
			AverageHeartrate: sp.AverageHeartrate,
		})
	}

	// Segment Efforts
	segmentsJSON := make([]SegmentEffortItemJSON, 0, len(act.SegmentEfforts))
	for _, eff := range act.SegmentEfforts {
		var effDist float64
		var effDistFmt string
		if unit == "mi" {
			effDist = eff.Distance / 1609.344
			effDistFmt = fmt.Sprintf("%.2f mi", effDist)
		} else {
			effDist = eff.Distance / 1000.0
			effDistFmt = fmt.Sprintf("%.2f km", effDist)
		}

		effTime := eff.MovingTime
		if effTime == 0 {
			effTime = eff.ElapsedTime
		}

		var effPace string
		if effTime > 0 && eff.Distance > 0 {
			effSpeedMMin := eff.Distance / (float64(effTime) / 60.0)
			effPaceMin := distScale / effSpeedMMin
			effPace = formatPace(effPaceMin) + paceUnit
		}

		segmentsJSON = append(segmentsJSON, SegmentEffortItemJSON{
			ID:               eff.ID,
			Name:             eff.Name,
			SegmentID:        eff.Segment.ID,
			Distance:         math.Round(effDist*100) / 100,
			DistanceFmt:      effDistFmt,
			MovingTime:       effTime,
			MovingTimeFmt:    formatDurationExact(effTime),
			ElapsedTime:      eff.ElapsedTime,
			ElapsedTimeFmt:   formatDurationExact(eff.ElapsedTime),
			Pace:             effPace,
			AverageGrade:     eff.Segment.AverageGrade,
			AverageGradeFmt:  fmt.Sprintf("%.1f%%", eff.Segment.AverageGrade),
			AverageHeartrate: eff.AverageHeartrate,
			MaxHeartrate:     eff.MaxHeartrate,
			PRRank:           eff.PRRank,
		})
	}

	// Best Efforts
	bestEffortsJSON := make([]BestEffortItemJSON, 0, len(act.BestEfforts))
	for _, be := range act.BestEfforts {
		var beDist float64
		var beDistFmt string
		if unit == "mi" {
			beDist = be.Distance / 1609.344
			beDistFmt = fmt.Sprintf("%.2f mi", beDist)
		} else {
			beDist = be.Distance / 1000.0
			beDistFmt = fmt.Sprintf("%.2f km", beDist)
		}

		beTime := be.MovingTime
		if beTime == 0 {
			beTime = be.ElapsedTime
		}

		bestEffortsJSON = append(bestEffortsJSON, BestEffortItemJSON{
			ID:          be.ID,
			Name:        be.Name,
			Distance:    math.Round(beDist*100) / 100,
			DistanceFmt: beDistFmt,
			Time:        beTime,
			TimeFmt:     formatDurationExact(beTime),
			PRRank:      be.PRRank,
		})
	}

	// Laps
	lapsJSON := make([]LapItemJSON, 0, len(act.Laps))
	for _, lap := range act.Laps {
		var lDist float64
		var lDistFmt string
		var lElev float64
		var lElevFmt string
		if unit == "mi" {
			lDist = lap.Distance / 1609.344
			lDistFmt = fmt.Sprintf("%.2f mi", lDist)
			lElev = lap.TotalElevationGain * 3.28084
			lElevFmt = fmt.Sprintf("%s ft", formatComma(int(math.Round(lElev))))
		} else {
			lDist = lap.Distance / 1000.0
			lDistFmt = fmt.Sprintf("%.2f km", lDist)
			lElev = lap.TotalElevationGain
			lElevFmt = fmt.Sprintf("%s m", formatComma(int(math.Round(lElev))))
		}

		lTime := lap.MovingTime
		if lTime == 0 {
			lTime = lap.ElapsedTime
		}

		var lPaceSpeed string
		if lTime > 0 && lap.Distance > 0 {
			speedVal := (lap.Distance / distScale) / (float64(lTime) / 3600.0)
			if IsRunActivity(act) {
				lSpeedMMin := lap.Distance / (float64(lTime) / 60.0)
				lPaceMin := distScale / lSpeedMMin
				lPaceSpeed = formatPace(lPaceMin) + paceUnit
			} else {
				lPaceSpeed = fmt.Sprintf("%.1f %s", speedVal, speedUnit)
			}
		}

		lapsJSON = append(lapsJSON, LapItemJSON{
			LapIndex:         lap.LapIndex,
			Name:             lap.Name,
			Distance:         math.Round(lDist*100) / 100,
			DistanceFmt:      lDistFmt,
			MovingTime:       lTime,
			MovingTimeFmt:    formatDurationExact(lTime),
			ElapsedTime:      lap.ElapsedTime,
			ElapsedTimeFmt:   formatDurationExact(lap.ElapsedTime),
			PaceOrSpeed:      lPaceSpeed,
			AverageHeartrate: lap.AverageHeartrate,
			Elevation:        lElev,
			ElevationFmt:     lElevFmt,
		})
	}

	// Streams processing
	var streamsJSON *TelemetryStreamsJSON
	if act.Streams != nil && len(act.Streams) > 0 {
		var rawDist, rawAlt, rawHR, rawVel, rawCad, rawWatts []float64
		var rawTime []int

		if ds, ok := act.Streams["distance"]; ok && len(ds.Data) > 0 {
			rawDist = getFloatSlice(ds.Data)
		}
		if as, ok := act.Streams["altitude"]; ok && len(as.Data) > 0 {
			rawAlt = getFloatSlice(as.Data)
		}
		if hs, ok := act.Streams["heartrate"]; ok && len(hs.Data) > 0 {
			rawHR = getFloatSlice(hs.Data)
		}
		if vs, ok := act.Streams["velocity_smooth"]; ok && len(vs.Data) > 0 {
			rawVel = getFloatSlice(vs.Data)
		}
		if cs, ok := act.Streams["cadence"]; ok && len(cs.Data) > 0 {
			rawCad = getFloatSlice(cs.Data)
		}
		if ws, ok := act.Streams["watts"]; ok && len(ws.Data) > 0 {
			rawWatts = getFloatSlice(ws.Data)
		}
		if ts, ok := act.Streams["time"]; ok && len(ts.Data) > 0 {
			for _, val := range ts.Data {
				switch v := val.(type) {
				case int:
					rawTime = append(rawTime, v)
				case float64:
					rawTime = append(rawTime, int(v))
				case json.Number:
					if i64, err := v.Int64(); err == nil {
						rawTime = append(rawTime, int(i64))
					}
				}
			}
		}

		if len(rawDist) > 0 && len(rawDist) == len(rawAlt) {
			maxStreamPoints := 250
			sDist := downsampleFloatSlice(rawDist, maxStreamPoints)
			sAlt := downsampleFloatSlice(rawAlt, maxStreamPoints)

			// Convert units
			for i := range sDist {
				if unit == "mi" {
					sDist[i] = math.Round((sDist[i]/1609.344)*100) / 100
				} else {
					sDist[i] = math.Round((sDist[i]/1000.0)*100) / 100
				}
			}
			for i := range sAlt {
				if unit == "mi" {
					sAlt[i] = math.Round(sAlt[i] * 3.28084)
				} else {
					sAlt[i] = math.Round(sAlt[i])
				}
			}

			streamsJSON = &TelemetryStreamsJSON{
				Points:   len(sDist),
				Distance: sDist,
				Altitude: sAlt,
			}

			if len(rawHR) == len(rawDist) {
				streamsJSON.Heartrate = downsampleFloatSlice(rawHR, maxStreamPoints)
			}
			if len(rawTime) == len(rawDist) {
				streamsJSON.Time = downsampleIntSlice(rawTime, maxStreamPoints)
			}
			if len(rawCad) == len(rawDist) {
				streamsJSON.Cadence = downsampleFloatSlice(rawCad, maxStreamPoints)
			}
			if len(rawWatts) == len(rawDist) {
				streamsJSON.Watts = downsampleFloatSlice(rawWatts, maxStreamPoints)
			}
			if len(rawVel) == len(rawDist) {
				velSampled := downsampleFloatSlice(rawVel, maxStreamPoints)
				speedSampled := make([]float64, len(velSampled))
				paceSampled := make([]float64, len(velSampled))
				for i, v := range velSampled {
					if v > 0 {
						if unit == "mi" {
							speedSampled[i] = math.Round(v*2.23694*10) / 10
							paceSampled[i] = math.Round((1609.344/v/60.0)*100) / 100
						} else {
							speedSampled[i] = math.Round(v*3.6*10) / 10
							paceSampled[i] = math.Round((1000.0/v/60.0)*100) / 100
						}
					}
				}
				streamsJSON.Speed = speedSampled
				streamsJSON.Pace = paceSampled
			}
		}
	}

	gearName := ""
	if act.Gear != nil && act.Gear.Name != "" {
		gearName = act.Gear.Name
	}

	resp := ActivityDetailResponseJSON{
		ID:                   act.ID,
		Name:                 act.Name,
		Description:          act.Description,
		Type:                 act.Type,
		SportType:            formatSportType(act.SportType, act.Type),
		StartDate:            act.StartDate.Format(time.RFC3339),
		StartDateFmt:         act.StartDate.Format("Jan 02, 2006"),
		StartDateFull:        act.StartDate.Format("Monday, Jan 02, 2006 at 3:04 PM"),
		Unit:                 distUnit,
		Distance:             math.Round(dist*100) / 100,
		DistanceFmt:          distFmt,
		MovingTime:           movingSecs,
		MovingTimeFmt:        formatDurationExact(movingSecs),
		ElapsedTime:          elapsedSecs,
		ElapsedTimeFmt:       formatDurationExact(elapsedSecs),
		Elevation:            math.Round(elev),
		ElevationFmt:         elevFmt,
		ElevHigh:             math.Round(elevHigh),
		ElevHighFmt:          elevHighFmt,
		ElevLow:              math.Round(elevLow),
		ElevLowFmt:           elevLowFmt,
		AvgPaceFmt:           avgPaceFmt,
		AvgSpeedFmt:          avgSpeedFmt,
		MaxPaceFmt:           maxPaceFmt,
		MaxSpeedFmt:          maxSpeedFmt,
		AverageHeartrate:     act.AverageHeartrate,
		MaxHeartrate:         act.MaxHeartrate,
		AverageCadence:       act.AverageCadence,
		AverageWatts:         act.AverageWatts,
		MaxWatts:             act.MaxWatts,
		WeightedAverageWatts: act.WeightedAverageWatts,
		Kilojoules:           act.Kilojoules,
		Calories:             act.Calories,
		EfficiencyFactor:     ef,
		EfficiencyFactorFmt:  efFmt,
		AverageTemp:          act.AverageTemp,
		AverageTempFmt:       tempFmt,
		DeviceName:           act.DeviceName,
		GearName:             gearName,
		GearID:               act.GearID,
		Shoe:                 act.ShoeName(),
		ShoeRelevant:         IsShoeRelevant(act),
		StravaURL:            fmt.Sprintf("https://www.strava.com/activities/%d", act.ID),
		Polyline:             polyline,
		Coordinates:          coords,
		Splits:               splitsJSON,
		Segments:             segmentsJSON,
		BestEfforts:          bestEffortsJSON,
		Laps:                 lapsJSON,
		Streams:              streamsJSON,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
