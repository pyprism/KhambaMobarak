// Package models provides database access and operations
package models

import (
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
	if err := db.AutoMigrate(&Device{}, &Event{}); err != nil {
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
