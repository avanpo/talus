package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// reorderArgs moves flags (and their arguments) before non-flag arguments so Go's flag package
// parses flags regardless of whether the backup directory is placed first or last.
func reorderArgs(flagTakesArg map[string]bool, args []string) []string {
	var flags []string
	var nonFlags []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			nonFlags = append(nonFlags, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			name := strings.TrimLeft(arg, "-")
			// If not --flag=val and the flag takes an argument, grab the next token as well
			if !strings.Contains(name, "=") && flagTakesArg[name] {
				if i+1 < len(args) {
					i++
					flags = append(flags, args[i])
				}
			}
		} else {
			nonFlags = append(nonFlags, arg)
		}
	}

	return append(flags, nonFlags...)
}

func parseDurationCustom(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, fmt.Errorf("invalid days format %q: %w", s, err)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	if strings.HasSuffix(s, "w") {
		weeks, err := strconv.Atoi(strings.TrimSuffix(s, "w"))
		if err != nil {
			return 0, fmt.Errorf("invalid weeks format %q: %w", s, err)
		}
		return time.Duration(weeks) * 7 * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func parseIDs(s string) ([]int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	var ids []int64
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid activity id %q: %w", p, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func resolveDirectories(posArg, flagDataDir, flagConfigDir, flagMetadataDir string) (string, string, string) {
	dataDir := flagDataDir
	if dataDir == "" {
		if posArg != "" {
			dataDir = posArg
		} else if envData := os.Getenv("DATA_DIR"); envData != "" {
			dataDir = envData
		} else {
			if _, err := os.Stat("/data"); err == nil {
				dataDir = "/data"
			} else {
				dataDir = "."
			}
		}
	}

	configDir := flagConfigDir
	if configDir == "" {
		if envConfig := os.Getenv("CONFIG_DIR"); envConfig != "" {
			configDir = envConfig
		} else {
			configDir = dataDir
		}
	}

	metadataDir := flagMetadataDir
	if metadataDir == "" {
		if envMeta := os.Getenv("METADATA_DIR"); envMeta != "" {
			metadataDir = envMeta
		} else {
			metadataDir = configDir
		}
	}

	return dataDir, configDir, metadataDir
}

func main() {
	if len(os.Args) < 2 {
		runServe(nil)
		return
	}

	subcommand := os.Args[1]

	switch subcommand {
	case "help", "-h", "--help":
		printUsageAndExit()

	case "sync":
		syncCmd := flag.NewFlagSet("sync", flag.ExitOnError)
		force := syncCmd.Bool("force", false, "Force download and overwrite all existing activity files")
		checkUpdates := syncCmd.Bool("check-updates", false, "Scan summaries to detect and redownload edited activities (title, distance, sport type, gear)")
		recent := syncCmd.String("recent", "", "Look back and refresh activities within duration (e.g. '30d', '12w', '180d')")
		refresh := syncCmd.String("refresh", "", "Comma-separated list of specific activity IDs to force re-download")
		clientID := syncCmd.String("client-id", "", "Strava API Client ID (if not authorized yet)")
		clientSecret := syncCmd.String("client-secret", "", "Strava API Client Secret (if not authorized yet)")
		dataDirFlag := syncCmd.String("data-dir", "", "Directory for storing raw activity JSON files (or $DATA_DIR)")
		configDirFlag := syncCmd.String("config-dir", "", "Directory for storing OAuth credentials and tokens (or $CONFIG_DIR)")
		metadataDirFlag := syncCmd.String("metadata-dir", "", "Directory for storing sync metadata (or $METADATA_DIR, defaults to config dir)")

		flagsWithArgs := map[string]bool{
			"recent": true, "refresh": true, "client-id": true, "client-secret": true,
			"data-dir": true, "config-dir": true, "metadata-dir": true,
		}
		_ = syncCmd.Parse(reorderArgs(flagsWithArgs, os.Args[2:]))

		args := syncCmd.Args()
		posArg := ""
		if len(args) >= 1 {
			posArg = args[0]
		}
		dataDir, configDir, metadataDir := resolveDirectories(posArg, *dataDirFlag, *configDirFlag, *metadataDirFlag)

		// Auto-initialize if not configured yet
		if !isConfigured(configDir) && !isConfigured(dataDir) {
			fmt.Printf("No configuration found in %s. Running one-time setup...\n", configDir)
			err := RunTerminalOAuthFlowWithDirs(configDir, dataDir, *clientID, *clientSecret)
			if err != nil {
				fmt.Printf("Setup failed: %v\n", err)
				os.Exit(1)
			}
		}

		var recentDuration time.Duration
		if *recent != "" {
			var err error
			recentDuration, err = parseDurationCustom(*recent)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
				os.Exit(1)
			}
		}

		refreshIDs, err := parseIDs(*refresh)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}

		opts := SyncOptions{
			Force:        *force,
			CheckUpdates: *checkUpdates,
			RecentWindow: recentDuration,
			RefreshIDs:   refreshIDs,
			DataDir:      dataDir,
			ConfigDir:    configDir,
			MetadataDir:  metadataDir,
		}

		fmt.Printf("Starting synchronization for %s...\n", dataDir)
		err = SyncActivitiesWithOptions(dataDir, opts)
		if err != nil {
			fmt.Printf("Error running synchronization: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Synchronization finished successfully.")

	case "route":
		runRouteCommand(os.Args[2:])

	case "serve":
		runServe(os.Args[2:])

	default:
		// Default to running serve with the provided arguments
		runServe(os.Args[1:])
	}
}

func runServe(rawArgs []string) {
	serveCmd := flag.NewFlagSet("serve", flag.ExitOnError)
	port := serveCmd.Int("port", 8080, "Port to listen on (or use $PORT)")
	syncIntervalStr := serveCmd.String("sync-interval", "1h", "Interval between incremental syncs (e.g. 1h, 30m, or use $SYNC_INTERVAL)")
	checkUpdatesIntervalStr := serveCmd.String("check-updates-interval", "24h", "Interval between update scans (e.g. 24h, 12h, or use $CHECK_UPDATES_INTERVAL)")
	noSync := serveCmd.Bool("no-sync", false, "Disable background sync daemon")
	dataDirFlag := serveCmd.String("data-dir", "", "Directory for storing raw activity JSON files (or $DATA_DIR)")
	configDirFlag := serveCmd.String("config-dir", "", "Directory for storing OAuth credentials and tokens (or $CONFIG_DIR)")
	metadataDirFlag := serveCmd.String("metadata-dir", "", "Directory for storing sync metadata (or $METADATA_DIR, defaults to config dir)")

	flagsWithArgs := map[string]bool{
		"port": true, "sync-interval": true, "check-updates-interval": true,
		"data-dir": true, "config-dir": true, "metadata-dir": true,
	}
	_ = serveCmd.Parse(reorderArgs(flagsWithArgs, rawArgs))

	args := serveCmd.Args()
	posArg := ""
	if len(args) >= 1 {
		posArg = args[0]
	}
	dataDir, configDir, metadataDir := resolveDirectories(posArg, *dataDirFlag, *configDirFlag, *metadataDirFlag)

	if envPort := os.Getenv("PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			*port = p
		}
	}
	if envSync := os.Getenv("SYNC_INTERVAL"); envSync != "" {
		*syncIntervalStr = envSync
	}
	if envCheck := os.Getenv("CHECK_UPDATES_INTERVAL"); envCheck != "" {
		*checkUpdatesIntervalStr = envCheck
	}

	syncInterval, err := parseDurationCustom(*syncIntervalStr)
	if err != nil {
		fmt.Printf("Error: Invalid sync interval %q: %v\n", *syncIntervalStr, err)
		os.Exit(1)
	}
	checkUpdatesInterval, err := parseDurationCustom(*checkUpdatesIntervalStr)
	if err != nil {
		fmt.Printf("Error: Invalid check updates interval %q: %v\n", *checkUpdatesIntervalStr, err)
		os.Exit(1)
	}

	err = StartWebServerWithDirs(dataDir, configDir, metadataDir, *port, syncInterval, checkUpdatesInterval, *noSync)
	if err != nil {
		fmt.Printf("Error running server: %v\n", err)
		os.Exit(1)
	}
}

