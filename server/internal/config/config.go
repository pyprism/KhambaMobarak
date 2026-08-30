// Package config provides configuration management for the Khamba server
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	AppName        = "khamba"
	DefaultPort    = 8080
	ConfigFileName = "config.json"

	// DefaultHeartbeatInterval mirrors the firmware's HEARTBEAT_INTERVAL. It's
	// only used to derive DefaultOfflineThresholdSeconds below.
	DefaultHeartbeatInterval = 60
	// DefaultOfflineThresholdSeconds is >=3x the firmware heartbeat interval,
	// so a couple of missed/delayed heartbeats don't flap a device offline.
	DefaultOfflineThresholdSeconds = 3 * DefaultHeartbeatInterval
	// DefaultRetentionDays is how long raw events are kept before pruning.
	DefaultRetentionDays = 7
)

// Config represents the server configuration
type Config struct {
	Port      int    `json:"port"`
	Host      string `json:"host"` // Bind address; empty means all interfaces
	DBPath    string `json:"db_path"`
	ConfigDir string `json:"-"` // Not stored in JSON

	// OfflineThresholdSeconds is how long a device can go without a heartbeat
	// before it's considered offline / an outage is recorded.
	OfflineThresholdSeconds int `json:"offline_threshold_seconds"`
	// RetentionDays is how long raw events are kept before pruning. 0 disables
	// the retention worker entirely.
	RetentionDays int `json:"retention_days"`
	// DisplayTimezone is an IANA timezone name (e.g. "America/New_York") used
	// to bucket daily outage summaries and "today/this month/this year"
	// boundaries. Empty means UTC.
	DisplayTimezone string `json:"display_timezone"`
}

// Location resolves DisplayTimezone to a *time.Location, defaulting to UTC
// when unset or invalid.
func (c *Config) Location() *time.Location {
	if c.DisplayTimezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(c.DisplayTimezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// GetConfigDir returns the XDG config directory for the application
func GetConfigDir() (string, error) {
	// Try XDG_CONFIG_HOME first
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		// Fall back to ~/.config
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		configHome = filepath.Join(homeDir, ".config")
	}

	configDir := filepath.Join(configHome, AppName)
	return configDir, nil
}

// GetDataDir returns the XDG data directory for the application
func GetDataDir() (string, error) {
	// Try XDG_DATA_HOME first
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		// Fall back to ~/.local/share
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		dataHome = filepath.Join(homeDir, ".local", "share")
	}

	dataDir := filepath.Join(dataHome, AppName)
	return dataDir, nil
}

// DefaultConfig returns the default configuration
func DefaultConfig() (*Config, error) {
	dataDir, err := GetDataDir()
	if err != nil {
		return nil, err
	}

	return &Config{
		Port:                    DefaultPort,
		DBPath:                  filepath.Join(dataDir, "khamba.db"),
		OfflineThresholdSeconds: DefaultOfflineThresholdSeconds,
		RetentionDays:           DefaultRetentionDays,
	}, nil
}

// Load loads the configuration from the config file
func Load() (*Config, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(configDir, ConfigFileName)

	// Check if config file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Return default config if file doesn't exist
		cfg, err := DefaultConfig()
		if err != nil {
			return nil, err
		}
		cfg.ConfigDir = configDir
		return cfg, cfg.Validate()
	}

	// Read and parse config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Fields added after the initial release use pointers here so a config
	// file written before they existed gets their defaults instead of a zero
	// value that Validate would otherwise reject (or that would silently
	// disable retention).
	var raw struct {
		Port                    int    `json:"port"`
		Host                    string `json:"host"`
		DBPath                  string `json:"db_path"`
		OfflineThresholdSeconds *int   `json:"offline_threshold_seconds"`
		RetentionDays           *int   `json:"retention_days"`
		DisplayTimezone         string `json:"display_timezone"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	cfg := Config{Port: raw.Port, Host: raw.Host, DBPath: raw.DBPath, DisplayTimezone: raw.DisplayTimezone}
	if raw.OfflineThresholdSeconds != nil {
		cfg.OfflineThresholdSeconds = *raw.OfflineThresholdSeconds
	} else {
		cfg.OfflineThresholdSeconds = DefaultOfflineThresholdSeconds
	}
	if raw.RetentionDays != nil {
		cfg.RetentionDays = *raw.RetentionDays
	} else {
		cfg.RetentionDays = DefaultRetentionDays
	}

	cfg.ConfigDir = configDir
	return &cfg, cfg.Validate()
}

// Validate rejects values that would otherwise fail later with vague errors.
func (c *Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if filepath.Clean(c.DBPath) == "." || c.DBPath == "" {
		return fmt.Errorf("db_path cannot be empty")
	}
	if c.OfflineThresholdSeconds < 1 {
		return fmt.Errorf("offline_threshold_seconds must be positive")
	}
	if c.RetentionDays < 0 {
		return fmt.Errorf("retention_days cannot be negative (use 0 to disable pruning)")
	}
	return nil
}

// Save saves the configuration to the config file
func (c *Config) Save() error {
	if err := c.Validate(); err != nil {
		return err
	}
	configDir, err := GetConfigDir()
	if err != nil {
		return err
	}

	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	configPath := filepath.Join(configDir, ConfigFileName)

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// EnsureDirectories ensures all required directories exist
func EnsureDirectories() error {
	configDir, err := GetConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	dataDir, err := GetDataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	return nil
}
