// Package api provides HTTP API handlers for the Khamba server
package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"khamba/internal/models"

	"github.com/gin-gonic/gin"
)

// chartRange maps URL param values to (hours, buckets)
var chartRanges = map[string][2]int{
	"24h": {24, 24},   // 24 one-hour buckets
	"7d":  {168, 42},  // 42 four-hour buckets
	"30d": {720, 30},  // 30 one-day buckets
	"3m":  {2160, 45}, // 45 two-day buckets
	"6m":  {4320, 54}, // 54 ~80-hour buckets
	"1y":  {8760, 52}, // 52 one-week buckets
}

// EventRequest represents the incoming event from ESP device
type EventRequest struct {
	EventType   string `json:"event_type" binding:"required"`
	EventID     string `json:"event_id"`
	ResetReason string `json:"reset_reason"`
}

const maxEventBody = 4096
const maxPageLimit = 100

type rateEntry struct {
	count int
	reset time.Time
}

var ingestRates = struct {
	sync.Mutex
	entries map[string]rateEntry
}{entries: make(map[string]rateEntry)}

func allowIngest(key string) bool {
	ingestRates.Lock()
	defer ingestRates.Unlock()
	now := time.Now()
	entry := ingestRates.entries[key]
	if entry.reset.Before(now) {
		entry = rateEntry{reset: now.Add(time.Minute)}
	}
	entry.count++
	ingestRates.entries[key] = entry
	return entry.count <= 120
}

var startIngestRateSweeperOnce sync.Once

// startIngestRateSweeper periodically evicts expired ingestRates entries so
// the map doesn't grow forever as new token|IP combinations show up over the
// life of the process. Safe to call more than once (e.g. from tests that
// call RegisterRoutes repeatedly) — only the first call starts the goroutine.
func startIngestRateSweeper() {
	startIngestRateSweeperOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				sweepExpiredIngestEntries(time.Now())
			}
		}()
	})
}

func sweepExpiredIngestEntries(now time.Time) {
	ingestRates.Lock()
	defer ingestRates.Unlock()
	for key, entry := range ingestRates.entries {
		if entry.reset.Before(now) {
			delete(ingestRates.entries, key)
		}
	}
}

func queryInt(c *gin.Context, name string, fallback, maximum int) (int, error) {
	raw := c.DefaultQuery(name, strconv.Itoa(fallback))
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || value > maximum {
		return 0, fmt.Errorf("%s must be between 0 and %d", name, maximum)
	}
	return value, nil
}

// RegisterRoutes registers all API routes
func RegisterRoutes(r *gin.Engine) {
	startIngestRateSweeper()
	api := r.Group("/api")
	{
		// Device events endpoint (for ESP clients)
		api.POST("/events", handleEvent)

		// Device management endpoints (for dashboard)
		api.GET("/devices", getDevices)
		api.POST("/devices", createDevice)
		api.GET("/devices/:id", getDevice)
		api.PATCH("/devices/:id", updateDevice)
		api.DELETE("/devices/:id", deleteDevice)
		api.GET("/devices/:id/events", getDeviceEvents)
		api.GET("/devices/:id/outages", getDeviceOutages)
		api.GET("/devices/:id/chart", getDeviceChartData)

		// Dashboard data endpoints
		api.GET("/outages", getAllOutages)
		api.PATCH("/outages/:id", acknowledgeOutage)
		api.GET("/stats", getDashboardStats)
		api.GET("/maintenance", getMaintenance)
		api.POST("/maintenance", createMaintenance)
		api.GET("/devices/:id/report", getAvailabilityReport)
		api.GET("/devices/:id/export.csv", exportDeviceCSV)
	}
}

