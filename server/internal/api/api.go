// Package api provides HTTP API handlers for the Khamba server
package api

import (
	"net/http"
	"strconv"
	"strings"

	"khamba/internal/models"

	"github.com/gin-gonic/gin"
)

// EventRequest represents the incoming event from ESP device
type EventRequest struct {
	EventType string `json:"event_type" binding:"required"`
	Timestamp int64  `json:"timestamp"` // Device uptime (optional)
}

// RegisterRoutes registers all API routes
func RegisterRoutes(r *gin.Engine) {
	api := r.Group("/api")
	{
		// Device events endpoint (for ESP clients)
		api.POST("/events", handleEvent)

		// Device management endpoints (for dashboard)
		api.GET("/devices", getDevices)
		api.GET("/devices/:id", getDevice)
		api.DELETE("/devices/:id", deleteDevice)
		api.GET("/devices/:id/events", getDeviceEvents)
		api.GET("/devices/:id/outages", getDeviceOutages)

		// Dashboard data endpoints
		api.GET("/outages", getAllOutages)
		api.GET("/stats", getDashboardStats)
	}
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

	// Parse request body
	var req EventRequest
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
	event, err := models.RecordEvent(device.ID, req.EventType)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record event"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "event recorded",
		"event_id": event.ID,
	})
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

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

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

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	outages, err := models.GetOutages(uint(id), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get outages"})
		return
	}

	c.JSON(http.StatusOK, outages)
}

// getAllOutages returns recent outages across all devices
func getAllOutages(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	outages, err := models.GetAllOutages(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get outages"})
		return
	}

	c.JSON(http.StatusOK, outages)
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

	outages, _ := models.GetAllOutages(0)
	ongoingOutages := 0
	for _, o := range outages {
		if o.IsOngoing {
			ongoingOutages++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"total_devices":   len(devices),
		"online_devices":  onlineCount,
		"offline_devices": offlineCount,
		"total_outages":   len(outages),
		"ongoing_outages": ongoingOutages,
	})
}
