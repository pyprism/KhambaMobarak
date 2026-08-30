// Package models provides database access and operations
package models

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB is the global database connection
var DB *gorm.DB

// InitDB initializes the database connection
func InitDB(dbPath string) error {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
	}

	// Configure GORM logger
	gormLogger := logger.Default.LogMode(logger.Warn)

	// Open database
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormLogger,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	// Enable WAL mode for better concurrency
	if err := db.Exec("PRAGMA journal_mode=WAL;").Error; err != nil {
		return fmt.Errorf("failed to enable WAL: %w", err)
	}
	if err := db.Exec("PRAGMA busy_timeout=5000;").Error; err != nil {
		return fmt.Errorf("failed to set busy timeout: %w", err)
	}

	// Auto migrate schemas
	if err := db.AutoMigrate(&Device{}, &Event{}, &DailyOutageSummary{}, &Outage{}, &DailyEventRollup{}, &MaintenanceWindow{}); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_events_device_event_id_nonempty ON events(device_id, event_id) WHERE event_id <> ''").Error; err != nil {
		return fmt.Errorf("failed to create event id index: %w", err)
	}
	if err := db.Exec("CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL)").Error; err != nil {
		return fmt.Errorf("failed to create migration ledger: %w", err)
	}
	if err := db.Exec("INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, CURRENT_TIMESTAMP)").Error; err != nil {
		return fmt.Errorf("failed to record schema version: %w", err)
	}

	DB = db
	return nil
}

// BackupDatabase creates a SQLite-consistent snapshot, including WAL content.
func BackupDatabase(destination string) error {
	if strings.TrimSpace(destination) == "" {
		return errors.New("backup path cannot be empty")
	}
	escaped := strings.ReplaceAll(destination, "'", "''")
	if err := DB.Exec("VACUUM INTO '" + escaped + "'").Error; err != nil {
		return fmt.Errorf("failed to create backup: %w", err)
	}
	return nil
}

// CreateDevice creates a new device with a generated token
func CreateDevice(name, location string) (*Device, string, error) {
	token, err := GenerateToken()
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate token: %w", err)
	}

	device := &Device{
		Name:     name,
		Location: location,
		Token:    token,
	}

	if err := DB.Create(device).Error; err != nil {
		return nil, "", fmt.Errorf("failed to create device: %w", err)
	}

	return device, token, nil
}

// GetDeviceByToken retrieves a device by its token
func GetDeviceByToken(token string) (*Device, error) {
	var device Device
	if err := DB.Where("token = ?", token).First(&device).Error; err != nil {
		return nil, err
	}
	return &device, nil
}

// GetAllDevices retrieves all devices
func GetAllDevices() ([]Device, error) {
	var devices []Device
	if err := DB.Order("name ASC").Find(&devices).Error; err != nil {
		return nil, err
	}
	return devices, nil
}

// GetDeviceByID retrieves a device by its ID
func GetDeviceByID(id uint) (*Device, error) {
	var device Device
	if err := DB.First(&device, id).Error; err != nil {
		return nil, err
	}
	return &device, nil
}

// GetDeviceByName retrieves the first device by name.
func GetDeviceByName(name string) (*Device, error) {
	var device Device
	if err := DB.Where("name = ?", name).Order("id ASC").First(&device).Error; err != nil {
		return nil, err
	}
	return &device, nil
}

// GetOrCreateDeviceByName reuses an existing device token by name or creates a new device.
func GetOrCreateDeviceByName(name, location string) (*Device, string, bool, error) {
	device, err := GetDeviceByName(name)
	if err == nil {
		return device, device.Token, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", false, fmt.Errorf("failed to query device by name: %w", err)
	}

	createdDevice, token, err := CreateDevice(name, location)
	if err != nil {
		return nil, "", false, err
	}

	return createdDevice, token, true, nil
}

// DeleteDevice deletes a device by ID, along with all of its events,
// summaries, outages, and device-specific maintenance windows. The device
// row itself is hard-deleted so its token can be reused.
func DeleteDevice(id uint) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&Event{}, "device_id = ?", id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&DailyOutageSummary{}, "device_id = ?", id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&DailyEventRollup{}, "device_id = ?", id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&Outage{}, "device_id = ?", id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&MaintenanceWindow{}, "device_id = ?", id).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(&Device{}, id).Error
	})
}

