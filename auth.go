package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TokenResponse represents the JSON returned by Strava OAuth token endpoints.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // Unix epoch seconds
}

// RunTerminalOAuthFlow prompts the user in the terminal to authorize when running headlessly.
func RunTerminalOAuthFlow(dir string, clientID string, clientSecret string) error {
	if clientID == "" {
		clientID = os.Getenv("STRAVA_CLIENT_ID")
	}
	if clientSecret == "" {
		clientSecret = os.Getenv("STRAVA_CLIENT_SECRET")
	}

	if clientID == "" {
		fmt.Print("Enter your Strava Client ID: ")
		_, _ = fmt.Scanln(&clientID)
		clientID = strings.TrimSpace(clientID)
	}
	if clientSecret == "" {
		fmt.Print("Enter your Strava Client Secret: ")
		_, _ = fmt.Scanln(&clientSecret)
		clientSecret = strings.TrimSpace(clientSecret)
	}

	if clientID == "" || clientSecret == "" {
		return fmt.Errorf("both Client ID and Client Secret are required")
	}

	authURL := fmt.Sprintf(
		"https://www.strava.com/oauth/authorize?client_id=%s&redirect_uri=http://localhost&response_type=code&scope=activity:read_all",
		clientID,
	)
	fmt.Printf("\n1. Open this URL in your browser to authorize talus:\n\n%s\n\n", authURL)
	fmt.Println("2. Approve access. You will be redirected to an address like: http://localhost/?state=&code=XYZ...")
	fmt.Print("3. Paste the authorization code (or full redirected URL) here: ")

	var input string
	if _, err := fmt.Scanln(&input); err != nil {
		return fmt.Errorf("failed to read authorization input: %w", err)
	}
	input = strings.TrimSpace(input)

	code := input
	if strings.Contains(input, "code=") {
		u, err := url.Parse(input)
		if err == nil {
			code = u.Query().Get("code")
		} else {
			parts := strings.Split(input, "code=")
			if len(parts) > 1 {
				code = strings.Split(parts[1], "&")[0]
			}
		}
	}

	if code == "" {
		return fmt.Errorf("invalid authorization code")
	}

	return exchangeAndSaveToken(dir, clientID, clientSecret, code)
}

func exchangeAndSaveToken(dir string, clientID, clientSecret, code string) error {
	// Prepare token exchange request
	tokenURL := "https://www.strava.com/oauth/token"
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")

	resp, err := http.PostForm(tokenURL, form)
	if err != nil {
		return fmt.Errorf("failed to post token exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errData map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&errData)
		return fmt.Errorf("token exchange returned status %s: %v", resp.Status, errData)
	}

	var tokResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokResp); err != nil {
		return fmt.Errorf("failed to decode token response: %w", err)
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "activities"), 0755); err != nil {
		return fmt.Errorf("failed to create activities subdirectory: %w", err)
	}

	cfg := &Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AccessToken:  tokResp.AccessToken,
		RefreshToken: tokResp.RefreshToken,
		ExpiresAt:    time.Unix(tokResp.ExpiresAt, 0),
	}

	if err := SaveConfig(dir, cfg); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Configuration and tokens successfully saved to %s\n", filepath.Join(dir, configFilename))
	return nil
}
