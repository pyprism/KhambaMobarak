package models

import (
	"path/filepath"
	"testing"
	"time"
)

func setupTestDB(t *testing.T) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "khamba-test.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
}

func TestCreateDeviceAndGetDeviceByToken(t *testing.T) {
	setupTestDB(t)

	created, token, err := CreateDevice("Kitchen", "Home")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}
	if token == "" {
		t.Fatalf("expected non-empty token")
	}
	if created.ID == 0 {
		t.Fatalf("expected persisted device ID")
	}

	found, err := GetDeviceByToken(token)
	if err != nil {
		t.Fatalf("GetDeviceByToken failed: %v", err)
	}
	if found.Name != "Kitchen" || found.Location != "Home" {
		t.Fatalf("unexpected device values: %+v", found)
	}
}

func TestRecordEventUpdatesLastSeenAndReturnsEvents(t *testing.T) {
	setupTestDB(t)

	device, _, err := CreateDevice("Office", "Floor 2")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	if _, err := RecordEvent(device.ID, EventTypeBoot); err != nil {
		t.Fatalf("RecordEvent boot failed: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := RecordEvent(device.ID, EventTypeHeartbeat); err != nil {
		t.Fatalf("RecordEvent heartbeat failed: %v", err)
	}

	updated, err := GetDeviceByID(device.ID)
	if err != nil {
		t.Fatalf("GetDeviceByID failed: %v", err)
	}
	if updated.LastSeen == nil {
		t.Fatalf("expected LastSeen to be updated after event")
	}

	events, total, err := GetDeviceEvents(device.ID, 10, 0)
	if err != nil {
		t.Fatalf("GetDeviceEvents failed: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected total=2, got %d", total)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].EventType != EventTypeHeartbeat {
		t.Fatalf("expected latest event heartbeat, got %q", events[0].EventType)
	}
}

func TestResetAnalyticsDataKeepsDevicesAndTokens(t *testing.T) {
	setupTestDB(t)

	deviceA, tokenA, err := CreateDevice("Node A", "Alpha")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}
	deviceB, tokenB, err := CreateDevice("Node B", "Beta")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	if _, err := RecordEvent(deviceA.ID, EventTypeBoot); err != nil {
		t.Fatalf("RecordEvent failed: %v", err)
	}
	if _, err := RecordEvent(deviceB.ID, EventTypeHeartbeat); err != nil {
		t.Fatalf("RecordEvent failed: %v", err)
	}

	if err := ResetAnalyticsData(); err != nil {
		t.Fatalf("ResetAnalyticsData failed: %v", err)
	}

	allDevices, err := GetAllDevices()
	if err != nil {
		t.Fatalf("GetAllDevices failed: %v", err)
	}
	if len(allDevices) != 2 {
		t.Fatalf("expected 2 devices after reset, got %d", len(allDevices))
	}

	for _, d := range allDevices {
		if d.LastSeen != nil {
			t.Fatalf("expected LastSeen=nil after analytics reset, got %v", d.LastSeen)
		}
	}

	foundA, err := GetDeviceByToken(tokenA)
	if err != nil || foundA.ID == 0 {
		t.Fatalf("expected token A to remain valid, err=%v", err)
	}
	foundB, err := GetDeviceByToken(tokenB)
	if err != nil || foundB.ID == 0 {
		t.Fatalf("expected token B to remain valid, err=%v", err)
	}

	recent, err := GetRecentEvents(10)
	if err != nil {
		t.Fatalf("GetRecentEvents failed: %v", err)
	}
	if len(recent) != 0 {
		t.Fatalf("expected no events after analytics reset, got %d", len(recent))
	}
}

func TestGetOrCreateDeviceByNameCreatesWhenMissing(t *testing.T) {
	setupTestDB(t)

	device, token, created, err := GetOrCreateDeviceByName("Dummy CLI", "Local")
	if err != nil {
		t.Fatalf("GetOrCreateDeviceByName failed: %v", err)
	}
	if !created {
		t.Fatalf("expected created=true for missing device")
	}
	if device.ID == 0 {
		t.Fatalf("expected persisted device ID")
	}
	if token == "" || token != device.Token {
		t.Fatalf("expected non-empty token matching device token")
	}
}

func TestGetOrCreateDeviceByNameReusesExistingToken(t *testing.T) {
	setupTestDB(t)

	createdDevice, createdToken, err := CreateDevice("Dummy CLI", "A")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	device, token, created, err := GetOrCreateDeviceByName("Dummy CLI", "B")
	if err != nil {
		t.Fatalf("GetOrCreateDeviceByName failed: %v", err)
	}
	if created {
		t.Fatalf("expected created=false when device already exists")
	}
	if device.ID != createdDevice.ID {
		t.Fatalf("expected existing device ID %d, got %d", createdDevice.ID, device.ID)
	}
	if token != createdToken {
		t.Fatalf("expected existing token to be reused")
	}
}
