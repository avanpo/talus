package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const hrZonesFilename = "hr-zones.json"

// HRZone represents one heart rate training zone with inclusive BPM bounds.
type HRZone struct {
	Zone int    `json:"zone"`
	Name string `json:"name"`
	Min  int    `json:"min"`
	Max  int    `json:"max"`
}

// SavedHRZonesConfig stores user-customized HR zones per sport category.
type SavedHRZonesConfig struct {
	ZonesBySport map[string][]HRZone `json:"zones_by_sport"`
}

// ActivityHRSummary holds compact heart rate telemetry extracted from an activity.
type ActivityHRSummary struct {
	BPMSeconds   map[int]int
	TotalSeconds int
	Peak30sHR    float64
	Peak20mHR    float64
	MaxHR        float64
	HasStream    bool
}

// HRZoneEstimationMeta describes how the 5-zone model was estimated from the data.
type HRZoneEstimationMeta struct {
	Model                 string `json:"model"` // "lthr" or "hrmax"
	EstimatedHRMax        int    `json:"estimated_hr_max"`
	EstimatedLTHR         int    `json:"estimated_lthr,omitempty"`
	ActivitiesAnalyzed    int    `json:"activities_analyzed"`
	ActivitiesWithStreams int    `json:"activities_with_streams"`
	TotalHRSeconds        int    `json:"total_hr_seconds"`
	UsedDefaultHRMax      bool   `json:"used_default_hr_max"`
}

// ZoneDistributionItem holds the time and percentage spent in a single HR zone.
type ZoneDistributionItem struct {
	Zone    int     `json:"zone"`
	Name    string  `json:"name"`
	Min     int     `json:"min"`
	Max     int     `json:"max"`
	Seconds int     `json:"seconds"`
	Percent float64 `json:"percent"`
}

var defaultZoneNames = []string{
	"Zone 1: Recovery",
	"Zone 2: Aerobic",
	"Zone 3: Tempo",
	"Zone 4: Threshold",
	"Zone 5: VO2 Max",
}

// IsValidHRSport reports whether sport is a supported HR zones filter.
func IsValidHRSport(sport string) bool {
	return sport == "all" || sport == "running" || sport == "cycling"
}

// NormalizeHRSport normalizes the sport query parameter to "all", "running", or "cycling".
func NormalizeHRSport(sport string) string {
	s := strings.ToLower(strings.TrimSpace(sport))
	if IsValidHRSport(s) {
		return s
	}
	return "all"
}

// LoadHRZones reads saved custom HR zones from dir.
func LoadHRZones(dir string) (*SavedHRZonesConfig, error) {
	path := filepath.Join(dir, hrZonesFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &SavedHRZonesConfig{ZonesBySport: make(map[string][]HRZone)}, nil
		}
		return nil, err
	}
	var cfg SavedHRZonesConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.ZonesBySport == nil {
		cfg.ZonesBySport = make(map[string][]HRZone)
	}
	return &cfg, nil
}

