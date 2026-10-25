package main

import (
	"flag"
	"fmt"
	"os"
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
			return 0, err
		}
		return time.Duration(weeks) * 7 * 24 * time.Hour, nil
	}
	// Try standard time.ParseDuration (e.g. "720h")
	d, err := time.ParseDuration(s)
	if err == nil {
		return d, nil
	}
	return 0, fmt.Errorf("unknown duration format %q (examples: '30d', '12w', '720h')", s)
}

func parseIDs(s string) ([]int64, error) {
	if strings.TrimSpace(s) == "" {
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

func main() {
	if len(os.Args) < 2 {
		printUsageAndExit()
	}

	subcommand := os.Args[1]

	switch subcommand {
	case "init":
		initCmd := flag.NewFlagSet("init", flag.ExitOnError)
		clientID := initCmd.String("client-id", "", "Strava API Client ID (required)")
		clientSecret := initCmd.String("client-secret", "", "Strava API Client Secret (required)")
		noLocalhost := initCmd.Bool("no-localhost", false, "Disable temporary local redirect server and use manual copy-paste fallback")

		flagsWithArgs := map[string]bool{
			"client-id": true, "client-secret": true,
		}
		_ = initCmd.Parse(reorderArgs(flagsWithArgs, os.Args[2:]))

		args := initCmd.Args()
		if len(args) < 1 {
			fmt.Println("Error: Missing backup directory path.")
			fmt.Println("Usage: talus init <backup_dir> --client-id <id> --client-secret <secret>")
			os.Exit(1)
		}
		backupDir := args[0]

		if *clientID == "" || *clientSecret == "" {
			fmt.Println("Error: Both --client-id and --client-secret are required.")
			fmt.Println("Usage: talus init <backup_dir> --client-id <id> --client-secret <secret>")
			os.Exit(1)
		}

		fmt.Printf("Initializing backup directory at %s...\n", backupDir)
		err := RunOAuthFlow(backupDir, *clientID, *clientSecret, !*noLocalhost)
		if err != nil {
			fmt.Printf("Error running initialization: %v\n", err)
			os.Exit(1)
		}

	case "sync":
		syncCmd := flag.NewFlagSet("sync", flag.ExitOnError)
		force := syncCmd.Bool("force", false, "Force download and overwrite all existing activity files")
		checkUpdates := syncCmd.Bool("check-updates", false, "Scan summaries to detect and redownload edited activities (title, distance, sport type, gear)")
		recent := syncCmd.String("recent", "", "Look back and refresh activities within duration (e.g. '30d', '12w', '180d')")
		refresh := syncCmd.String("refresh", "", "Comma-separated list of specific activity IDs to force re-download")

		flagsWithArgs := map[string]bool{
			"recent": true, "refresh": true,
		}
		_ = syncCmd.Parse(reorderArgs(flagsWithArgs, os.Args[2:]))

		args := syncCmd.Args()
		if len(args) < 1 {
			fmt.Println("Error: Missing backup directory path.")
			fmt.Println("Usage: talus sync <backup_dir> [--force] [--check-updates] [--recent 30d] [--refresh <id1,id2>]")
			os.Exit(1)
		}
		backupDir := args[0]

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
		}

		fmt.Printf("Starting synchronization for %s...\n", backupDir)
		err = SyncActivitiesWithOptions(backupDir, opts)
		if err != nil {
			fmt.Printf("Error running synchronization: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Synchronization finished successfully.")

	case "stats":
		statsCmd := flag.NewFlagSet("stats", flag.ExitOnError)
		period := statsCmd.String("period", "month", "Aggregation period: 'week', 'month', or 'year'")
		year := statsCmd.Int("year", -1, "Filter by year (-1 = current year, 0 = all years)")
		allYears := statsCmd.Bool("all-years", false, "Show stats across all years")
		sportType := statsCmd.String("type", "run", "Sport type filter: 'run', 'road', 'trail', 'ride', 'hike', 'walk', or 'all'")
		unit := statsCmd.String("unit", "km", "Distance unit: 'km' or 'mi'")
		breakdown := statsCmd.Bool("breakdown", false, "Show road vs trail breakdown sub-rows")

		flagsWithArgs := map[string]bool{
			"period": true, "year": true, "type": true, "unit": true,
		}
		_ = statsCmd.Parse(reorderArgs(flagsWithArgs, os.Args[2:]))

		args := statsCmd.Args()
		if len(args) < 1 {
			fmt.Println("Error: Missing backup directory path.")
			fmt.Println("Usage: talus stats <backup_dir> [--period week|month|year] [--year <year>] [--all-years] [--type run|road|trail|ride|gravel|all] [--unit km|mi] [--breakdown]")
			os.Exit(1)
		}
		backupDir := args[0]

		effectiveYear := *year
		if *allYears || *year == 0 {
			effectiveYear = 0
		} else if *year == -1 {
			// If period is "year", default to showing all years!
			if strings.ToLower(*period) == "year" {
				effectiveYear = 0
			} else {
				effectiveYear = time.Now().Year()
			}
		}

		opts := StatsOptions{
			Period:      *period,
			Year:        effectiveYear,
			SportType:   *sportType,
			Unit:        *unit,
			ShowSubtype: *breakdown,
		}

		err := GenerateStatsReport(backupDir, opts)
		if err != nil {
			fmt.Printf("Error generating stats: %v\n", err)
			os.Exit(1)
		}

	case "analyze":
		analyzeCmd := flag.NewFlagSet("analyze", flag.ExitOnError)
		_ = analyzeCmd.Parse(reorderArgs(nil, os.Args[2:]))

		args := analyzeCmd.Args()
		if len(args) < 1 {
			fmt.Println("Error: Missing backup directory path.")
			fmt.Println("Usage: talus analyze <backup_dir>")
			os.Exit(1)
		}
		backupDir := args[0]

		err := AnalyzeActivities(backupDir)
		if err != nil {
			fmt.Printf("Error running analysis: %v\n", err)
			os.Exit(1)
		}

	case "hr-zones":
		hrZonesCmd := flag.NewFlagSet("hr-zones", flag.ExitOnError)
		year := hrZonesCmd.Int("year", time.Now().Year(), "Year to analyze")
		zones := hrZonesCmd.String("zones", "", "Custom heart rate zones as 4 comma-separated thresholds (e.g. 120,138,152,168)")
		out := hrZonesCmd.String("out", "", "Output path for the generated SVG file")

		flagsWithArgs := map[string]bool{
			"year": true, "zones": true, "out": true,
		}
		_ = hrZonesCmd.Parse(reorderArgs(flagsWithArgs, os.Args[2:]))

		args := hrZonesCmd.Args()
		if len(args) < 1 {
			fmt.Println("Error: Missing backup directory path.")
			fmt.Println("Usage: talus hr-zones <backup_dir> [--year <year>] [--zones <zones>] [--out <output_path>]")
			os.Exit(1)
		}
		backupDir := args[0]

		err := GenerateHRZonesReport(backupDir, *year, *zones, *out)
		if err != nil {
			fmt.Printf("Error running heart rate zones analysis: %v\n", err)
			os.Exit(1)
		}

	case "help", "-h", "--help":
		printUsageAndExit()

	default:
		fmt.Printf("Error: Unknown command %q.\n\n", subcommand)
		printUsageAndExit()
	}
}

func printUsageAndExit() {
	fmt.Println("talus is a local endurance telemetry and training analytics tool for trail/road running, MTB, and gravel.")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  talus <command> <backup_dir> [flags]")
	fmt.Println()
	fmt.Println("The commands are:")
	fmt.Println("  init <backup_dir> --client-id <id> --client-secret <secret>")
	fmt.Println("         Initialize a backup directory and authorize with Strava.")
	fmt.Println("         Optional flags:")
	fmt.Println("           --no-localhost   Disable temporary local server (useful on headless systems).")
	fmt.Println()
	fmt.Println("  sync <backup_dir>")
	fmt.Println("         Synchronize (incremental backup) the authorized directory with Strava.")
	fmt.Println("         Optional flags:")
	fmt.Println("           --recent <dur>   Check and refresh activities within recent duration (e.g. '30d', '180d').")
	fmt.Println("           --check-updates  Scan all activities on Strava to detect edits (titles, crops, sport type).")
	fmt.Println("           --refresh <ids>  Comma-separated activity IDs to re-download specifically.")
	fmt.Println("           --force          Force overwrite of all existing files.")
	fmt.Println()
	fmt.Println("  stats <backup_dir>")
	fmt.Println("         Display periodic volume and fitness statistics (weekly, monthly, yearly).")
	fmt.Println("         Optional flags:")
	fmt.Println("           --period <p>     Aggregation period: 'week', 'month' (default), or 'year'.")
	fmt.Println("           --year <year>    Filter to a specific year (default: current year; all years if --period year).")
	fmt.Println("           --all-years      Show stats across all years.")
	fmt.Println("           --type <type>    Filter sport type: 'run' (default), 'road', 'trail', 'ride', 'gravel', 'all'.")
	fmt.Println("           --unit <unit>    Distance unit: 'km' (default) or 'mi'.")
	fmt.Println("           --breakdown      Show road vs. trail sub-rows under each period.")
	fmt.Println()
	fmt.Println("  analyze <backup_dir>")
	fmt.Println("         Analyze running/riding fitness and group activities by route GPS similarity.")
	fmt.Println()
	fmt.Println("  hr-zones <backup_dir>")
	fmt.Println("         Generate an SVG chart of heart rate zone/beat distribution for a specific year.")
	fmt.Println("         Optional flags:")
	fmt.Println("           --year <year>    Filter activities by year (defaults to current year).")
	fmt.Println("           --zones <z>      Optional 4 comma-separated zone thresholds (e.g. 120,138,152,168).")
	fmt.Println("           --out <file>     Custom file path to save the generated SVG chart.")
	fmt.Println()
	fmt.Println("  help")
	fmt.Println("         Print this usage message.")
	fmt.Println()
	os.Exit(0)
}
