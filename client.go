package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RateLimitError represents a Strava rate limit error (HTTP 429).
type RateLimitError struct {
	IsDaily bool
	Message string
}

func (e *RateLimitError) Error() string {
	return e.Message
}

// SummaryActivity represents the fields we need from the summary list.
type SummaryActivity struct {
	ID                 int64     `json:"id"`
	Name               string    `json:"name"`
	Distance           float64   `json:"distance"`
	MovingTime         int       `json:"moving_time"`
	ElapsedTime        int       `json:"elapsed_time"`
	TotalElevationGain float64   `json:"total_elevation_gain"`
	Type               string    `json:"type"`
	SportType          string    `json:"sport_type"`
	StartDate          time.Time `json:"start_date"`
	GearID             string    `json:"gear_id"`
}

// StravaClient handles communication with the Strava API.
type StravaClient struct {
	httpClient *http.Client
	configDir  string
	config     *Config

	// Rate limiting fields
	limit15Min int
	limitDaily int
	usage15Min int
	usageDaily int
}

var testTransport http.RoundTripper // Hook for unit tests to mock network requests

// NewStravaClient creates a new Strava client and automatically handles token refresh if needed.
func NewStravaClient(configDir string) (*StravaClient, error) {
	cfg, err := LoadConfig(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load config (have you run 'init'?): %w", err)
	}

	client := &StravaClient{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		configDir:  configDir,
		config:     cfg,
	}

	if testTransport != nil {
		client.httpClient.Transport = testTransport
	}

	if err := client.ensureValidToken(); err != nil {
		return nil, fmt.Errorf("token validation failed: %w", err)
	}

	return client, nil
}

func (c *StravaClient) updateRateLimits(resp *http.Response) {
	if resp == nil {
		return
	}
	limitHeader := resp.Header.Get("X-RateLimit-Limit")
	usageHeader := resp.Header.Get("X-RateLimit-Usage")
	if limitHeader == "" || usageHeader == "" {
		return
	}

	limits := strings.Split(limitHeader, ",")
	usages := strings.Split(usageHeader, ",")
	if len(limits) < 2 || len(usages) < 2 {
		return
	}

	limit15, err1 := strconv.Atoi(strings.TrimSpace(limits[0]))
	limitDaily, err2 := strconv.Atoi(strings.TrimSpace(limits[1]))
	usage15, err3 := strconv.Atoi(strings.TrimSpace(usages[0]))
	usageDaily, err4 := strconv.Atoi(strings.TrimSpace(usages[1]))

	if err1 == nil && err2 == nil && err3 == nil && err4 == nil {
		c.limit15Min = limit15
		c.limitDaily = limitDaily
		c.usage15Min = usage15
		c.usageDaily = usageDaily
	}
}

// ensureValidToken checks if the current token is expired (or close to expiring) and refreshes it if so.
func (c *StravaClient) ensureValidToken() error {
	// If the token is valid for another 2 minutes, don't refresh.
	if time.Now().Add(2 * time.Minute).Before(c.config.ExpiresAt) {
		return nil
	}

	fmt.Println("Access token expired or expiring soon. Refreshing...")

	tokenURL := "https://www.strava.com/oauth/token"
	form := url.Values{}
	form.Set("client_id", c.config.ClientID)
	form.Set("client_secret", c.config.ClientSecret)
	form.Set("refresh_token", c.config.RefreshToken)
	form.Set("grant_type", "refresh_token")

	resp, err := c.httpClient.PostForm(tokenURL, form)
	if err != nil {
		return fmt.Errorf("failed to send refresh token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errData map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&errData)
		return fmt.Errorf("token refresh failed with status %s: %v", resp.Status, errData)
	}

	var tokResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokResp); err != nil {
		return fmt.Errorf("failed to decode refresh token response: %w", err)
	}

	// Update cached config and persist it
	c.config.AccessToken = tokResp.AccessToken
	c.config.RefreshToken = tokResp.RefreshToken
	c.config.ExpiresAt = time.Unix(tokResp.ExpiresAt, 0)

	if err := SaveConfig(c.configDir, c.config); err != nil {
		return fmt.Errorf("failed to save refreshed configuration: %w", err)
	}

	fmt.Println("Token successfully refreshed and config updated.")
	return nil
}

