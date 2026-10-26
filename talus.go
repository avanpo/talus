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

		flagsWithArgs := map[string]bool{
			"recent": true, "refresh": true, "client-id": true, "client-secret": true,
		}
		_ = syncCmd.Parse(reorderArgs(flagsWithArgs, os.Args[2:]))

		args := syncCmd.Args()
		backupDir := "."
		if len(args) >= 1 {
			backupDir = args[0]
		} else if envDir := os.Getenv("DATA_DIR"); envDir != "" {
			backupDir = envDir
		}

		// Auto-initialize if not configured yet
		if !isConfigured(backupDir) {
			fmt.Printf("No configuration found in %s. Running one-time setup...\n", backupDir)
			err := RunTerminalOAuthFlow(backupDir, *clientID, *clientSecret)
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
		}

		fmt.Printf("Starting synchronization for %s...\n", backupDir)
		err = SyncActivitiesWithOptions(backupDir, opts)
		if err != nil {
			fmt.Printf("Error running synchronization: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Synchronization finished successfully.")

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

	flagsWithArgs := map[string]bool{
		"port": true, "sync-interval": true, "check-updates-interval": true,
	}
	_ = serveCmd.Parse(reorderArgs(flagsWithArgs, rawArgs))

	args := serveCmd.Args()
	backupDir := ""
	if len(args) >= 1 {
		backupDir = args[0]
	} else if envDir := os.Getenv("DATA_DIR"); envDir != "" {
		backupDir = envDir
	} else {
		if _, err := os.Stat("/data"); err == nil {
			backupDir = "/data"
		} else {
			backupDir = "."
		}
	}

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

	err = StartWebServer(backupDir, *port, syncInterval, checkUpdatesInterval, *noSync)
	if err != nil {
		fmt.Printf("Error running server: %v\n", err)
		os.Exit(1)
	}
}

func printUsageAndExit() {
	fmt.Println("talus: local endurance telemetry & training analytics dashboard")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  talus [backup_dir] [flags]       Start web dashboard & background sync daemon (default)")
	fmt.Println("  talus sync [backup_dir] [flags]  Run a one-off headless synchronization")
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
	os.Exit(0)
}
