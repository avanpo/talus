package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SyncOptions configures synchronization behavior.
type SyncOptions struct {
	Force        bool
	CheckUpdates bool
	RecentWindow time.Duration
	RefreshIDs   []int64
}

// localActivityHeader represents the basic summary fields stored inside an existing activity JSON.
type localActivityHeader struct {
	Name               string                `json:"name"`
	Distance           float64               `json:"distance"`
	MovingTime         int                   `json:"moving_time"`
	TotalElevationGain float64               `json:"total_elevation_gain"`
	Type               string                `json:"type"`
	SportType          string                `json:"sport_type"`
	GearID             string                `json:"gear_id"`
	Streams            map[string]StreamInfo `json:"streams"`
}

// needsUpdate checks if the local activity file needs to be re-downloaded based on summary comparison.
func needsUpdate(filename string, sa *SummaryActivity) (bool, string) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return true, "file unreadable"
	}

	var local localActivityHeader
	if err := json.Unmarshal(data, &local); err != nil {
		return true, "corrupted JSON"
	}

	// 1. Check if name was edited
	if sa.Name != "" && local.Name != sa.Name {
		return true, fmt.Sprintf("title changed (%q -> %q)", local.Name, sa.Name)
	}

	// 2. Check if sport type changed (e.g. Run -> TrailRun)
	if sa.SportType != "" && local.SportType != "" && local.SportType != sa.SportType {
		return true, fmt.Sprintf("sport type changed (%s -> %s)", local.SportType, sa.SportType)
	} else if sa.Type != "" && local.Type != sa.Type {
		return true, fmt.Sprintf("type changed (%s -> %s)", local.Type, sa.Type)
	}

	// 3. Check if distance changed significantly (>1m) e.g. crop
	if sa.Distance > 0 && math.Abs(local.Distance-sa.Distance) > 1.0 {
		return true, fmt.Sprintf("distance changed (%.1fm -> %.1fm)", local.Distance, sa.Distance)
	}

	// 4. Check if moving time changed (>2s)
	if sa.MovingTime > 0 && int(math.Abs(float64(local.MovingTime-sa.MovingTime))) > 2 {
		return true, fmt.Sprintf("moving time changed (%ds -> %ds)", local.MovingTime, sa.MovingTime)
	}

	// 5. Check if gear changed
	if sa.GearID != "" && local.GearID != sa.GearID {
		return true, fmt.Sprintf("gear changed (%s -> %s)", local.GearID, sa.GearID)
	}

	// 6. Check if GPS activity is missing streams entirely
	isGPS := sa.Type == "Run" || sa.SportType == "Run" || sa.SportType == "TrailRun" || sa.Type == "Ride"
	if isGPS && (local.Streams == nil || len(local.Streams) == 0) {
		return true, "missing streams"
	}

	return false, ""
}

func fetchWithRetry(fetchFn func() ([]byte, error), desc string) ([]byte, error) {
	for {
		bytes, err := fetchFn()
		if err != nil {
			if isDailyRateLimit(err) {
				fmt.Println("Daily rate limit reached. Please run the sync tool again tomorrow.")
				return nil, err
			}
			if isRateLimitError(err) {
				fmt.Printf("Rate limit hit (%s). Waiting 15 minutes to resume...\n", desc)
				time.Sleep(15 * time.Minute)
				continue
			}
			return nil, err
		}
		return bytes, nil
	}
}

func downloadAndSaveActivity(client *StravaClient, id int64, filename string) error {
	detailBytes, err := fetchWithRetry(func() ([]byte, error) {
		return client.FetchActivityDetail(id)
	}, fmt.Sprintf("activity %d detail", id))
	if err != nil {
		return err
	}

	streamBytes, err := fetchWithRetry(func() ([]byte, error) {
		return client.FetchActivityStreams(id)
	}, fmt.Sprintf("activity %d streams", id))
	if err != nil {
		return err
	}

	// Merge detail and stream JSONs
	var mergedBytes []byte
	var detailMap map[string]interface{}
	if err := json.Unmarshal(detailBytes, &detailMap); err == nil {
		var streamsVal interface{}
		if err := json.Unmarshal(streamBytes, &streamsVal); err == nil {
			detailMap["streams"] = streamsVal
			if mBytes, err := json.MarshalIndent(detailMap, "", "  "); err == nil {
				mergedBytes = mBytes
			}
		}
	}
	if len(mergedBytes) == 0 {
		mergedBytes = detailBytes
	}

	return os.WriteFile(filename, mergedBytes, 0644)
}