func reportWindow(c *gin.Context) (time.Time, time.Time, bool) {
	days, err := queryInt(c, "days", 30, 366)
	if err != nil || days == 0 {
		c.JSON(400, gin.H{"error": "days must be between 1 and 366"})
		return time.Time{}, time.Time{}, false
	}
	to := time.Now()
	return to.AddDate(0, 0, -days), to, true
}
func getAvailabilityReport(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid device ID"})
		return
	}
	from, to, ok := reportWindow(c)
	if !ok {
		return
	}
	report, err := models.GetAvailabilityReport(uint(id), from, to)
	if err != nil {
		c.JSON(404, gin.H{"error": "device not found"})
		return
	}
	c.JSON(200, report)
}
func exportDeviceCSV(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid device ID"})
		return
	}
	from, to, ok := reportWindow(c)
	if !ok {
		return
	}
	report, err := models.GetAvailabilityReport(uint(id), from, to)
	if err != nil {
		c.JSON(404, gin.H{"error": "device not found"})
		return
	}
	c.Header("Content-Disposition", "attachment; filename=device-report.csv")
	c.Header("Content-Type", "text/csv")
	w := csv.NewWriter(c.Writer)
	_ = w.Write([]string{"device", "location", "from", "to", "uptime_percent", "outage_count", "downtime_seconds"})
	_ = w.Write([]string{report.Device.Name, report.Device.Location, report.From.Format(time.RFC3339), report.To.Format(time.RFC3339), strconv.FormatFloat(report.UptimePercent, 'f', 2, 64), strconv.FormatInt(report.OutageCount, 10), strconv.FormatInt(int64(report.Downtime.Seconds()), 10)})
	w.Flush()
}

// handleEvent handles incoming events from ESP devices
func handleEvent(c *gin.Context) {
	// Extract token from Authorization header
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
		return
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == authHeader {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid authorization format"})
		return
	}

	// Validate device token
	device, err := models.GetDeviceByToken(token)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid device token"})
		return
	}
	if !allowIngest(token + "|" + c.ClientIP()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
		return
	}

	// Parse request body
	var req EventRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxEventBody)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	// Validate event type
	if req.EventType != models.EventTypeBoot && req.EventType != models.EventTypeHeartbeat {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid event type"})
		return
	}

	// Record event
	event, err := models.RecordEvent(device.ID, req.EventType, req.EventID, req.ResetReason)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record event"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "event recorded",
		"event_id": event.ID,
	})
}

type createDeviceRequest struct {
	Name     string `json:"name" binding:"required"`
	Location string `json:"location"`
}

// createDevice registers a new device from the dashboard and returns its
// bearer token once (the same one-time-reveal contract as the CLI).
func createDevice(c *gin.Context) {
	var req createDeviceRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	device, token, err := models.CreateDevice(strings.TrimSpace(req.Name), req.Location)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create device"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"device": device, "token": token})
}

type updateDeviceRequest struct {
	Name     *string `json:"name"`
	Location *string `json:"location"`
}

// updateDevice renames and/or relocates a device.
func updateDevice(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device ID"})
		return
	}
	var req updateDeviceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	device, err := models.UpdateDevice(uint(id), req.Name, req.Location)
	if err != nil {
		if err.Error() == "name cannot be empty" {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
		return
	}
	c.JSON(http.StatusOK, device)
}

// getDevices returns all registered devices
func getDevices(c *gin.Context) {
	devices, err := models.GetAllDevices()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get devices"})
		return
	}

	c.JSON(http.StatusOK, devices)
}

// getDevice returns a single device by ID
func getDevice(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device ID"})
		return
	}

	device, err := models.GetDeviceByID(uint(id))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
		return
	}

	c.JSON(http.StatusOK, device)
}

// deleteDevice deletes a device by ID
func deleteDevice(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device ID"})
		return
	}

	if err := models.DeleteDevice(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete device"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "device deleted"})
}

