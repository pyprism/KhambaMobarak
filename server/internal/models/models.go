// Package models defines the database models for the Khamba Mobarak server.
package models

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"gorm.io/gorm"
)

// Device represents a monitored device/location
type Device struct {
	ID        uint           `gorm:"primarykey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	Name     string     `gorm:"size:100;not null" json:"name"`
	Location string     `gorm:"size:255" json:"location"`
	Token    string     `gorm:"size:64;uniqueIndex;not null" json:"-"`
	LastSeen *time.Time `json:"last_seen"`
	IsOnline bool       `gorm:"-" json:"is_online"` // Computed field
	Events   []Event    `gorm:"foreignKey:DeviceID" json:"events,omitempty"`
}

// Event represents a power event (boot or heartbeat)
type Event struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	CreatedAt time.Time `json:"created_at"`

	DeviceID  uint      `gorm:"index;not null" json:"device_id"`
	EventType string    `gorm:"size:20;not null" json:"event_type"`
	Timestamp time.Time `gorm:"not null" json:"timestamp"`
}

// EventType constants
const (
	EventTypeBoot      = "boot"
	EventTypeHeartbeat = "heartbeat"
)

// GenerateToken creates a secure random token for device authentication
func GenerateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// IsDeviceOnline checks if a device is online based on last heartbeat
// A device is considered online if it sent a heartbeat in the last 2 minutes
func (d *Device) IsDeviceOnline() bool {
	if d.LastSeen == nil {
		return false
	}
	return time.Since(*d.LastSeen) < 2*time.Minute
}

// BeforeCreate hook to set computed fields
func (d *Device) AfterFind(tx *gorm.DB) error {
	d.IsOnline = d.IsDeviceOnline()
	return nil
}

// DailyOutageSummary stores aggregated outage counts per device per day.
// It is updated each time a boot event (power restoration) is recorded.
type DailyOutageSummary struct {
	ID            uint      `gorm:"primarykey" json:"id"`
	DeviceID      uint      `gorm:"uniqueIndex:idx_device_date;not null" json:"device_id"`
	Date          time.Time `gorm:"uniqueIndex:idx_device_date;not null" json:"date"` // Truncated to midnight UTC
	OutageCount   int       `gorm:"not null;default:0" json:"outage_count"`
	TotalDowntime int64     `gorm:"not null;default:0" json:"total_downtime"` // nanoseconds
}

// OutageStats holds aggregated outage statistics for a device across different time windows.
type OutageStats struct {
	TodayCount    int           `json:"today_count"`
	TodayDowntime time.Duration `json:"today_downtime"`
	MonthCount    int           `json:"month_count"`
	MonthDowntime time.Duration `json:"month_downtime"`
	YearCount     int           `json:"year_count"`
	YearDowntime  time.Duration `json:"year_downtime"`
}

// OutageInfo represents information about a power outage
type OutageInfo struct {
	DeviceID   uint          `json:"device_id"`
	DeviceName string        `json:"device_name"`
	Location   string        `json:"location"`
	StartTime  time.Time     `json:"start_time"`
	EndTime    *time.Time    `json:"end_time,omitempty"`
	Duration   time.Duration `json:"duration"`
	IsOngoing  bool          `json:"is_ongoing"`
}

// DeviceStats represents statistics for a device
type DeviceStats struct {
	DeviceID      uint          `json:"device_id"`
	TotalOutages  int           `json:"total_outages"`
	TotalDowntime time.Duration `json:"total_downtime"`
	LastOutage    *time.Time    `json:"last_outage,omitempty"`
	UptimePercent float64       `json:"uptime_percent"`
}
