package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Config holds the Strava API credentials and tokens.
type Config struct {
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Metadata stores the state of the backup directory.
type Metadata struct {
	LastActivityTimestamp int64     `json:"last_activity_timestamp"` // Unix timestamp (seconds) of the newest activity
	LastSyncTime          time.Time `json:"last_sync_time"`
	TotalActivities       int       `json:"total_activities"`
}

const (
	configFilename   = ".talus-config.json"
	metadataFilename = "metadata.json"
)

// LoadConfig reads the configuration file from the backup directory.
func LoadConfig(dir string) (*Config, error) {
	path := filepath.Join(dir, configFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SaveConfig writes the configuration file to the backup directory.
func SaveConfig(dir string, cfg *Config) error {
	path := filepath.Join(dir, configFilename)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Write with read/write permissions for user only (0600) for security.
	return os.WriteFile(path, data, 0600)
}

// LoadMetadata reads the sync metadata from the backup directory.
func LoadMetadata(dir string) (*Metadata, error) {
	path := filepath.Join(dir, metadataFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return default metadata if file does not exist
			return &Metadata{}, nil
		}
		return nil, err
	}
	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// SaveMetadata writes the sync metadata to the backup directory.
func SaveMetadata(dir string, meta *Metadata) error {
	path := filepath.Join(dir, metadataFilename)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
