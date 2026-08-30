package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetConfigDirUsesXDGConfigHome(t *testing.T) {
	xdgConfig := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)

	configDir, err := GetConfigDir()
	if err != nil {
		t.Fatalf("GetConfigDir failed: %v", err)
	}

	expected := filepath.Join(xdgConfig, AppName)
	if configDir != expected {
		t.Fatalf("expected config dir %q, got %q", expected, configDir)
	}
}

func TestGetDataDirUsesXDGDataHome(t *testing.T) {
	xdgData := filepath.Join(t.TempDir(), "xdg-data")
	t.Setenv("XDG_DATA_HOME", xdgData)

	dataDir, err := GetDataDir()
	if err != nil {
		t.Fatalf("GetDataDir failed: %v", err)
	}

	expected := filepath.Join(xdgData, AppName)
	if dataDir != expected {
		t.Fatalf("expected data dir %q, got %q", expected, dataDir)
	}
}

func TestLoadReturnsDefaultConfigWhenMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Port != DefaultPort {
		t.Fatalf("expected default port %d, got %d", DefaultPort, cfg.Port)
	}
	if cfg.DBPath == "" {
		t.Fatalf("expected non-empty default DB path")
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	xdgConfig := filepath.Join(t.TempDir(), "cfg")
	xdgData := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_DATA_HOME", xdgData)

	cfg := &Config{Port: 9090, DBPath: filepath.Join(xdgData, "custom.db"), OfflineThresholdSeconds: DefaultOfflineThresholdSeconds, RetentionDays: DefaultRetentionDays}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Port != 9090 {
		t.Fatalf("expected loaded port 9090, got %d", loaded.Port)
	}
	if loaded.DBPath != filepath.Join(xdgData, "custom.db") {
		t.Fatalf("unexpected loaded DB path: %q", loaded.DBPath)
	}
}

func TestEnsureDirectoriesCreatesConfigAndDataDirs(t *testing.T) {
	xdgConfig := filepath.Join(t.TempDir(), "cfg")
	xdgData := filepath.Join(t.TempDir(), "data")
	t.Setenv("XDG_CONFIG_HOME", xdgConfig)
	t.Setenv("XDG_DATA_HOME", xdgData)

	if err := EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories failed: %v", err)
	}

	configDir, _ := GetConfigDir()
	dataDir, _ := GetDataDir()

	if _, err := os.Stat(configDir); err != nil {
		t.Fatalf("expected config dir to exist: %v", err)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("expected data dir to exist: %v", err)
	}
}

func TestValidateRejectsInvalidConfiguration(t *testing.T) {
	valid := Config{Port: 8080, DBPath: "data.db", OfflineThresholdSeconds: DefaultOfflineThresholdSeconds, RetentionDays: DefaultRetentionDays}
	invalidCases := []Config{valid, valid, valid, valid, valid}
	invalidCases[0].Port = 0
	invalidCases[1].Port = 65536
	invalidCases[2].DBPath = ""
	invalidCases[3].OfflineThresholdSeconds = 0
	invalidCases[4].RetentionDays = -1
	for _, cfg := range invalidCases {
		cfg := cfg
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected invalid config %+v to be rejected", cfg)
		}
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("expected valid config: %v", err)
	}
}

func TestLocationDefaultsToUTC(t *testing.T) {
	cfg := &Config{DisplayTimezone: ""}
	if cfg.Location() != time.UTC {
		t.Fatalf("expected empty timezone to resolve to UTC")
	}
	cfg.DisplayTimezone = "not-a-real-timezone"
	if cfg.Location() != time.UTC {
		t.Fatalf("expected invalid timezone to fall back to UTC")
	}
	cfg.DisplayTimezone = "America/New_York"
	if cfg.Location().String() != "America/New_York" {
		t.Fatalf("expected named timezone to resolve, got %v", cfg.Location())
	}
}
