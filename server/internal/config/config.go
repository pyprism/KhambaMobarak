// Package config provides configuration management for the Khamba server
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	AppName        = "khamba"
	DefaultPort    = 8080
	ConfigFileName = "config.json"
)

// Config represents the server configuration
type Config struct {
	Port      int    `json:"port"`
	DBPath    string `json:"db_path"`
	ConfigDir string `json:"-"` // Not stored in JSON
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
		Port:   DefaultPort,
		DBPath: filepath.Join(dataDir, "khamba.db"),
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
		return cfg, nil
	}

	// Read and parse config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	cfg.ConfigDir = configDir
	return &cfg, nil
}

// Save saves the configuration to the config file
func (c *Config) Save() error {
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