// getDeviceEvents returns events for a specific device
func getDeviceEvents(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device ID"})
		return
	}

	limit, err := queryInt(c, "limit", 50, maxPageLimit)
	if err != nil || limit == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be between 1 and 100"})
		return
	}
	offset, err := queryInt(c, "offset", 0, 100000)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	events, total, err := models.GetDeviceEvents(uint(id), limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get events"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"events": events,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// getDeviceOutages returns outages for a specific device
func getDeviceOutages(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device ID"})
		return
	}

	limit, err := queryInt(c, "limit", 20, maxPageLimit)
	if err != nil || limit == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be between 1 and 100"})
		return
	}

	outages, err := models.GetOutages(uint(id), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get outages"})
		return
	}

	c.JSON(http.StatusOK, outages)
}

// getDeviceChartData returns time-bucketed event data for charts
// Query param: range = 24h | 7d | 30d | 3m | 6m | 1y  (default: 24h)
func getDeviceChartData(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid device ID"})
		return
	}

	rangeKey := c.DefaultQuery("range", "24h")
	params, ok := chartRanges[rangeKey]
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid range; use 24h, 7d, 30d, 3m, 6m, or 1y"})
		return
	}

	buckets, err := models.GetEventChartData(uint(id), params[0], params[1])
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get chart data"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"range":   rangeKey,
		"buckets": buckets,
	})
}

// getAllOutages returns recent outages across all devices
func getAllOutages(c *gin.Context) {
	limit, err := queryInt(c, "limit", 20, maxPageLimit)
	if err != nil || limit == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be between 1 and 100"})
		return
	}

	outages, err := models.GetAllOutages(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get outages"})
		return
	}

	c.JSON(http.StatusOK, outages)
}

type acknowledgeOutageRequest struct {
	Notes string `json:"notes"`
}

// acknowledgeOutage marks a persisted outage as acknowledged, optionally
// attaching a note. Derived/ongoing outages (no row yet) return 404.
func acknowledgeOutage(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid outage ID"})
		return
	}
	var req acknowledgeOutageRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}
	}
	outage, err := models.AcknowledgeOutage(uint(id), req.Notes)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "outage not found"})
		return
	}
	c.JSON(http.StatusOK, outage)
}

type maintenanceRequest struct {
	DeviceID  *uint     `json:"device_id"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
	Notes     string    `json:"notes"`
}

func getMaintenance(c *gin.Context) {
	var windows []models.MaintenanceWindow
	if err := models.DB.Order("start_time DESC").Find(&windows).Error; err != nil {
		c.JSON(500, gin.H{"error": "failed to get maintenance windows"})
		return
	}
	c.JSON(200, windows)
}
func createMaintenance(c *gin.Context) {
	var req maintenanceRequest
	if err := c.ShouldBindJSON(&req); err != nil || !req.EndTime.After(req.StartTime) {
		c.JSON(400, gin.H{"error": "start_time and a later end_time are required"})
		return
	}
	window := models.MaintenanceWindow{DeviceID: req.DeviceID, StartTime: req.StartTime, EndTime: req.EndTime, Notes: req.Notes}
	if err := models.DB.Create(&window).Error; err != nil {
		c.JSON(500, gin.H{"error": "failed to save maintenance window"})
		return
	}
	c.JSON(201, window)
}

// getDashboardStats returns statistics for the dashboard
func getDashboardStats(c *gin.Context) {
	devices, err := models.GetAllDevices()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get stats"})
		return
	}

	onlineCount := 0
	offlineCount := 0
	for _, d := range devices {
		if d.IsOnline {
			onlineCount++
		} else {
			offlineCount++
		}
	}

	persistedOutages, err := models.CountOutages()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get stats"})
		return
	}
	ongoingOutages, err := models.CountOngoingOutages()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get stats"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"total_devices": len(devices), "online_devices": onlineCount, "offline_devices": offlineCount,
		// total_outages = persisted incidents plus devices currently mid-outage
		// (which have no row yet; see models.CountOngoingOutages).
		"total_outages":   persistedOutages + ongoingOutages,
		"ongoing_outages": ongoingOutages,
	})
}