func (c *StravaClient) isDailyLimitExceeded() bool {
	if c.limitDaily > 0 && c.usageDaily >= c.limitDaily {
		return true
	}
	// If Strava returned 429 but 15-minute quota was not reached, it is the daily quota that was reached
	if c.limit15Min > 0 && c.usage15Min < c.limit15Min {
		return true
	}
	return false
}

// FetchSummaryActivities retrieves summary activities created after the specified Unix epoch.
// Strava's /athlete/activities endpoint is paginated.
func (c *StravaClient) FetchSummaryActivities(after int64, page int, perPage int) ([]SummaryActivity, error) {
	if err := c.ensureValidToken(); err != nil {
		return nil, fmt.Errorf("token refresh check failed: %w", err)
	}

	apiURL := fmt.Sprintf(
		"https://www.strava.com/api/v3/athlete/activities?after=%d&page=%d&per_page=%d",
		after, page, perPage,
	)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create GET request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.config.AccessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch activities list: %w", err)
	}
	defer resp.Body.Close()

	c.updateRateLimits(resp)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, &RateLimitError{
				IsDaily: c.isDailyLimitExceeded(),
				Message: fmt.Sprintf("API request failed with status %s: %s", resp.Status, string(body)),
			}
		}
		return nil, fmt.Errorf("API request failed with status %s: %s", resp.Status, string(body))
	}

	var activities []SummaryActivity
	if err := json.NewDecoder(resp.Body).Decode(&activities); err != nil {
		return nil, fmt.Errorf("failed to parse activities list JSON: %w", err)
	}

	return activities, nil
}

// FetchActivityDetail retrieves the complete details of a specific activity as raw JSON bytes.
func (c *StravaClient) FetchActivityDetail(activityID int64) ([]byte, error) {
	if err := c.ensureValidToken(); err != nil {
		return nil, fmt.Errorf("token refresh check failed: %w", err)
	}

	apiURL := fmt.Sprintf("https://www.strava.com/api/v3/activities/%d", activityID)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create detail request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.config.AccessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute detail request: %w", err)
	}
	defer resp.Body.Close()

	c.updateRateLimits(resp)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, &RateLimitError{
				IsDaily: c.isDailyLimitExceeded(),
				Message: fmt.Sprintf("API request failed with status %s: %s", resp.Status, string(body)),
			}
		}
		return nil, fmt.Errorf("API request failed with status %s: %s", resp.Status, string(body))
	}

	return io.ReadAll(resp.Body)
}

// FetchActivityStreams retrieves time-series data streams for a specific activity.
func (c *StravaClient) FetchActivityStreams(activityID int64) ([]byte, error) {
	if err := c.ensureValidToken(); err != nil {
		return nil, fmt.Errorf("token refresh check failed: %w", err)
	}

	keys := "time,distance,latlng,altitude,velocity_smooth,heartrate,cadence,watts,temp,moving,grade_smooth"
	apiURL := fmt.Sprintf(
		"https://www.strava.com/api/v3/activities/%d/streams?keys=%s&key_by_type=true",
		activityID, keys,
	)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create streams request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.config.AccessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute streams request: %w", err)
	}
	defer resp.Body.Close()

	c.updateRateLimits(resp)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, &RateLimitError{
				IsDaily: c.isDailyLimitExceeded(),
				Message: fmt.Sprintf("API request failed with status %s: %s", resp.Status, string(body)),
			}
		}
		// Some activities (e.g., manual entries) do not have streams.
		// Return "null" for streams instead of failing the sync process.
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
			return []byte("null"), nil
		}
		return nil, fmt.Errorf("API request failed with status %s: %s", resp.Status, string(body))
	}

	return io.ReadAll(resp.Body)
}