// SyncActivities synchronizes activities from Strava into the backup directory.
// Preserved for backwards compatibility with existing callers and unit tests.
func SyncActivities(dir string, force bool) error {
	return SyncActivitiesWithOptions(dir, SyncOptions{Force: force})
}

// SyncActivitiesWithOptions synchronizes activities with fine-grained control over updates and lookbacks.
func SyncActivitiesWithOptions(dir string, opts SyncOptions) error {
	// 1. Load config and metadata
	client, err := NewStravaClient(dir)
	if err != nil {
		return err
	}

	meta, err := LoadMetadata(dir)
	if err != nil {
		return fmt.Errorf("failed to load metadata: %w", err)
	}

	activitiesDir := filepath.Join(dir, "activities")
	if err := os.MkdirAll(activitiesDir, 0755); err != nil {
		return fmt.Errorf("failed to create activities directory: %w", err)
	}

	// 2. Targeted refresh of specific IDs if requested
	processedRefreshIDs := make(map[int64]bool)
	if len(opts.RefreshIDs) > 0 {
		fmt.Printf("Explicit refresh requested for %d activity ID(s)...\n", len(opts.RefreshIDs))
		for _, id := range opts.RefreshIDs {
			filename := filepath.Join(activitiesDir, fmt.Sprintf("%d.json", id))
			fmt.Printf("Refreshing activity %d...\n", id)
			if err := downloadAndSaveActivity(client, id, filename); err != nil {
				return fmt.Errorf("failed to refresh activity %d: %w", id, err)
			}
			processedRefreshIDs[id] = true
			time.Sleep(1 * time.Second)
		}
	}

	// Determine starting timestamp
	afterTimestamp := meta.LastActivityTimestamp
	if opts.Force || opts.CheckUpdates {
		afterTimestamp = 0
	} else if opts.RecentWindow > 0 {
		recentEpoch := time.Now().Add(-opts.RecentWindow).Unix()
		// Start at whichever is earlier: recent window or last synced activity
		if meta.LastActivityTimestamp == 0 || recentEpoch < meta.LastActivityTimestamp {
			afterTimestamp = recentEpoch
		}
	}

	if opts.Force {
		fmt.Println("Force sync enabled: fetching all activities from the beginning of time and overwriting local files...")
	} else if opts.CheckUpdates {
		fmt.Println("Check updates enabled: scanning all activities to detect changes on Strava...")
	} else if opts.RecentWindow > 0 {
		fmt.Printf("Recent window sync: checking activities since %s (last %v)...\n",
			time.Unix(afterTimestamp, 0).Format(time.RFC3339), opts.RecentWindow)
	} else {
		fmt.Printf("Starting sync since %s (timestamp: %d)...\n",
			time.Unix(afterTimestamp, 0).Format(time.RFC3339),
			afterTimestamp,
		)
	}

	page := 1
	perPage := 100
	newActivitiesCount := 0
	updatedActivitiesCount := 0
	skippedActivitiesCount := 0
	var maxTimestamp int64 = meta.LastActivityTimestamp

	anyFailure := false
	var syncErr error

	for {
		fmt.Printf("Fetching page %d of activities list...\n", page)
		var summaries []SummaryActivity
		for {
			summaries, err = client.FetchSummaryActivities(afterTimestamp, page, perPage)
			if err != nil {
				if isDailyRateLimit(err) {
					fmt.Println("Daily rate limit reached. Please run the sync tool again tomorrow.")
					anyFailure = true
					syncErr = err
					break
				}
				if isRateLimitError(err) {
					fmt.Println("Rate limit hit. Waiting 15 minutes to resume...")
					time.Sleep(15 * time.Minute)
					continue
				}
				anyFailure = true
				syncErr = fmt.Errorf("failed to fetch page %d: %w", page, err)
				break
			}
			break
		}
		if anyFailure {
			break
		}

		if len(summaries) == 0 {
			fmt.Println("No more activities found on this page.")
			break
		}

		fmt.Printf("Found %d activities on page %d.\n", len(summaries), page)

		for _, sa := range summaries {
			epoch := sa.StartDate.Unix()
			filename := filepath.Join(activitiesDir, fmt.Sprintf("%d.json", sa.ID))

			// Check if file already exists
			shouldDownload := false
			isExisting := false

			if _, err := os.Stat(filename); err == nil {
				isExisting = true
			}

			if opts.Force || processedRefreshIDs[sa.ID] {
				shouldDownload = true
			} else if !isExisting {
				shouldDownload = true
			} else {
				// File already exists locally. Check if we need to inspect for updates.
				isRecent := opts.RecentWindow > 0 && sa.StartDate.After(time.Now().Add(-opts.RecentWindow))
				if opts.CheckUpdates || isRecent {
					needed, reason := needsUpdate(filename, &sa)
					if needed {
						fmt.Printf("Detected update for activity %d (%s): %s\n", sa.ID, sa.Name, reason)
						shouldDownload = true
						updatedActivitiesCount++
					}
				}
			}

			if !shouldDownload {
				skippedActivitiesCount++
				if epoch > maxTimestamp {
					maxTimestamp = epoch
				}
				continue
			}

			actionLabel := "Downloading"
			if isExisting {
				actionLabel = "Updating"
			}
			fmt.Printf("%s detailed activity %d (%s - %s)...\n",
				actionLabel, sa.ID, sa.StartDate.Format("2006-01-02 15:04:05"), sa.Name)

			if err := downloadAndSaveActivity(client, sa.ID, filename); err != nil {
				if isDailyRateLimit(err) {
					fmt.Println("Daily rate limit reached. Please run the sync tool again tomorrow.")
					anyFailure = true
					syncErr = err
					break
				}
				anyFailure = true
				syncErr = fmt.Errorf("failed to download activity %d: %w", sa.ID, err)
				fmt.Printf("Error: %v. Stopping sync.\n", syncErr)
				break
			}

			if !isExisting {
				newActivitiesCount++
			}
			if epoch > maxTimestamp {
				maxTimestamp = epoch
			}

			// Polite delay to avoid hitting limits
			time.Sleep(1 * time.Second)
		}

		if anyFailure {
			break
		}

		// Move to the next page
		page++
	}

	// Count total files to update metadata accurately
	files, err := os.ReadDir(activitiesDir)
	if err == nil {
		meta.TotalActivities = len(files)
	}

	meta.LastSyncTime = time.Now()
	// Only advance the last activity timestamp if there were no failures.
	if !anyFailure {
		meta.LastActivityTimestamp = maxTimestamp
	} else {
		fmt.Printf("Sync completed with a partial failure. Not advancing last_activity_timestamp (remains: %d) so next sync can resume.\n", meta.LastActivityTimestamp)
	}

	if err := SaveMetadata(dir, meta); err != nil {
		return fmt.Errorf("failed to save metadata: %w", err)
	}

	fmt.Printf("\nSync summary:\n")
	fmt.Printf("- New activities downloaded: %d\n", newActivitiesCount)
	if updatedActivitiesCount > 0 {
		fmt.Printf("- Existing activities updated: %d\n", updatedActivitiesCount)
	}
	fmt.Printf("- Activities skipped (up to date): %d\n", skippedActivitiesCount)
	fmt.Printf("- Total activities in backup: %d\n", meta.TotalActivities)

	return syncErr
}

func isRateLimitError(err error) bool {
	var rlErr *RateLimitError
	if errors.As(err, &rlErr) {
		return true
	}
	return err != nil && (strings.Contains(err.Error(), "429 Too Many Requests") || strings.Contains(err.Error(), "429 too many requests"))
}

func isDailyRateLimit(err error) bool {
	var rlErr *RateLimitError
	if errors.As(err, &rlErr) {
		return rlErr.IsDaily
	}
	return false
}