// UpdateDevice updates the name and/or location of a device. Passing nil for
// a field leaves it unchanged.
func UpdateDevice(id uint, name, location *string) (*Device, error) {
	updates := map[string]interface{}{}
	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" {
			return nil, errors.New("name cannot be empty")
		}
		updates["name"] = trimmed
	}
	if location != nil {
		updates["location"] = *location
	}
	if len(updates) > 0 {
		if err := DB.Model(&Device{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("failed to update device: %w", err)
		}
	}
	return GetDeviceByID(id)
}

// RecordEvent records a new event for a device
func RecordEvent(deviceID uint, eventType string, metadata ...string) (*Event, error) {
	now := time.Now()
	eventID, resetReason := "", ""
	if len(metadata) > 0 {
		eventID = metadata[0]
	}
	if len(metadata) > 1 {
		resetReason = metadata[1]
	}
	if eventID == "" {
		eventID = fmt.Sprintf("server-%d", now.UnixNano())
	}
	event := &Event{DeviceID: deviceID, EventID: eventID, EventType: eventType, Timestamp: now, ResetReason: resetReason}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if eventID != "" {
			var existing Event
			err := tx.Where("device_id = ? AND event_id = ?", deviceID, eventID).First(&existing).Error
			if err == nil {
				*event = existing
				return nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		var device Device
		if err := tx.First(&device, deviceID).Error; err != nil {
			return err
		}
		// Copy the value rather than aliasing device.LastSeen: GORM's Update
		// below mutates the pointee of an existing pointer field in place, so
		// holding onto the pointer itself would silently turn "previous" into
		// "now" before the gap comparison runs.
		var previous *time.Time
		if device.LastSeen != nil {
			t := *device.LastSeen
			previous = &t
		} else {
			var last Event
			if err := tx.Where("device_id = ?", deviceID).Order("timestamp DESC").First(&last).Error; err == nil {
				previous = &last.Timestamp
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		if err := tx.Create(event).Error; err != nil {
			if eventID != "" && isUniqueConstraintErr(err) {
				// Lost a race with a concurrent retry of the same event; treat as a
				// successful dedupe instead of surfacing a 500.
				var existing Event
				if lookupErr := tx.Where("device_id = ? AND event_id = ?", deviceID, eventID).First(&existing).Error; lookupErr == nil {
					*event = existing
					return nil
				}
			}
			return err
		}
		if err := tx.Model(&device).Update("last_seen", now).Error; err != nil {
			return err
		}
		if err := updateDailyRollup(tx, deviceID, now, eventType); err != nil {
			return err
		}
		if previous != nil && now.Sub(*previous) > OfflineThreshold {
			// A reset reason only speaks to *this* event; it's only trustworthy
			// evidence of what caused the gap when the device actually rebooted
			// to send it. A heartbeat-only recovery means the device never
			// restarted, so the gap is a connectivity interruption, full stop.
			outageResetReason := ""
			if eventType == EventTypeBoot {
				outageResetReason = resetReason
			}
			if err := createOutage(tx, deviceID, *previous, now, outageResetReason); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to record event: %w", err)
	}
	return event, nil
}

// isUniqueConstraintErr reports whether err came from a SQLite UNIQUE
// constraint violation (e.g. a racing insert of the same device/event_id).
func isUniqueConstraintErr(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// dayBucket truncates t to midnight in DisplayLocation.
func dayBucket(t time.Time) time.Time {
	t = t.In(DisplayLocation)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, DisplayLocation)
}

// classifyOutage infers an outage's cause/confidence from the device's
// self-reported reset reason. An empty reason (heartbeat-only recovery, or
// firmware that doesn't report one) always yields the conservative default.
func classifyOutage(resetReason string) (cause, confidence string) {
	r := strings.ToLower(resetReason)
	switch {
	case r == "":
		return "connectivity", "inferred"
	case strings.Contains(r, "brownout"), strings.Contains(r, "power"):
		return "power", "confirmed"
	case strings.Contains(r, "deep-sleep"), strings.Contains(r, "deep sleep"):
		return "planned", "confirmed"
	case strings.Contains(r, "watchdog"), strings.Contains(r, "panic"),
		strings.Contains(r, "exception"), strings.Contains(r, "software"),
		strings.Contains(r, "fatal"), strings.Contains(r, "external"):
		return "device-reset", "confirmed"
	default:
		return "connectivity", "inferred"
	}
}

func updateDailyRollup(tx *gorm.DB, deviceID uint, now time.Time, eventType string) error {
	day := dayBucket(now)
	var rollup DailyEventRollup
	err := tx.Where("device_id = ? AND date = ?", deviceID, day).First(&rollup).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		rollup = DailyEventRollup{DeviceID: deviceID, Date: day}
		if eventType == EventTypeBoot {
			rollup.Boots = 1
		} else {
			rollup.Heartbeats = 1
		}
		return tx.Create(&rollup).Error
	}
	if err != nil {
		return err
	}
	if eventType == EventTypeBoot {
		return tx.Model(&rollup).Update("boots", rollup.Boots+1).Error
	}
	return tx.Model(&rollup).Update("heartbeats", rollup.Heartbeats+1).Error
}

func createOutage(tx *gorm.DB, deviceID uint, start, end time.Time, resetReason string) error {
	suppressed, err := IsMaintenanceActive(tx, deviceID, start, end)
	if err != nil {
		return err
	}
	cause, confidence := classifyOutage(resetReason)
	outage := Outage{DeviceID: deviceID, StartTime: start, EndTime: &end, Duration: int64(end.Sub(start)), Cause: cause, Confidence: confidence, Suppressed: suppressed}
	if err := tx.Create(&outage).Error; err != nil {
		return err
	}
	day := dayBucket(start)
	var summary DailyOutageSummary
	err = tx.Where("device_id = ? AND date = ?", deviceID, day).First(&summary).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&DailyOutageSummary{DeviceID: deviceID, Date: day, OutageCount: 1, TotalDowntime: outage.Duration}).Error
	}
	if err != nil {
		return err
	}
	return tx.Model(&summary).Updates(map[string]interface{}{"outage_count": summary.OutageCount + 1, "total_downtime": summary.TotalDowntime + outage.Duration}).Error
}

// GetDeviceEvents retrieves events for a device with pagination
func GetDeviceEvents(deviceID uint, limit, offset int) ([]Event, int64, error) {
	var events []Event
	var total int64

	db := DB.Model(&Event{}).Where("device_id = ?", deviceID)

	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := db.Order("timestamp DESC").Limit(limit).Offset(offset).Find(&events).Error; err != nil {
		return nil, 0, err
	}

	return events, total, nil
}

// GetBootEvents retrieves boot events (power restorations) for a device
func GetBootEvents(deviceID uint, limit int) ([]Event, error) {
	var events []Event
	if err := DB.Where("device_id = ? AND event_type = ?", deviceID, EventTypeBoot).
		Order("timestamp DESC").
		Limit(limit).
		Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

// ongoingOutageInfos derives OutageInfo entries for devices that are
// currently offline. These have no persisted Outage row yet (RecordEvent
// only creates one once the device recovers), so they're computed from
// Device.LastSeen. Pass deviceID to scope to one device, or nil for all.
func ongoingOutageInfos(deviceID *uint) ([]OutageInfo, error) {
	cutoff := time.Now().Add(-OfflineThreshold)
	q := DB.Where("last_seen IS NOT NULL AND last_seen < ?", cutoff)
	if deviceID != nil {
		q = q.Where("id = ?", *deviceID)
	}
	var devices []Device
	if err := q.Find(&devices).Error; err != nil {
		return nil, err
	}
	infos := make([]OutageInfo, 0, len(devices))
	for _, d := range devices {
		infos = append(infos, OutageInfo{DeviceID: d.ID, DeviceName: d.Name, Location: d.Location, StartTime: *d.LastSeen, Duration: time.Since(*d.LastSeen), IsOngoing: true, Cause: "connectivity", Confidence: "inferred"})
	}
	return infos, nil
}

// GetOutages returns outage periods for a device, most recent first.
func GetOutages(deviceID uint, limit int) ([]OutageInfo, error) {
	device, err := GetDeviceByID(deviceID)
	if err != nil {
		return nil, err
	}
	var records []Outage
	if err := DB.Where("device_id = ?", deviceID).Order("start_time DESC").Find(&records).Error; err != nil {
		return nil, err
	}
	outages := make([]OutageInfo, 0, len(records)+1)
	for _, record := range records {
		outages = append(outages, OutageInfo{ID: record.ID, DeviceID: deviceID, DeviceName: device.Name, Location: device.Location, StartTime: record.StartTime, EndTime: record.EndTime, Duration: time.Duration(record.Duration), Cause: record.Cause, Confidence: record.Confidence, Suppressed: record.Suppressed, AcknowledgedAt: record.AcknowledgedAt, Notes: record.Notes})
	}
	// Compatibility for databases populated before outage records were introduced.
	// New writes never need this path because RecordEvent persists the incident.
	if len(records) == 0 {
		var events []Event
		if err := DB.Where("device_id = ?", deviceID).Order("timestamp ASC").Find(&events).Error; err != nil {
			return nil, err
		}
		for i := 1; i < len(events); i++ {
			gap := events[i].Timestamp.Sub(events[i-1].Timestamp)
			if gap > OfflineThreshold {
				end := events[i].Timestamp
				outages = append(outages, OutageInfo{DeviceID: deviceID, DeviceName: device.Name, Location: device.Location, StartTime: events[i-1].Timestamp, EndTime: &end, Duration: gap, Cause: "connectivity", Confidence: "inferred"})
			}
		}
	}
	ongoing, err := ongoingOutageInfos(&deviceID)
	if err != nil {
		return nil, err
	}
	outages = append(outages, ongoing...)
	sort.Slice(outages, func(i, j int) bool { return outages[i].StartTime.After(outages[j].StartTime) })

	// Apply limit
	if limit > 0 && len(outages) > limit {
		outages = outages[:limit]
	}

	return outages, nil
}

// GetAllOutages retrieves recent outages across all devices. Persisted
// outages are limited and ordered in SQL rather than loading every device's
// full history into Go; only the legacy pre-outage-table fallback (see
// GetOutages) is skipped here since it applies to at most a handful of
// devices from before this table existed and is still reachable per-device.
func GetAllOutages(limit int) ([]OutageInfo, error) {
	type outageRow struct {
		ID             uint
		DeviceID       uint
		DeviceName     string
		Location       string
		StartTime      time.Time
		EndTime        *time.Time
		Duration       int64
		Cause          string
		Confidence     string
		Suppressed     bool
		AcknowledgedAt *time.Time
		Notes          string
	}
	query := DB.Table("outages").
		Select("outages.id, outages.device_id, devices.name AS device_name, devices.location, outages.start_time, outages.end_time, outages.duration, outages.cause, outages.confidence, outages.suppressed, outages.acknowledged_at, outages.notes").
		Joins("JOIN devices ON devices.id = outages.device_id AND devices.deleted_at IS NULL").
		Order("outages.start_time DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	var rows []outageRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	all := make([]OutageInfo, 0, len(rows)+1)
	for _, r := range rows {
		all = append(all, OutageInfo{ID: r.ID, DeviceID: r.DeviceID, DeviceName: r.DeviceName, Location: r.Location, StartTime: r.StartTime, EndTime: r.EndTime, Duration: time.Duration(r.Duration), Cause: r.Cause, Confidence: r.Confidence, Suppressed: r.Suppressed, AcknowledgedAt: r.AcknowledgedAt, Notes: r.Notes})
	}
	ongoing, err := ongoingOutageInfos(nil)
	if err != nil {
		return nil, err
	}
	all = append(all, ongoing...)
	sort.Slice(all, func(i, j int) bool { return all[i].StartTime.After(all[j].StartTime) })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// CountOutages returns the total number of persisted outage records.
func CountOutages() (int64, error) {
	var count int64
	err := DB.Model(&Outage{}).Count(&count).Error
	return count, err
}

// CountOngoingOutages returns the number of devices currently offline (i.e.
// with a derived, not-yet-persisted ongoing outage).
func CountOngoingOutages() (int64, error) {
	cutoff := time.Now().Add(-OfflineThreshold)
	var count int64
	err := DB.Model(&Device{}).Where("last_seen IS NOT NULL AND last_seen < ?", cutoff).Count(&count).Error
	return count, err
}

// AcknowledgeOutage marks an outage as acknowledged, optionally attaching a note.
func AcknowledgeOutage(id uint, notes string) (*Outage, error) {
	updates := map[string]interface{}{"acknowledged_at": time.Now()}
	if notes != "" {
		updates["notes"] = notes
	}
	result := DB.Model(&Outage{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to acknowledge outage: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var outage Outage
	if err := DB.First(&outage, id).Error; err != nil {
		return nil, err
	}
	return &outage, nil
}

func IsMaintenanceActive(db *gorm.DB, deviceID uint, start, end time.Time) (bool, error) {
	var count int64
	err := db.Model(&MaintenanceWindow{}).Where("(device_id IS NULL OR device_id = ?) AND start_time < ? AND end_time > ?", deviceID, end, start).Count(&count).Error
	return count > 0, err
}

// GetMaintenanceWindows returns maintenance windows that apply to a device:
// windows scoped to it plus fleet-wide windows (device_id IS NULL).
func GetMaintenanceWindows(deviceID uint) ([]MaintenanceWindow, error) {
	var windows []MaintenanceWindow
	err := DB.Where("device_id IS NULL OR device_id = ?", deviceID).Order("start_time DESC").Find(&windows).Error
	return windows, err
}

// ResetAnalyticsData clears derived analytics while keeping device identity/token data.
// It removes all events, daily outage summaries, and resets last_seen for every device.
func ResetAnalyticsData() error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM events").Error; err != nil {
			return fmt.Errorf("failed to clear events: %w", err)
		}

		if err := tx.Exec("DELETE FROM daily_outage_summaries").Error; err != nil {
			return fmt.Errorf("failed to clear daily outage summaries: %w", err)
		}
		if err := tx.Exec("DELETE FROM outages").Error; err != nil {
			return fmt.Errorf("failed to clear outages: %w", err)
		}
		if err := tx.Exec("DELETE FROM daily_event_rollups").Error; err != nil {
			return fmt.Errorf("failed to clear event rollups: %w", err)
		}

		if err := tx.Model(&Device{}).Where("1 = 1").Update("last_seen", nil).Error; err != nil {
			return fmt.Errorf("failed to reset device last_seen: %w", err)
		}

		return nil
	})
}

// DeleteOldEvents removes events older than retentionDays days.
// Call this periodically (e.g. from a background goroutine) to keep the event table small.
func DeleteOldEvents(retentionDays int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	result := DB.Where("timestamp < ?", cutoff).Delete(&Event{})
	if result.Error != nil {
		return 0, fmt.Errorf("failed to delete old events: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// GetDeviceOutageStats returns aggregated outage counts for today, current month, and current year.
func GetDeviceOutageStats(deviceID uint) (*OutageStats, error) {
	now := time.Now().In(DisplayLocation)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, DisplayLocation)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, DisplayLocation)
	yearStart := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, DisplayLocation)

	type aggRow struct {
		OutageCount   int
		TotalDowntime int64
	}

	queryRange := func(from time.Time) (aggRow, error) {
		var row aggRow
		err := DB.Model(&DailyOutageSummary{}).
			Select("COALESCE(SUM(outage_count),0) as outage_count, COALESCE(SUM(total_downtime),0) as total_downtime").
			Where("device_id = ? AND date >= ?", deviceID, from).
			Scan(&row).Error
		return row, err
	}

	todayRow, err := queryRange(todayStart)
	if err != nil {
		return nil, err
	}
	monthRow, err := queryRange(monthStart)
	if err != nil {
		return nil, err
	}
	yearRow, err := queryRange(yearStart)
	if err != nil {
		return nil, err
	}

	return &OutageStats{
		TodayCount:    todayRow.OutageCount,
		TodayDowntime: time.Duration(todayRow.TotalDowntime),
		MonthCount:    monthRow.OutageCount,
		MonthDowntime: time.Duration(monthRow.TotalDowntime),
		YearCount:     yearRow.OutageCount,
		YearDowntime:  time.Duration(yearRow.TotalDowntime),
	}, nil
}

// AvailabilityReport is export-friendly availability data for a requested window.
type AvailabilityReport struct {
	Device        Device        `json:"device"`
	From          time.Time     `json:"from"`
	To            time.Time     `json:"to"`
	OutageCount   int64         `json:"outage_count"`
	Downtime      time.Duration `json:"downtime"`
	UptimePercent float64       `json:"uptime_percent"`
}

func GetAvailabilityReport(deviceID uint, from, to time.Time) (*AvailabilityReport, error) {
	device, err := GetDeviceByID(deviceID)
	if err != nil {
		return nil, err
	}
	var row struct {
		Count    int64
		Duration int64
	}
	if err := DB.Model(&Outage{}).Select("COUNT(*) AS count, COALESCE(SUM(duration), 0) AS duration").Where("device_id = ? AND start_time < ? AND (end_time IS NULL OR end_time > ?) AND suppressed = ?", deviceID, to, from, false).Scan(&row).Error; err != nil {
		return nil, err
	}
	total := to.Sub(from)
	uptime := 100.0
	if total > 0 {
		uptime = 100 * float64(maxDuration(0, total-time.Duration(row.Duration))) / float64(total)
	}
	return &AvailabilityReport{Device: *device, From: from, To: to, OutageCount: row.Count, Downtime: time.Duration(row.Duration), UptimePercent: uptime}, nil
}
func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// ChartBucket holds aggregated event counts for one time bucket
type ChartBucket struct {
	Label      string `json:"label"`     // Human-readable bucket label
	Timestamp  int64  `json:"timestamp"` // Unix ms for JS Date
	Heartbeats int    `json:"heartbeats"`
	Boots      int    `json:"boots"`
	Total      int    `json:"total"`
}

// GetEventChartData returns time-bucketed event counts for a device.
// rangeHours controls how far back to look; buckets controls how many buckets to split into.
func GetEventChartData(deviceID uint, rangeHours int, buckets int) ([]ChartBucket, error) {
	now := time.Now()
	from := now.Add(-time.Duration(rangeHours) * time.Hour)
	bucketDur := time.Duration(rangeHours) * time.Hour / time.Duration(buckets)

	rawFrom := from
	if rangeHours > 7*24 {
		rawFrom = now.AddDate(0, 0, -7)
	}
	var events []Event
	if err := DB.Where("device_id = ? AND timestamp >= ?", deviceID, rawFrom).Order("timestamp ASC").Find(&events).Error; err != nil {
		return nil, fmt.Errorf("failed to fetch events for chart: %w", err)
	}
	var rollups []DailyEventRollup
	if rangeHours > 7*24 {
		// Raw rows may already be pruned; daily rollups preserve historical chart data.
		if err := DB.Where("device_id = ? AND date >= ? AND date < ?", deviceID, from, rawFrom).Find(&rollups).Error; err != nil {
			return nil, fmt.Errorf("failed to fetch event rollups: %w", err)
		}
	}

	result := make([]ChartBucket, buckets)
	for i := 0; i < buckets; i++ {
		bucketStart := from.Add(time.Duration(i) * bucketDur)
		bucketEnd := bucketStart.Add(bucketDur)
		mid := bucketStart.Add(bucketDur / 2)

		b := ChartBucket{
			Label:     chartBucketLabel(bucketStart, bucketDur),
			Timestamp: mid.UnixMilli(),
		}
		for _, e := range events {
			if !e.Timestamp.Before(bucketStart) && e.Timestamp.Before(bucketEnd) {
				b.Total++
				if e.EventType == EventTypeBoot {
					b.Boots++
				} else {
					b.Heartbeats++
				}
			}
		}
		for _, r := range rollups {
			if !r.Date.Before(bucketStart) && r.Date.Before(bucketEnd) {
				b.Heartbeats += r.Heartbeats
				b.Boots += r.Boots
				b.Total += r.Heartbeats + r.Boots
			}
		}
		result[i] = b
	}

	return result, nil
}

func chartBucketLabel(bucketStart time.Time, bucketDur time.Duration) string {
	switch {
	case bucketDur < time.Hour:
		return bucketStart.Format("3:04 pm")
	case bucketDur < 24*time.Hour:
		return bucketStart.Format("Jan 2 3:00 pm")
	default:
		return bucketStart.Format("Jan 2")
	}
}
