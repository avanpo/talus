package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// TokenResponse represents the JSON returned by Strava OAuth token endpoints.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // Unix epoch seconds
}

// RunOAuthFlow starts the OAuth process to get access and refresh tokens.
func RunOAuthFlow(dir string, clientID string, clientSecret string, useLocalhost bool) error {
	// 1. If we are NOT using local callback, fallback to manual code entry
	if !useLocalhost {
		authURL := fmt.Sprintf(
			"https://www.strava.com/oauth/authorize?client_id=%s&redirect_uri=http://localhost&response_type=code&scope=activity:read_all",
			clientID,
		)
		fmt.Printf("1. Open the following URL in your browser to authorize talus:\n\n%s\n\n", authURL)
		fmt.Printf("2. After authorizing, you will be redirected to a localhost URL. Copy the 'code' parameter from the URL bar (e.g., http://localhost/?state=&code=XYZ... -> XYZ).\n")
		fmt.Print("3. Enter the authorization code here: ")

		var code string
		if _, err := fmt.Scanln(&code); err != nil {
			return fmt.Errorf("failed to read authorization code: %w", err)
		}

		return exchangeAndSaveToken(dir, clientID, clientSecret, code)
	}

	// 2. Start local HTTP server
	port := 8080
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		// Port might be in use, fall back to manual or print error
		return fmt.Errorf("failed to start local server on port %d: %w (try with --no-localhost flag for manual authorization)", port, err)
	}
	defer listener.Close()

	codeChan := make(chan string, 1)
	errChan := make(chan error, 1)

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/callback" {
				http.NotFound(w, r)
				return
			}
			code := r.URL.Query().Get("code")
			if code == "" {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, "<html><body><h3>Error: Authorization code not found in request.</h3></body></html>")
				errChan <- fmt.Errorf("no authorization code in callback URL")
				return
			}

			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "<html><body style='font-family: sans-serif; text-align: center; margin-top: 100px;'><h2>Authorization Successful!</h2><p>You can close this window now and return to your terminal.</p></body></html>")
			codeChan <- code
		}),
	}

	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	authURL := fmt.Sprintf(
		"https://www.strava.com/oauth/authorize?client_id=%s&redirect_uri=http://localhost:%d/callback&response_type=code&scope=activity:read_all",
		clientID, port,
	)

	fmt.Printf("Open the following URL in your browser to authorize talus:\n\n%s\n\n", authURL)
	fmt.Println("Waiting for authorization callback on http://localhost:8080/callback ...")

	select {
	case code := <-codeChan:
		// Shut down the server gracefully
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)

		fmt.Println("Received authorization code. Exchanging for access token...")
		return exchangeAndSaveToken(dir, clientID, clientSecret, code)
	case err := <-errChan:
		return fmt.Errorf("oauth callback server error: %w", err)
	case <-time.After(5 * time.Minute):
		// Timeout
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		return fmt.Errorf("timeout waiting for authorization callback (5 minutes)")
	}
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
	// Create activities subdirectory too
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