func runRouteCommand(rawArgs []string) {
	routeCmd := flag.NewFlagSet("route", flag.ExitOnError)
	startStr := routeCmd.String("start", "", "Start/stop coordinates as 'lat,lon' (e.g. '40.015,-105.270')")
	distKm := routeCmd.Float64("distance", 10.0, "Target route distance in km")
	tolKm := routeCmd.Float64("tolerance", 1.0, "Distance tolerance (+/- km)")
	maxOverlap := routeCmd.Float64("max-overlap", 5.0, "Maximum allowed trail overlap percentage (0-100)")
	minRuns := routeCmd.Int("min-runs", 2, "Minimum unique runs required on a trail segment")
	sport := routeCmd.String("sport", "run", "Sport filter: 'run', 'foot' (run+hike+walk), or 'all'")
	allowRepeat := routeCmd.Bool("allow-repeat-loops", false, "Allow repeating a sub-loop that meets --min-loop-gain")
	minLoopGain := routeCmd.Float64("min-loop-gain", 400.0, "Minimum elevation gain (m) for a repeatable sub-loop")
	snapRadius := routeCmd.Float64("snap-radius", 25.0, "GPS trace snapping grid resolution in meters")
	timeoutSec := routeCmd.Int("timeout", 15, "Search time budget in seconds")
	maxResults := routeCmd.Int("top", 5, "Number of top route options to output")
	maxCPUs := routeCmd.Int("max-cpus", 0, "Max CPU cores to use (default: 25% of host cores, min 1)")
	maxMemGB := routeCmd.Int("max-mem-gb", 8, "Soft memory limit in GiB")
	outDir := routeCmd.String("out-dir", "", "Directory to write output .gpx files (default: <data-dir>/routes)")
	dataDirFlag := routeCmd.String("data-dir", "", "Directory containing raw activity JSON files (or $DATA_DIR)")

	flagsWithArgs := map[string]bool{
		"start": true, "distance": true, "tolerance": true, "max-overlap": true,
		"min-runs": true, "sport": true, "min-loop-gain": true, "snap-radius": true,
		"timeout": true, "top": true, "max-cpus": true, "max-mem-gb": true,
		"out-dir": true, "data-dir": true,
	}
	_ = routeCmd.Parse(reorderArgs(flagsWithArgs, rawArgs))

	args := routeCmd.Args()
	posArg := ""
	if len(args) >= 1 {
		posArg = args[0]
	}
	dataDir, _, _ := resolveDirectories(posArg, *dataDirFlag, "", "")

	// Enforce polite resource caps inside CLI execution
	workers := *maxCPUs
	if workers <= 0 {
		workers = defaultRouteWorkers()
	}
	runtime.GOMAXPROCS(workers)
	if *maxMemGB > 0 {
		debug.SetMemoryLimit(int64(*maxMemGB) << 30)
	}

	var startLat, startLon float64
	if strings.TrimSpace(*startStr) != "" {
		parts := strings.Split(*startStr, ",")
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Error: --start must be 'lat,lon' (got %q)\n", *startStr)
			os.Exit(1)
		}
		var err1, err2 error
		startLat, err1 = strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		startLon, err2 = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err1 != nil || err2 != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid coordinates in --start %q\n", *startStr)
			os.Exit(1)
		}
	} else {
		starts := DetectFrequentStarts(dataDir, 1)
		if len(starts) == 0 {
			fmt.Fprintln(os.Stderr, "Error: --start lat,lon is required (no frequent start locations detected)")
			os.Exit(1)
		}
		startLat, startLon = starts[0].Lat, starts[0].Lon
		fmt.Printf("No --start provided; using most frequent start location: %.5f, %.5f (%d activities)\n",
			startLat, startLon, starts[0].Count)
	}

	req := RouteBuildRequest{
		StartLat:         startLat,
		StartLon:         startLon,
		TargetDistanceKm: *distKm,
		ToleranceKm:      *tolKm,
		MaxOverlapPct:    *maxOverlap,
		MinRuns:          *minRuns,
		SportFilter:      *sport,
		AllowRepeatLoops: *allowRepeat,
		MinLoopGainM:     *minLoopGain,
		SnapRadiusM:      *snapRadius,
		TimeoutSec:       *timeoutSec,
		MaxResults:       *maxResults,
		MaxCPUs:          workers,
	}
	if err := req.Normalize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Building routes from %s (CPUs: %d, Mem limit: %d GiB)...\n", dataDir, workers, *maxMemGB)
	progressFn := func(msg string) {
		fmt.Printf("  %s\n", msg)
	}

	ctx := context.Background()
	graph, err := BuildTrailGraph(ctx, dataDir, req, progressFn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building trail graph: %v\n", err)
		os.Exit(1)
	}

	out, err := FindTopRoutes(ctx, graph, req, progressFn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error finding routes: %v\n", err)
		os.Exit(1)
	}

	destDir := *outDir
	if destDir == "" {
		destDir = filepath.Join(dataDir, "routes")
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output directory %s: %v\n", destDir, err)
		os.Exit(1)
	}

	fmt.Printf("\nTop %d routes (evaluated %d candidates in %.1fs):\n",
		len(out.Routes), out.CandidatesFound, float64(out.ElapsedMs)/1000.0)
	for i := range out.Routes {
		r := &out.Routes[i]
		gpxBytes, err := GenerateRouteGPX(r)
		if err != nil {
			continue
		}
		fname := fmt.Sprintf("route-%d-%.1fkm-%.0fm.gpx", r.Rank, r.DistanceKm, r.ElevationGainM)
		fpath := filepath.Join(destDir, fname)
		if err := os.WriteFile(fpath, gpxBytes, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", fpath, err)
			continue
		}
		repeatNote := ""
		if r.RepeatInfo != nil {
			repeatNote = fmt.Sprintf(" [%dx %.1fkm loop, +%.0fm/lap]", r.RepeatInfo.Laps, r.RepeatInfo.LoopDistanceKm, r.RepeatInfo.LoopGainM)
		}
		fmt.Printf("  #%d [%s]: %.2f km | +%.0fm gain (%.1f m/km) | %.1f%% overlap%s -> %s\n",
			r.Rank, r.DistinctLabel, r.DistanceKm, r.ElevationGainM, r.VertPerKm, r.OverlapPct, repeatNote, fpath)
	}
}