// LoadHRZonesWithFallback tries loading saved HR zones from configDir, falling back to fallbackDir.
func LoadHRZonesWithFallback(configDir, fallbackDir string) (*SavedHRZonesConfig, error) {
	cfg, err := LoadHRZones(configDir)
	if err == nil && len(cfg.ZonesBySport) > 0 {
		return cfg, nil
	}
	if fallbackDir != "" && fallbackDir != configDir {
		if fallbackCfg, err2 := LoadHRZones(fallbackDir); err2 == nil && len(fallbackCfg.ZonesBySport) > 0 {
			return fallbackCfg, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// SaveHRZones writes saved custom HR zones to dir.
func SaveHRZones(dir string, cfg *SavedHRZonesConfig) error {
	if cfg == nil {
		cfg = &SavedHRZonesConfig{ZonesBySport: make(map[string][]HRZone)}
	}
	if cfg.ZonesBySport == nil {
		cfg.ZonesBySport = make(map[string][]HRZone)
	}
	path := filepath.Join(dir, hrZonesFilename)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// computeActivityHRSummary extracts a compact BPM histogram and sustained peak HR metrics.
func computeActivityHRSummary(act *ActivityDetail) *ActivityHRSummary {
	if act == nil {
		return nil
	}

	summary := &ActivityHRSummary{
		BPMSeconds: make(map[int]int),
	}

	if act.MaxHeartrate >= 30 && act.MaxHeartrate <= 230 {
		summary.MaxHR = act.MaxHeartrate
	}

	if act.Streams != nil {
		if hrStream, okHR := act.Streams["heartrate"]; okHR && len(hrStream.Data) > 0 {
			hr := getFloatSlice(hrStream.Data)
			var t []float64
			if timeStream, okTime := act.Streams["time"]; okTime && len(timeStream.Data) == len(hr) {
				t = getFloatSlice(timeStream.Data)
			}
			var moving []bool
			if movingStream, okMov := act.Streams["moving"]; okMov && len(movingStream.Data) == len(hr) {
				moving = make([]bool, len(movingStream.Data))
				for i, v := range movingStream.Data {
					if b, ok := v.(bool); ok {
						moving[i] = b
					} else {
						moving[i] = true
					}
				}
			}

			summary.HasStream = true

			// Sliding window state for 30s and 20m sustained peak HR
			type hrSample struct {
				time float64
				hr   float64
				dt   float64
			}
			var samples []hrSample

			for i, bpmVal := range hr {
				dt := 1.0
				if len(t) == len(hr) && i > 0 {
					diff := t[i] - t[i-1]
					if diff <= 0 {
						dt = 1.0
					} else if diff > 15 {
						// Watch pause or GPS gap: avoid inflating a single HR sample across a long pause
						dt = 1.0
					} else {
						dt = diff
					}
				}

				if len(moving) == len(hr) && !moving[i] && len(t) == len(hr) && i > 0 && (t[i]-t[i-1]) > 5 {
					continue
				}

				if bpmVal < 30 || bpmVal > 230 {
					continue
				}

				dtInt := int(math.Round(dt))
				if dtInt < 1 {
					dtInt = 1
				}
				bpmInt := int(math.Round(bpmVal))
				summary.BPMSeconds[bpmInt] += dtInt
				summary.TotalSeconds += dtInt

				curT := float64(i)
				if len(t) == len(hr) {
					curT = t[i]
				}
				samples = append(samples, hrSample{time: curT, hr: bpmVal, dt: dt})
			}

			// Compute sustained 30s and 20m peak rolling averages
			if len(samples) > 0 {
				var sum30, dur30 float64
				left30 := 0
				var sum20, dur20 float64
				left20 := 0

				for right := 0; right < len(samples); right++ {
					// Reset windows across large time gaps (> 30s)
					if right > 0 && (samples[right].time-samples[right-1].time) > 30 {
						left30 = right
						sum30 = 0
						dur30 = 0
						left20 = right
						sum20 = 0
						dur20 = 0
					}

					w := samples[right].dt
					sum30 += samples[right].hr * w
					dur30 += w
					for left30 < right && (samples[right].time-samples[left30].time) > 30 && dur30-samples[left30].dt >= 30 {
						sum30 -= samples[left30].hr * samples[left30].dt
						dur30 -= samples[left30].dt
						left30++
					}
					if dur30 >= 30 && (samples[right].time-samples[left30].time) >= 25 {
						avg := sum30 / dur30
						if avg > summary.Peak30sHR {
							summary.Peak30sHR = avg
						}
					}

					sum20 += samples[right].hr * w
					dur20 += w
					for left20 < right && (samples[right].time-samples[left20].time) > 1200 && dur20-samples[left20].dt >= 1200 {
						sum20 -= samples[left20].hr * samples[left20].dt
						dur20 -= samples[left20].dt
						left20++
					}
					if dur20 >= 1200 && (samples[right].time-samples[left20].time) >= 1140 {
						avg := sum20 / dur20
						if avg > summary.Peak20mHR {
							summary.Peak20mHR = avg
						}
					}
				}
			}

			if summary.TotalSeconds > 0 {
				return summary
			}
		}
	}

	// Fallback when no heartrate stream is available
	if act.AverageHeartrate >= 30 && act.AverageHeartrate <= 230 && act.MovingTime > 0 {
		bpmInt := int(math.Round(act.AverageHeartrate))
		summary.BPMSeconds[bpmInt] = act.MovingTime
		summary.TotalSeconds = act.MovingTime
		return summary
	}

	return nil
}

// EstimateHRZones constructs a 5-zone running heart rate model from activity summaries.
func EstimateHRZones(summaries []*ActivityHRSummary) ([]HRZone, HRZoneEstimationMeta) {
	var meta HRZoneEstimationMeta
	var maxPeak30s, maxPeak20m, maxObserved float64

	for _, s := range summaries {
		if s == nil {
			continue
		}
		meta.ActivitiesAnalyzed++
		if s.HasStream {
			meta.ActivitiesWithStreams++
		}
		meta.TotalHRSeconds += s.TotalSeconds
		if s.Peak30sHR > maxPeak30s {
			maxPeak30s = s.Peak30sHR
		}
		if s.Peak20mHR > maxPeak20m {
			maxPeak20m = s.Peak20mHR
		}
		if s.MaxHR > maxObserved {
			maxObserved = s.MaxHR
		}
	}

	hrMax := 0
	if maxPeak30s >= 120 && maxPeak30s <= 225 {
		hrMax = int(math.Round(maxPeak30s))
	} else if maxObserved >= 120 && maxObserved <= 225 {
		hrMax = int(math.Round(maxObserved))
	} else {
		hrMax = 190
		meta.UsedDefaultHRMax = true
	}
	meta.EstimatedHRMax = hrMax

	if maxPeak20m >= 100 {
		lthr := int(math.Round(0.95 * maxPeak20m))
		minLTHR := int(math.Round(0.78 * float64(hrMax)))
		maxLTHR := int(math.Round(0.94 * float64(hrMax)))
		if lthr < minLTHR {
			lthr = minLTHR
		}
		if lthr > maxLTHR {
			lthr = maxLTHR
		}
		meta.EstimatedLTHR = lthr
	}

	z1Min := int(math.Round(0.50 * float64(hrMax)))
	if meta.EstimatedLTHR > 0 {
		// Joe Friel LTHR-anchored 5-zone running model (compresses upper zones for fit athletes):
		// Z1: < 85% LTHR, Z2: 85-89% LTHR, Z3: 90-94% LTHR, Z4: 95-99% LTHR, Z5: >= 100% LTHR
		meta.Model = "lthr"
		lthr := float64(meta.EstimatedLTHR)
		z2Min := int(math.Round(0.85 * lthr))
		z3Min := int(math.Round(0.90 * lthr))
		z4Min := int(math.Round(0.95 * lthr))
		z5Min := meta.EstimatedLTHR
		if z1Min >= z2Min {
			z1Min = z2Min - 10
		}

		zones := []HRZone{
			{Zone: 1, Name: defaultZoneNames[0], Min: z1Min, Max: z2Min - 1},
			{Zone: 2, Name: defaultZoneNames[1], Min: z2Min, Max: z3Min - 1},
			{Zone: 3, Name: defaultZoneNames[2], Min: z3Min, Max: z4Min - 1},
			{Zone: 4, Name: defaultZoneNames[3], Min: z4Min, Max: z5Min - 1},
			{Zone: 5, Name: defaultZoneNames[4], Min: z5Min, Max: hrMax},
		}
		return zones, meta
	}

	// Fallback %HRmax 5-zone model when no >=20m stream effort is available:
	// Z1: 50-60%, Z2: 60-70%, Z3: 70-80%, Z4: 80-90%, Z5: 90-100%
	meta.Model = "hrmax"
	z1Max := int(math.Round(0.60 * float64(hrMax)))
	z2Max := int(math.Round(0.70 * float64(hrMax)))
	z3Max := int(math.Round(0.80 * float64(hrMax)))
	z4Max := int(math.Round(0.90 * float64(hrMax)))
	z5Max := hrMax

	zones := []HRZone{
		{Zone: 1, Name: defaultZoneNames[0], Min: z1Min, Max: z1Max},
		{Zone: 2, Name: defaultZoneNames[1], Min: z1Max + 1, Max: z2Max},
		{Zone: 3, Name: defaultZoneNames[2], Min: z2Max + 1, Max: z3Max},
		{Zone: 4, Name: defaultZoneNames[3], Min: z3Max + 1, Max: z4Max},
		{Zone: 5, Name: defaultZoneNames[4], Min: z4Max + 1, Max: z5Max},
	}

	return zones, meta
}

// ValidateHRZones validates a slice of 5 HR zones and normalizes their zone numbers/names if empty.
func ValidateHRZones(zones []HRZone) ([]HRZone, error) {
	if len(zones) != 5 {
		return nil, fmt.Errorf("expected exactly 5 heart rate zones, got %d", len(zones))
	}

	out := make([]HRZone, 5)
	for i, z := range zones {
		if z.Min < 30 || z.Max > 250 || z.Min > z.Max {
			return nil, fmt.Errorf("zone %d has invalid BPM range (%d-%d)", i+1, z.Min, z.Max)
		}
		if i > 0 && z.Min <= out[i-1].Min {
			return nil, fmt.Errorf("zone %d min (%d) must be greater than zone %d min (%d)", i+1, z.Min, i, out[i-1].Min)
		}
		name := strings.TrimSpace(z.Name)
		if name == "" {
			name = defaultZoneNames[i]
		}
		out[i] = HRZone{
			Zone: i + 1,
			Name: name,
			Min:  z.Min,
			Max:  z.Max,
		}
	}
	return out, nil
}

// ComputeZoneDistribution calculates time and percentage spent in each of the 5 HR zones.
func ComputeZoneDistribution(histogram map[int]int, zones []HRZone) []ZoneDistributionItem {
	dist := make([]ZoneDistributionItem, len(zones))
	for i, z := range zones {
		dist[i] = ZoneDistributionItem{
			Zone: z.Zone,
			Name: z.Name,
			Min:  z.Min,
			Max:  z.Max,
		}
	}
	if len(zones) != 5 {
		return dist
	}

	totalSecs := 0
	for bpm, secs := range histogram {
		if secs <= 0 || bpm < 30 || bpm > 250 {
			continue
		}
		totalSecs += secs
		switch {
		case bpm <= zones[0].Max:
			dist[0].Seconds += secs
		case bpm <= zones[1].Max:
			dist[1].Seconds += secs
		case bpm <= zones[2].Max:
			dist[2].Seconds += secs
		case bpm <= zones[3].Max:
			dist[3].Seconds += secs
		default:
			dist[4].Seconds += secs
		}
	}

	if totalSecs > 0 {
		for i := range dist {
			dist[i].Percent = math.Round((float64(dist[i].Seconds)/float64(totalSecs))*1000) / 10
		}
	}

	return dist
}
