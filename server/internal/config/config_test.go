package config

import (
	"os"
	"path/filepath"
	"testing"
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

	cfg := &Config{Port: 9090, DBPath: filepath.Join(xdgData, "custom.db")}
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