func printUsageAndExit() {
	fmt.Println("talus: local endurance telemetry & training analytics dashboard")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  talus [backup_dir] [flags]       Start web dashboard & background sync daemon (default)")
	fmt.Println("  talus sync [backup_dir] [flags]  Run a one-off headless synchronization")
	fmt.Println("  talus route [backup_dir] [flags] Build high-elevation loop routes from personal GPS history")
	fmt.Println()
	fmt.Println("Directory flags:")
	fmt.Println("  --data-dir <path>                Directory for raw activity JSON data (default: [backup_dir] or $DATA_DIR)")
	fmt.Println("  --config-dir <path>              Directory for OAuth tokens and credentials (default: $CONFIG_DIR or data dir)")
	fmt.Println("  --metadata-dir <path>            Directory for sync metadata (default: $METADATA_DIR or config dir)")
	fmt.Println()
	fmt.Println("Server flags (talus / talus serve):")
	fmt.Println("  --port <port>                    Port to listen on (default: 8080 or $PORT)")
	fmt.Println("  --sync-interval <dur>            Interval for incremental syncs (default: 1h or $SYNC_INTERVAL)")
	fmt.Println("  --check-updates-interval <dur>   Interval for edit/crop scans (default: 24h or $CHECK_UPDATES_INTERVAL)")
	fmt.Println("  --no-sync                        Dashboard-only mode (disable background sync)")
	fmt.Println()
	fmt.Println("Sync flags (talus sync):")
	fmt.Println("  --recent <dur>                   Check activities within recent duration (e.g. '30d', '180d')")
	fmt.Println("  --check-updates                  Scan all activities to detect edits (titles, crops, gear)")
	fmt.Println("  --refresh <id1,id2>              Force re-download of specific activity IDs")
	fmt.Println("  --force                          Force overwrite of all existing files")
	fmt.Println()
	fmt.Println("Route flags (talus route):")
	fmt.Println("  --start <lat,lon>                Start/finish coordinates (auto-detects most frequent start if omitted)")
	fmt.Println("  --distance <km>                  Target distance in km (default: 10)")
	fmt.Println("  --tolerance <km>                 Distance tolerance +/- km (default: 1)")
	fmt.Println("  --max-overlap <pct>              Max trail overlap percentage (default: 5)")
	fmt.Println("  --min-runs <n>                   Min unique runs per trail segment (default: 2)")
	fmt.Println("  --allow-repeat-loops             Allow repeating a high-elevation loop")
	fmt.Println("  --min-loop-gain <m>              Min elevation gain (m) for a repeatable loop (default: 400)")
	fmt.Println("  --timeout <sec>                  Search time budget in seconds (default: 15)")
	fmt.Println("  --max-cpus <n>                   Max CPU cores (default: 25% of host cores)")
	fmt.Println("  --max-mem-gb <gb>                Soft memory limit in GiB (default: 8)")
	fmt.Println("  --out-dir <path>                 Output directory for GPX files (default: <data-dir>/routes)")
	fmt.Println()
	os.Exit(0)
}
