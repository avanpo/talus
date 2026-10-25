package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// GenerateHRZonesReport aggregates heart rate streams for a given year and outputs an SVG chart.
func GenerateHRZonesReport(dir string, year int, zonesStr string, outPath string) error {
	activitiesDir := filepath.Join(dir, "activities")
	files, err := os.ReadDir(activitiesDir)
	if err != nil {
		return fmt.Errorf("failed to read activities directory: %w", err)
	}

	// Maps heart rate (bpm) to total duration in seconds
	bpmSeconds := make(map[int]float64)
	totalSeconds := 0.0
	activityCount := 0

	startDate := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	endDate := time.Date(year, 12, 31, 23, 59, 59, 999999999, time.UTC)

	for _, file := range files {
		if filepath.Ext(file.Name()) != ".json" {
			continue
		}

		path := filepath.Join(activitiesDir, file.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var act ActivityDetail
		if err := json.Unmarshal(data, &act); err != nil {
			continue
		}

		// Filter by year
		if act.StartDate.Before(startDate) || act.StartDate.After(endDate) {
			continue
		}

		if act.Streams == nil {
			continue
		}

		hrStream, okHR := act.Streams["heartrate"]
		timeStream, okTime := act.Streams["time"]

		if okHR && okTime && len(hrStream.Data) > 1 {
			hr := getFloatSlice(hrStream.Data)
			t := getFloatSlice(timeStream.Data)

			if len(hr) == len(t) {
				activityCount++
				for i := 0; i < len(hr); i++ {
					bpm := int(math.Round(hr[i]))
					if bpm <= 0 {
						continue
					}

					duration := 1.0
					if i > 0 {
						duration = t[i] - t[i-1]
						if duration <= 0 || duration > 10.0 {
							duration = 1.0 // Fallback/cap for large pauses
						}
					}
					bpmSeconds[bpm] += duration
					totalSeconds += duration
				}
			}
		}
	}

	if totalSeconds == 0 {
		fmt.Printf("No heart rate data found for year %d.\n", year)
		return nil
	}

	// Determine output path if not supplied
	if outPath == "" {
		filename := fmt.Sprintf("hr_analysis_%d.svg", year)
		if zonesStr != "" {
			filename = fmt.Sprintf("hr_zones_%d.svg", year)
		}
		outPath = filepath.Join(dir, filename)
	}

	if zonesStr != "" {
		zones, err := parseZones(zonesStr)
		if err != nil {
			return err
		}
		err = drawZonesSVG(outPath, year, zones, bpmSeconds, totalSeconds, activityCount)
		if err != nil {
			return fmt.Errorf("failed to generate zones SVG: %w", err)
		}
	} else {
		err = drawBeatDistributionSVG(outPath, year, bpmSeconds, totalSeconds, activityCount)
		if err != nil {
			return fmt.Errorf("failed to generate distribution SVG: %w", err)
		}
	}

	fmt.Printf("Successfully generated heart rate analysis chart at: %s\n", outPath)
	return nil
}

func parseZones(zonesStr string) ([]int, error) {
	parts := strings.Split(zonesStr, ",")
	if len(parts) != 4 {
		return nil, fmt.Errorf("zones must have exactly 4 comma-separated values (e.g. 120,138,152,168)")
	}
	var zones []int
	for _, part := range parts {
		val, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("invalid zone boundary value %q: %w", part, err)
		}
		zones = append(zones, val)
	}
	return zones, nil
}

