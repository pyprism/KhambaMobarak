package models

import (
	"os"
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

func TestRecordEventIsIdempotentForDeviceEventID(t *testing.T) {
	setupTestDB(t)
	device, _, err := CreateDevice("Idempotent", "Lab")
	if err != nil {
		t.Fatal(err)
	}
	first, err := RecordEvent(device.ID, EventTypeHeartbeat, "evt-123", "software")
	if err != nil {
		t.Fatal(err)
	}
	second, err := RecordEvent(device.ID, EventTypeHeartbeat, "evt-123", "software")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("retry created a second event: %d != %d", first.ID, second.ID)
	}
	_, total, err := GetDeviceEvents(device.ID, 10, 0)
	if err != nil || total != 1 {
		t.Fatalf("expected one stored event, total=%d err=%v", total, err)
	}
}

func TestAvailabilityReportAndBackup(t *testing.T) {
	setupTestDB(t)
	device, _, err := CreateDevice("Report", "HQ")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := DB.Create(&Outage{DeviceID: device.ID, StartTime: now.Add(-time.Hour), EndTime: &now, Duration: int64(time.Hour), Cause: "connectivity", Confidence: "inferred"}).Error; err != nil {
		t.Fatal(err)
	}
	report, err := GetAvailabilityReport(device.ID, now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.OutageCount != 1 || report.UptimePercent <= 90 || report.UptimePercent >= 100 {
		t.Fatalf("unexpected report: %+v", report)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := BackupDatabase(backup); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(backup); err != nil || info.Size() == 0 {
		t.Fatalf("backup not created: %v", err)
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

	var remainingEvents int64
	if err := DB.Model(&Event{}).Count(&remainingEvents).Error; err != nil {
		t.Fatalf("failed to count events: %v", err)
	}
	if remainingEvents != 0 {
		t.Fatalf("expected no events after analytics reset, got %d", remainingEvents)
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

func TestDeleteDeviceRemovesRecord(t *testing.T) {
	setupTestDB(t)

	device, _, err := CreateDevice("Delete Me", "Tmp")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	if err := DeleteDevice(device.ID); err != nil {
		t.Fatalf("DeleteDevice failed: %v", err)
	}

	if _, err := GetDeviceByID(device.ID); err == nil {
		t.Fatalf("expected deleted device lookup to fail")
	}
}

func TestDeleteDeviceHardDeletesAllowingTokenReuse(t *testing.T) {
	setupTestDB(t)

	device, token, err := CreateDevice("Reuse Me", "Tmp")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}
	if err := DB.Create(&MaintenanceWindow{DeviceID: &device.ID, StartTime: time.Now(), EndTime: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatalf("failed to create maintenance window: %v", err)
	}

	if err := DeleteDevice(device.ID); err != nil {
		t.Fatalf("DeleteDevice failed: %v", err)
	}

	var deviceCount int64
	if err := DB.Unscoped().Model(&Device{}).Where("id = ?", device.ID).Count(&deviceCount).Error; err != nil {
		t.Fatalf("failed to count devices: %v", err)
	}
	if deviceCount != 0 {
		t.Fatalf("expected device row to be hard-deleted, found %d", deviceCount)
	}

	var windowCount int64
	if err := DB.Model(&MaintenanceWindow{}).Where("device_id = ?", device.ID).Count(&windowCount).Error; err != nil {
		t.Fatalf("failed to count maintenance windows: %v", err)
	}
	if windowCount != 0 {
		t.Fatalf("expected device-specific maintenance windows to be removed, found %d", windowCount)
	}

	if _, _, err := CreateDevice("Reuse Me Again", "Tmp"); err != nil {
		t.Fatalf("expected new device creation to succeed after delete: %v", err)
	}
	if token == "" {
		t.Fatalf("sanity: token should have been non-empty")
	}
}

func TestUpdateDeviceUpdatesNameAndLocation(t *testing.T) {
	setupTestDB(t)

	device, _, err := CreateDevice("Old Name", "Old Loc")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	newName := "New Name"
	updated, err := UpdateDevice(device.ID, &newName, nil)
	if err != nil {
		t.Fatalf("UpdateDevice failed: %v", err)
	}
	if updated.Name != "New Name" || updated.Location != "Old Loc" {
		t.Fatalf("unexpected device after partial update: %+v", updated)
	}

	newLocation := "New Loc"
	updated, err = UpdateDevice(device.ID, nil, &newLocation)
	if err != nil {
		t.Fatalf("UpdateDevice failed: %v", err)
	}
	if updated.Name != "New Name" || updated.Location != "New Loc" {
		t.Fatalf("unexpected device after second update: %+v", updated)
	}

	blank := "   "
	if _, err := UpdateDevice(device.ID, &blank, nil); err == nil {
		t.Fatalf("expected blank name to be rejected")
	}
}

func TestAcknowledgeOutagePersistsNotes(t *testing.T) {
	setupTestDB(t)

	device, _, err := CreateDevice("Ack Node", "Lab")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}
	end := time.Now()
	outage := Outage{DeviceID: device.ID, StartTime: end.Add(-time.Hour), EndTime: &end, Duration: int64(time.Hour), Cause: "connectivity", Confidence: "inferred"}
	if err := DB.Create(&outage).Error; err != nil {
		t.Fatalf("failed to create outage: %v", err)
	}

	acked, err := AcknowledgeOutage(outage.ID, "checked breaker panel")
	if err != nil {
		t.Fatalf("AcknowledgeOutage failed: %v", err)
	}
	if acked.AcknowledgedAt == nil {
		t.Fatalf("expected AcknowledgedAt to be set")
	}
	if acked.Notes != "checked breaker panel" {
		t.Fatalf("expected notes to be persisted, got %q", acked.Notes)
	}

	if _, err := AcknowledgeOutage(outage.ID+999, ""); err == nil {
		t.Fatalf("expected acknowledging a missing outage to error")
	}
}

func TestClassifyOutageMapsResetReasons(t *testing.T) {
	cases := []struct {
		reason              string
		wantCause, wantConf string
	}{
		{"", "connectivity", "inferred"},
		{"Power on", "power", "confirmed"},
		{"Brownout Reset", "power", "confirmed"},
		{"Deep-Sleep Wake", "planned", "confirmed"},
		{"Task Watchdog", "device-reset", "confirmed"},
		{"Exception/Panic", "device-reset", "confirmed"},
		{"External Reset", "device-reset", "confirmed"},
		{"something unrecognized", "connectivity", "inferred"},
	}
	for _, tc := range cases {
		cause, confidence := classifyOutage(tc.reason)
		if cause != tc.wantCause || confidence != tc.wantConf {
			t.Fatalf("classifyOutage(%q) = (%q, %q), want (%q, %q)", tc.reason, cause, confidence, tc.wantCause, tc.wantConf)
		}
	}
}

func TestGetBootEventsFiltersByType(t *testing.T) {
	setupTestDB(t)

	device, _, err := CreateDevice("Boot Node", "Lab")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	if _, err := RecordEvent(device.ID, EventTypeHeartbeat); err != nil {
		t.Fatalf("RecordEvent heartbeat failed: %v", err)
	}
	if _, err := RecordEvent(device.ID, EventTypeBoot); err != nil {
		t.Fatalf("RecordEvent boot failed: %v", err)
	}

	boots, err := GetBootEvents(device.ID, 10)
	if err != nil {
		t.Fatalf("GetBootEvents failed: %v", err)
	}
	if len(boots) != 1 {
		t.Fatalf("expected 1 boot event, got %d", len(boots))
	}
	if boots[0].EventType != EventTypeBoot {
		t.Fatalf("expected boot event type, got %q", boots[0].EventType)
	}
}

func TestGetOutagesDetectsGap(t *testing.T) {
	setupTestDB(t)

	device, _, err := CreateDevice("Gap Node", "Plant")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	base := time.Now().Add(-20 * time.Minute)
	events := []Event{
		{DeviceID: device.ID, EventType: EventTypeHeartbeat, Timestamp: base},
		{DeviceID: device.ID, EventType: EventTypeHeartbeat, Timestamp: base.Add(6 * time.Minute)},
	}
	if err := DB.Create(&events).Error; err != nil {
		t.Fatalf("failed creating test events: %v", err)
	}

	outages, err := GetOutages(device.ID, 10)
	if err != nil {
		t.Fatalf("GetOutages failed: %v", err)
	}
	if len(outages) == 0 {
		t.Fatalf("expected at least one outage from event gap")
	}
	if outages[0].Duration < 2*time.Minute {
		t.Fatalf("expected outage duration over threshold, got %s", outages[0].Duration)
	}
}

func TestGetAllOutagesAppliesLimit(t *testing.T) {
	setupTestDB(t)

	deviceA, _, err := CreateDevice("A", "One")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}
	deviceB, _, err := CreateDevice("B", "Two")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	base := time.Now().Add(-40 * time.Minute)
	older := base.Add(10 * time.Minute)
	newer := base.Add(20 * time.Minute)
	outageRecords := []Outage{
		{DeviceID: deviceA.ID, StartTime: base, EndTime: &older, Duration: int64(older.Sub(base)), Cause: "connectivity", Confidence: "inferred"},
		{DeviceID: deviceB.ID, StartTime: older, EndTime: &newer, Duration: int64(newer.Sub(older)), Cause: "connectivity", Confidence: "inferred"},
	}
	if err := DB.Create(&outageRecords).Error; err != nil {
		t.Fatalf("failed creating test outages: %v", err)
	}

	outages, err := GetAllOutages(1)
	if err != nil {
		t.Fatalf("GetAllOutages failed: %v", err)
	}
	if len(outages) != 1 {
		t.Fatalf("expected limit to return one outage, got %d", len(outages))
	}
}

func TestGetEventChartDataAggregatesCounts(t *testing.T) {
	setupTestDB(t)

	device, _, err := CreateDevice("Chart Node", "Ops")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	now := time.Now()
	events := []Event{
		{DeviceID: device.ID, EventType: EventTypeHeartbeat, Timestamp: now.Add(-20 * time.Minute)},
		{DeviceID: device.ID, EventType: EventTypeBoot, Timestamp: now.Add(-10 * time.Minute)},
	}
	if err := DB.Create(&events).Error; err != nil {
		t.Fatalf("failed creating chart events: %v", err)
	}

	buckets, err := GetEventChartData(device.ID, 1, 6)
	if err != nil {
		t.Fatalf("GetEventChartData failed: %v", err)
	}
	if len(buckets) != 6 {
		t.Fatalf("expected 6 buckets, got %d", len(buckets))
	}

	total := 0
	for _, b := range buckets {
		total += b.Total
	}
	if total < 2 {
		t.Fatalf("expected chart to include inserted events, total=%d", total)
	}
}

func TestChartBucketLabelUses12HourClock(t *testing.T) {
	timestamp := time.Date(2026, time.May, 14, 15, 4, 0, 0, time.UTC)

	if got := chartBucketLabel(timestamp, 10*time.Minute); got != "3:04 pm" {
		t.Fatalf("expected minute bucket to use 12-hour time, got %q", got)
	}
	if got := chartBucketLabel(timestamp, 2*time.Hour); got != "May 14 3:00 pm" {
		t.Fatalf("expected hourly bucket to use 12-hour time, got %q", got)
	}
	if got := chartBucketLabel(timestamp, 48*time.Hour); got != "May 14" {
		t.Fatalf("expected multi-day bucket to omit the clock, got %q", got)
	}
}

func TestDeleteOldEventsRemovesStaleRows(t *testing.T) {
	setupTestDB(t)

	device, _, err := CreateDevice("Retention Node", "Lab")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	old := time.Now().Add(-40 * 24 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)

	events := []Event{
		{DeviceID: device.ID, EventType: EventTypeHeartbeat, Timestamp: old},
		{DeviceID: device.ID, EventType: EventTypeHeartbeat, Timestamp: recent},
	}
	if err := DB.Create(&events).Error; err != nil {
		t.Fatalf("failed to create test events: %v", err)
	}

	deleted, err := DeleteOldEvents(30)
	if err != nil {
		t.Fatalf("DeleteOldEvents failed: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 event deleted, got %d", deleted)
	}

	remaining, total, err := GetDeviceEvents(device.ID, 10, 0)
	if err != nil {
		t.Fatalf("GetDeviceEvents failed: %v", err)
	}
	if total != 1 || len(remaining) != 1 {
		t.Fatalf("expected 1 remaining event, got total=%d len=%d", total, len(remaining))
	}
	if !remaining[0].Timestamp.Equal(recent) {
		t.Fatalf("expected the recent event to remain, got %v", remaining[0].Timestamp)
	}
}

// TestRecordEventPersistsOutageOnHeartbeatRecovery locks in the fix for the
// bug where an outage only closed on a boot event: a device that never
// rebooted (WiFi/router drop, not a power loss) but recovers via a plain
// heartbeat must still get a persisted Outage row, not just a vanished gap.
func TestRecordEventPersistsOutageOnHeartbeatRecovery(t *testing.T) {
	setupTestDB(t)

	device, _, err := CreateDevice("Recovery Node", "Lab")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	old := time.Now().Add(-10 * time.Minute)
	if err := DB.Create(&Event{DeviceID: device.ID, EventType: EventTypeHeartbeat, Timestamp: old}).Error; err != nil {
		t.Fatalf("failed to create heartbeat event: %v", err)
	}
	if err := DB.Model(&Device{}).Where("id = ?", device.ID).Update("last_seen", old).Error; err != nil {
		t.Fatalf("failed to set last_seen: %v", err)
	}

	if _, err := RecordEvent(device.ID, EventTypeHeartbeat); err != nil {
		t.Fatalf("RecordEvent failed: %v", err)
	}

	var count int64
	if err := DB.Model(&Outage{}).Where("device_id = ?", device.ID).Count(&count).Error; err != nil {
		t.Fatalf("failed to count outages: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 persisted outage after heartbeat recovery, got %d", count)
	}

	stats, err := GetDeviceOutageStats(device.ID)
	if err != nil {
		t.Fatalf("GetDeviceOutageStats failed: %v", err)
	}
	if stats.TodayCount != 1 {
		t.Fatalf("expected TodayCount=1, got %d", stats.TodayCount)
	}
}

func TestGetDeviceOutageStatsReturnsZeroWhenNoData(t *testing.T) {
	setupTestDB(t)

	device, _, err := CreateDevice("Zero Node", "Basement")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	stats, err := GetDeviceOutageStats(device.ID)
	if err != nil {
		t.Fatalf("GetDeviceOutageStats failed: %v", err)
	}
	if stats.TodayCount != 0 || stats.MonthCount != 0 || stats.YearCount != 0 {
		t.Fatalf("expected zero stats for new device, got %+v", stats)
	}
}

func TestRecordBootEventTriggersSummaryUpdate(t *testing.T) {
	setupTestDB(t)

	device, _, err := CreateDevice("AutoSummary Node", "Field")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	// Insert a heartbeat directly to simulate an outage gap.
	oldHB := Event{DeviceID: device.ID, EventType: EventTypeHeartbeat, Timestamp: time.Now().Add(-15 * time.Minute)}
	if err := DB.Create(&oldHB).Error; err != nil {
		t.Fatalf("failed to create heartbeat: %v", err)
	}

	// Recording a boot event should automatically update the summary.
	if _, err := RecordEvent(device.ID, EventTypeBoot); err != nil {
		t.Fatalf("RecordEvent boot failed: %v", err)
	}

	stats, err := GetDeviceOutageStats(device.ID)
	if err != nil {
		t.Fatalf("GetDeviceOutageStats failed: %v", err)
	}
	if stats.TodayCount != 1 {
		t.Fatalf("expected TodayCount=1 after boot event, got %d", stats.TodayCount)
	}
}
