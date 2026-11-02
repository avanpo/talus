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

func printUsageAndExit() {
	fmt.Println("talus: local endurance telemetry & training analytics dashboard")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  talus [backup_dir] [flags]       Start web dashboard & background sync daemon (default)")
	fmt.Println("  talus sync [backup_dir] [flags]  Run a one-off headless synchronization")
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
	os.Exit(0)
}