func drawZonesSVG(outPath string, year int, zones []int, bpmSeconds map[int]float64, totalSecs float64, actCount int) error {
	// Group into 5 zones
	zoneLabels := []string{"Zone 1 (Recovery)", "Zone 2 (Aerobic)", "Zone 3 (Tempo)", "Zone 4 (Threshold)", "Zone 5 (Anaerobic)"}
	zoneColors := []string{"#38bdf8", "#4ade80", "#facc15", "#fb923c", "#ef4444"}
	zoneRanges := []string{
		fmt.Sprintf("<%d bpm", zones[0]),
		fmt.Sprintf("%d-%d bpm", zones[0], zones[1]-1),
		fmt.Sprintf("%d-%d bpm", zones[1], zones[2]-1),
		fmt.Sprintf("%d-%d bpm", zones[2], zones[3]-1),
		fmt.Sprintf(">=%d bpm", zones[3]),
	}
	zoneDurations := make([]float64, 5)

	for bpm, secs := range bpmSeconds {
		if bpm < zones[0] {
			zoneDurations[0] += secs
		} else if bpm < zones[1] {
			zoneDurations[1] += secs
		} else if bpm < zones[2] {
			zoneDurations[2] += secs
		} else if bpm < zones[3] {
			zoneDurations[3] += secs
		} else {
			zoneDurations[4] += secs
		}
	}

	maxDuration := 0.0
	for _, dur := range zoneDurations {
		if dur > maxDuration {
			maxDuration = dur
		}
	}

	// SVG layout settings
	width := 800
	height := 500
	paddingLeft := 180
	paddingRight := 50
	paddingTop := 90
	paddingBottom := 60
	chartWidth := width - paddingLeft - paddingRight
	barHeight := 45
	gap := 20

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Write SVG Header
	fmt.Fprintf(f, "<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"%d\" height=\"%d\" viewBox=\"0 0 %d %d\">\n", width, height, width, height)
	fmt.Fprint(f, "  <style>\n")
	fmt.Fprint(f, "    .bg { fill: #0b0f19; }\n")
	fmt.Fprint(f, "    .title { font-family: system-ui, -apple-system, sans-serif; font-size: 22px; font-weight: 700; fill: #f8fafc; }\n")
	fmt.Fprint(f, "    .subtitle { font-family: system-ui, -apple-system, sans-serif; font-size: 13px; fill: #94a3b8; }\n")
	fmt.Fprint(f, "    .label { font-family: system-ui, -apple-system, sans-serif; font-size: 12px; font-weight: 500; fill: #e2e8f0; text-anchor: end; }\n")
	fmt.Fprint(f, "    .sublabel { font-family: system-ui, -apple-system, sans-serif; font-size: 11px; fill: #64748b; text-anchor: end; }\n")
	fmt.Fprint(f, "    .val { font-family: system-ui, -apple-system, sans-serif; font-size: 12px; font-weight: 600; fill: #f8fafc; }\n")
	fmt.Fprint(f, "    .grid { stroke: #1e293b; stroke-width: 1; stroke-dasharray: 4,4; }\n")
	fmt.Fprint(f, "    .axis { stroke: #334155; stroke-width: 1.5; }\n")
	fmt.Fprint(f, "  </style>\n")

	// Background
	fmt.Fprintf(f, "  <rect class=\"bg\" width=\"%d\" height=\"%d\" />\n", width, height)

	// Titles
	fmt.Fprintf(f, "  <text class=\"title\" x=\"40\" y=\"45\">Heart Rate Training Zones (%d)</text>\n", year)
	hStr, mStr := formatHoursMinutes(totalSecs)
	fmt.Fprintf(f, "  <text class=\"subtitle\" x=\"40\" y=\"68\">Total Run Time: %s across %d activities</text>\n", fmt.Sprintf("%s %s", hStr, mStr), actCount)

	// Draw grid and axis
	for i := 0; i <= 4; i++ {
		pct := float64(i) * 25.0
		x := paddingLeft + int(float64(chartWidth)*(pct/100.0))
		fmt.Fprintf(f, "  <line class=\"grid\" x1=\"%d\" y1=\"%d\" x2=\"%d\" y2=\"%d\" />\n", x, paddingTop-10, x, height-paddingBottom)
		fmt.Fprintf(f, "  <text x=\"%d\" y=\"%d\" font-family=\"sans-serif\" font-size=\"10\" fill=\"#64748b\" text-anchor=\"middle\">%.0f%%</text>\n", x, height-paddingBottom+15, pct)
	}

	// Render bars
	for i := 0; i < 5; i++ {
		y := paddingTop + i*(barHeight+gap)
		dur := zoneDurations[i]
		pct := (dur / totalSecs) * 100.0

		// Avoid dividing by zero if maxDuration is 0
		barW := 0
		if maxDuration > 0 {
			barW = int(float64(chartWidth) * (dur / maxDuration))
		}

		// Zone Labels
		fmt.Fprintf(f, "  <text class=\"label\" x=\"%d\" y=\"%d\">%s</text>\n", paddingLeft-15, y+20, zoneLabels[i])
		fmt.Fprintf(f, "  <text class=\"sublabel\" x=\"%d\" y=\"%d\">%s</text>\n", paddingLeft-15, y+35, zoneRanges[i])

		// Rounded bar
		if barW > 0 {
			fmt.Fprintf(f, "  <rect x=\"%d\" y=\"%d\" width=\"%d\" height=\"%d\" fill=\"%s\" rx=\"4\" />\n", paddingLeft, y, barW, barHeight, zoneColors[i])
		}

		// Values (Time & Percentage)
		timeLabel := formatDurationCompact(dur)
		fmt.Fprintf(f, "  <text class=\"val\" x=\"%d\" y=\"%d\">%s (%.1f%%)</text>\n", paddingLeft+barW+10, y+27, timeLabel, pct)
	}

	// Vertical Baseline Axis
	fmt.Fprintf(f, "  <line class=\"axis\" x1=\"%d\" y1=\"%d\" x2=\"%d\" y2=\"%d\" />\n", paddingLeft, paddingTop-10, paddingLeft, height-paddingBottom)

	fmt.Fprint(f, "</svg>\n")
	return nil
}

