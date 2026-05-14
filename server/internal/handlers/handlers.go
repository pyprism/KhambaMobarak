// Package handlers provides HTTP handlers for web pages
package handlers

import (
	"net/http"
	"strconv"

	"khamba/internal/models"

	"github.com/gin-gonic/gin"
)

const recentEventLimit = 20

// RegisterRoutes registers web page routes
func RegisterRoutes(r *gin.Engine) {
	// Dashboard
	r.GET("/", dashboardHandler)
	r.GET("/devices/:id", deviceDetailHandler)
}

// dashboardHandler renders the main dashboard
func dashboardHandler(c *gin.Context) {
	devices, err := models.GetAllDevices()
	if err != nil {
		c.HTML(http.StatusInternalServerError, "error.html", gin.H{
			"error": "Failed to load devices",
		})
		return
	}

	outages, _ := models.GetAllOutages(10)

	onlineCount := 0
	offlineCount := 0
	for _, d := range devices {
		if d.IsOnline {
			onlineCount++
		} else {
			offlineCount++
		}
	}

	c.HTML(http.StatusOK, "dashboard.html", gin.H{
		"title":          "Power Outage Monitor Dashboard",
		"devices":        devices,
		"outages":        outages,
		"totalDevices":   len(devices),
		"onlineDevices":  onlineCount,
		"offlineDevices": offlineCount,
	})
}

// deviceDetailHandler renders the device detail page
func deviceDetailHandler(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.HTML(http.StatusBadRequest, "error.html", gin.H{
			"error": "Invalid device ID",
		})
		return
	}

	device, err := models.GetDeviceByID(uint(id))
	if err != nil {
		c.HTML(http.StatusNotFound, "error.html", gin.H{
			"error": "Device not found",
		})
		return
	}

	events, _, _ := models.GetDeviceEvents(uint(id), recentEventLimit, 0)
	outages, _ := models.GetOutages(uint(id), 20)
	bootEvents, _ := models.GetBootEvents(uint(id), 10)
	outageStats, _ := models.GetDeviceOutageStats(uint(id))

	c.HTML(http.StatusOK, "device.html", gin.H{
		"title":       device.Name + " - Power Monitor",
		"device":      device,
		"events":      events,
		"outages":     outages,
		"bootEvents":  bootEvents,
		"outageStats": outageStats,
	})
}
