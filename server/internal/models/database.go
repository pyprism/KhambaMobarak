// Package models provides database access and operations
package models

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	db.Exec("PRAGMA journal_mode=WAL;")
	db.Exec("PRAGMA busy_timeout=5000;")

	// Auto migrate schemas
	if err := db.AutoMigrate(&Device{}, &Event{}, &DailyOutageSummary{}); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	DB = db
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

// DeleteDevice deletes a device by ID
func DeleteDevice(id uint) error {
	return DB.Delete(&Device{}, id).Error
}

// RecordEvent records a new event for a device
func RecordEvent(deviceID uint, eventType string) (*Event, error) {
	now := time.Now()

	event := &Event{
		DeviceID:  deviceID,
		EventType: eventType,
		Timestamp: now,
	}

	if err := DB.Create(event).Error; err != nil {
		return nil, fmt.Errorf("failed to record event: %w", err)
	}

	// Update device's last seen timestamp
	if err := DB.Model(&Device{}).Where("id = ?", deviceID).Update("last_seen", now).Error; err != nil {
		return nil, fmt.Errorf("failed to update device last seen: %w", err)
	}

	// On boot (power restored), update daily outage summary
	if eventType == EventTypeBoot {
		if err := UpdateOutageSummaryOnBoot(deviceID, now); err != nil {
			// Non-fatal: log but don't fail the event recording
			_ = err
		}
	}

	return event, nil
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

// GetRecentEvents retrieves recent events across all devices
func GetRecentEvents(limit int) ([]Event, error) {
	var events []Event
	if err := DB.Order("timestamp DESC").Limit(limit).Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
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

// GetOutages calculates outage periods for a device
// An outage is detected when there's a gap > 2 minutes between events
func GetOutages(deviceID uint, limit int) ([]OutageInfo, error) {
	var events []Event
	if err := DB.Where("device_id = ?", deviceID).
		Order("timestamp ASC").
		Find(&events).Error; err != nil {
		return nil, err
	}

	device, err := GetDeviceByID(deviceID)
	if err != nil {
		return nil, err
	}

	var outages []OutageInfo
	threshold := 2 * time.Minute

	for i := 1; i < len(events); i++ {
		gap := events[i].Timestamp.Sub(events[i-1].Timestamp)
		if gap > threshold {
			outage := OutageInfo{
				DeviceID:   deviceID,
				DeviceName: device.Name,
				Location:   device.Location,
				StartTime:  events[i-1].Timestamp,
				EndTime:    &events[i].Timestamp,
				Duration:   gap,
				IsOngoing:  false,
			}
			outages = append([]OutageInfo{outage}, outages...) // Prepend for reverse order
		}
	}

	// Check for ongoing outage
	if len(events) > 0 {
		lastEvent := events[len(events)-1]
		if time.Since(lastEvent.Timestamp) > threshold {
			outage := OutageInfo{
				DeviceID:   deviceID,
				DeviceName: device.Name,
				Location:   device.Location,
				StartTime:  lastEvent.Timestamp,
				Duration:   time.Since(lastEvent.Timestamp),
				IsOngoing:  true,
			}
			outages = append([]OutageInfo{outage}, outages...)
		}
	}

	// Apply limit
	if limit > 0 && len(outages) > limit {
		outages = outages[:limit]
	}

	return outages, nil
}

// GetAllOutages retrieves recent outages across all devices
func GetAllOutages(limit int) ([]OutageInfo, error) {
	devices, err := GetAllDevices()
	if err != nil {
		return nil, err
	}

	var allOutages []OutageInfo
	for _, device := range devices {
		outages, err := GetOutages(device.ID, 0)
		if err != nil {
			continue
		}
		allOutages = append(allOutages, outages...)
	}

	// Sort by start time (most recent first) and limit
	// Simple bubble sort for small dataset
	for i := 0; i < len(allOutages); i++ {
		for j := i + 1; j < len(allOutages); j++ {
			if allOutages[j].StartTime.After(allOutages[i].StartTime) {
				allOutages[i], allOutages[j] = allOutages[j], allOutages[i]
			}
		}
	}

	if limit > 0 && len(allOutages) > limit {
		allOutages = allOutages[:limit]
	}

	return allOutages, nil
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

// UpdateOutageSummaryOnBoot is called whenever a boot (power-restored) event is recorded.
// It looks for the most recent event before bootTime; if the gap exceeds 2 minutes, it
// records the outage in DailyOutageSummary keyed to the day the outage started.
func UpdateOutageSummaryOnBoot(deviceID uint, bootTime time.Time) error {
	var lastEvent Event
	if err := DB.Where("device_id = ? AND timestamp < ?", deviceID, bootTime).
		Order("timestamp DESC").First(&lastEvent).Error; err != nil {
		// No previous event — nothing to record.
		return nil
	}

	gap := bootTime.Sub(lastEvent.Timestamp)
	if gap <= 2*time.Minute {
		return nil
	}

	// Attribute the outage to the calendar day it started.
	loc := lastEvent.Timestamp.Location()
	startDay := time.Date(lastEvent.Timestamp.Year(), lastEvent.Timestamp.Month(), lastEvent.Timestamp.Day(), 0, 0, 0, 0, loc)

	var summary DailyOutageSummary
	err := DB.Where("device_id = ? AND date = ?", deviceID, startDay).First(&summary).Error
	if err != nil {
		// Create a fresh row.
		summary = DailyOutageSummary{
			DeviceID:      deviceID,
			Date:          startDay,
			OutageCount:   1,
			TotalDowntime: int64(gap),
		}
		return DB.Create(&summary).Error
	}

	// Increment existing row.
	return DB.Model(&summary).Updates(map[string]interface{}{
		"outage_count":   summary.OutageCount + 1,
		"total_downtime": summary.TotalDowntime + int64(gap),
	}).Error
}

// GetDeviceOutageStats returns aggregated outage counts for today, current month, and current year.
func GetDeviceOutageStats(deviceID uint) (*OutageStats, error) {
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	yearStart := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())

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

	var events []Event
	if err := DB.Where("device_id = ? AND timestamp >= ?", deviceID, from).
		Order("timestamp ASC").Find(&events).Error; err != nil {
		return nil, fmt.Errorf("failed to fetch events for chart: %w", err)
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