func drawBeatDistributionSVG(outPath string, year int, bpmSeconds map[int]float64, totalSecs float64, actCount int) error {
	// Find min and max BPM with significant data
	minBpm := 250
	maxBpm := 0

	for bpm, secs := range bpmSeconds {
		if secs > 10.0 { // ignore noise
			if bpm < minBpm {
				minBpm = bpm
			}
			if bpm > maxBpm {
				maxBpm = bpm
			}
		}
	}

	// Sensible defaults if range is invalid
	if minBpm >= maxBpm {
		minBpm = 100
		maxBpm = 200
	} else {
		// Expand slightly for visual margins
		minBpm = int(math.Max(50, float64(minBpm-5)))
		maxBpm = int(math.Min(220, float64(maxBpm+5)))
	}

	// Group and scale data
	maxSecs := 0.0
	bpmData := make([]float64, maxBpm-minBpm+1)
	for bpm, secs := range bpmSeconds {
		if bpm >= minBpm && bpm <= maxBpm {
			bpmData[bpm-minBpm] = secs
			if secs > maxSecs {
				maxSecs = secs
			}
		}
	}

	// SVG layout settings
	width := 800
	height := 500
	paddingLeft := 70
	paddingRight := 40
	paddingTop := 90
	paddingBottom := 60
	chartWidth := width - paddingLeft - paddingRight
	chartHeight := height - paddingTop - paddingBottom

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Write SVG Header
	fmt.Fprintf(f, "<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"%d\" height=\"%d\" viewBox=\"0 0 %d %d\">\n", width, height, width, height)
	fmt.Fprint(f, "  <style>\n")
	fmt.Fprint(f, "    .bg { fill: #0b0f19; }\n")
	fmt.Fprint(f, "    .title { font-family: system-ui, -apple-system, sans-serif; font-size: 22px; font-weight: 700; fill: #f8fafc; }\n")
	fmt.Fprint(f, "    .subtitle { font-family: system-ui, -apple-system, sans-serif; font-size: 13px; fill: #94a3b8; }\n")
	fmt.Fprint(f, "    .grid { stroke: #1e293b; stroke-width: 1; stroke-dasharray: 4,4; }\n")
	fmt.Fprint(f, "    .axis { stroke: #475569; stroke-width: 1.5; }\n")
	fmt.Fprint(f, "  </style>\n")

	// Background
	fmt.Fprintf(f, "  <rect class=\"bg\" width=\"%d\" height=\"%d\" />\n", width, height)

	// Titles
	fmt.Fprintf(f, "  <text class=\"title\" x=\"40\" y=\"45\">Heart Rate Distribution (%d)</text>\n", year)
	hStr, mStr := formatHoursMinutes(totalSecs)
	fmt.Fprintf(f, "  <text class=\"subtitle\" x=\"40\" y=\"68\">Total Run Time: %s across %d activities</text>\n", fmt.Sprintf("%s %s", hStr, mStr), actCount)

	// Y-axis Gridlines & Labels
	yTicks := 4
	for i := 0; i <= yTicks; i++ {
		val := float64(i) * maxSecs / float64(yTicks)
		y := height - paddingBottom - int(float64(chartHeight)*(float64(i)/float64(yTicks)))
		fmt.Fprintf(f, "  <line class=\"grid\" x1=\"%d\" y1=\"%d\" x2=\"%d\" y2=\"%d\" />\n", paddingLeft, y, width-paddingRight, y)
		fmt.Fprintf(f, "  <text x=\"%d\" y=\"%d\" font-family=\"sans-serif\" font-size=\"10\" fill=\"#64748b\" text-anchor=\"end\">%s</text>\n", paddingLeft-10, y+4, formatDurationCompact(val))
	}

	// X-axis Gridlines & Labels (every 10 bpm)
	xStep := 10
	startLabelBpm := ((minBpm + xStep - 1) / xStep) * xStep // Next multiple of 10
	for bpm := startLabelBpm; bpm <= maxBpm; bpm += xStep {
		ratio := float64(bpm-minBpm) / float64(maxBpm-minBpm)
		x := paddingLeft + int(float64(chartWidth)*ratio)
		fmt.Fprintf(f, "  <line class=\"grid\" x1=\"%d\" y1=\"%d\" x2=\"%d\" y2=\"%d\" />\n", x, paddingTop, x, height-paddingBottom)
		fmt.Fprintf(f, "  <text x=\"%d\" y=\"%d\" font-family=\"sans-serif\" font-size=\"10\" fill=\"#64748b\" text-anchor=\"middle\">%d</text>\n", x, height-paddingBottom+18, bpm)
	}

	// X-axis label
	fmt.Fprintf(f, "  <text x=\"%d\" y=\"%d\" font-family=\"sans-serif\" font-weight=\"600\" font-size=\"11\" fill=\"#94a3b8\" text-anchor=\"middle\">Heart Rate (BPM)</text>\n", paddingLeft+(chartWidth/2), height-paddingBottom+40)

	// Draw Histogram Bars
	numBars := len(bpmData)
	barW := float64(chartWidth) / float64(numBars)

	for i, secs := range bpmData {
		if secs <= 0 {
			continue
		}
		bpm := minBpm + i
		barH := 0
		if maxSecs > 0 {
			barH = int(float64(chartHeight) * (secs / maxSecs))
		}
		x := float64(paddingLeft) + float64(i)*barW
		y := height - paddingBottom - barH

		color := getBPMColor(bpm, minBpm, maxBpm)

		// Draw single beat bar (with subtle rounded corners if thin bars permit it, rx=1 is safe)
		// Leave a 0.5px border/gap between bars if they are thick enough
		gapW := 0.2
		if barW < 2.0 {
			gapW = 0.0
		}
		fmt.Fprintf(f, "  <rect x=\"%.2f\" y=\"%d\" width=\"%.2f\" height=\"%d\" fill=\"%s\" rx=\"1\" />\n", x, y, barW-gapW, barH, color)
	}

	// Axes lines
	fmt.Fprintf(f, "  <line class=\"axis\" x1=\"%d\" y1=\"%d\" x2=\"%d\" y2=\"%d\" />\n", paddingLeft, height-paddingBottom, width-paddingRight, height-paddingBottom)
	fmt.Fprintf(f, "  <line class=\"axis\" x1=\"%d\" y1=\"%d\" x2=\"%d\" y2=\"%d\" />\n", paddingLeft, paddingTop, paddingLeft, height-paddingBottom)

	fmt.Fprint(f, "</svg>\n")
	return nil
}

func getBPMColor(bpm int, minBpm, maxBpm int) string {
	t := float64(bpm-minBpm) / float64(maxBpm-minBpm)
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}

	// 5-color spectrum mapping:
	// Cyan/Blue -> Green -> Yellow -> Orange -> Crimson
	var r, g, b int
	if t < 0.25 {
		// Interpolate Blue (56, 189, 248) to Green (74, 222, 128)
		f := t / 0.25
		r = int(56 + f*(74-56))
		g = int(189 + f*(222-189))
		b = int(248 + f*(128-248))
	} else if t < 0.50 {
		// Interpolate Green (74, 222, 128) to Yellow (250, 204, 21)
		f := (t - 0.25) / 0.25
		r = int(74 + f*(250-74))
		g = int(222 + f*(204-222))
		b = int(128 + f*(21-128))
	} else if t < 0.75 {
		// Interpolate Yellow (250, 204, 21) to Orange (251, 146, 60)
		f := (t - 0.50) / 0.25
		r = int(250 + f*(251-250))
		g = int(204 + f*(146-204))
		b = int(21 + f*(60-21))
	} else {
		// Interpolate Orange (251, 146, 60) to Red (239, 68, 68)
		f := (t - 0.75) / 0.25
		r = int(251 + f*(239-251))
		g = int(146 + f*(68-146))
		b = int(60 + f*(68-60))
	}

	return fmt.Sprintf("rgb(%d, %d, %d)", r, g, b)
}

func formatHoursMinutes(secs float64) (string, string) {
	h := int(secs) / 3600
	m := (int(secs) % 3600) / 60
	return fmt.Sprintf("%dh", h), fmt.Sprintf("%dm", m)
}

func formatDurationCompact(secs float64) string {
	h := int(secs) / 3600
	m := (int(secs) % 3600) / 60
	s := int(secs) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
