// Package handlers provides HTTP handlers for web pages
package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"khamba/internal/models"

	"github.com/gin-gonic/gin"
)

const recentEventLimit = 20
const availabilityWindowDays = 30

// RegisterRoutes registers web page routes
func RegisterRoutes(r *gin.Engine) {
	// Dashboard
	r.GET("/", dashboardHandler)
	r.GET("/devices/:id", deviceDetailHandler)

	// Infra endpoints
	r.GET("/healthz", healthzHandler)
	r.GET("/metrics", metricsHandler)
}

// healthzHandler is a liveness/readiness probe for systemd/uptime checks. It
// touches the database so a broken connection is reported as unhealthy.
func healthzHandler(c *gin.Context) {
	if _, err := models.GetAllDevices(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// metricsHandler exposes basic fleet counters in Prometheus text exposition
// format, built from data already collected for the dashboard.
func metricsHandler(c *gin.Context) {
	devices, err := models.GetAllDevices()
	if err != nil {
		c.String(http.StatusInternalServerError, "# failed to load devices\n")
		return
	}
	online := 0
	for _, d := range devices {
		if d.IsOnline {
			online++
		}
	}
	totalOutages, err := models.CountOutages()
	if err != nil {
		c.String(http.StatusInternalServerError, "# failed to load outages\n")
		return
	}
	ongoingOutages, err := models.CountOngoingOutages()
	if err != nil {
		c.String(http.StatusInternalServerError, "# failed to load ongoing outages\n")
		return
	}
	causeCounts, err := models.CountOutagesByCause()
	if err != nil {
		c.String(http.StatusInternalServerError, "# failed to load outage cause breakdown\n")
		return
	}

	lines := []string{
		"# HELP khamba_devices_total Total registered devices.",
		"# TYPE khamba_devices_total gauge",
		fmt.Sprintf("khamba_devices_total %d", len(devices)),
		"# HELP khamba_devices_online Devices currently online.",
		"# TYPE khamba_devices_online gauge",
		fmt.Sprintf("khamba_devices_online %d", online),
		"# HELP khamba_devices_offline Devices currently offline.",
		"# TYPE khamba_devices_offline gauge",
		fmt.Sprintf("khamba_devices_offline %d", len(devices)-online),
		// khamba_outages_total excludes maintenance-suppressed rows and
		// planned (deep-sleep) wakes; it is not a raw row count.
		"# HELP khamba_outages_total Total persisted outage records, excluding maintenance and planned wakes.",
		"# TYPE khamba_outages_total counter",
		fmt.Sprintf("khamba_outages_total %d", totalOutages),
		"# HELP khamba_outages_ongoing Devices currently in an outage.",
		"# TYPE khamba_outages_ongoing gauge",
		fmt.Sprintf("khamba_outages_ongoing %d", ongoingOutages),
		"# HELP khamba_outages_power_total Persisted outages confirmed as power loss.",
		"# TYPE khamba_outages_power_total counter",
		fmt.Sprintf("khamba_outages_power_total %d", causeCounts.Power),
		"# HELP khamba_outages_connectivity_total Persisted outages inferred as connectivity-only gaps.",
		"# TYPE khamba_outages_connectivity_total counter",
		fmt.Sprintf("khamba_outages_connectivity_total %d", causeCounts.Connectivity),
		"# HELP khamba_outages_device_reset_total Persisted outages from a device-initiated reset.",
		"# TYPE khamba_outages_device_reset_total counter",
		fmt.Sprintf("khamba_outages_device_reset_total %d", causeCounts.DeviceReset),
	}
	c.String(http.StatusOK, strings.Join(lines, "\n")+"\n")
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
	now := time.Now()
	availability, _ := models.GetAvailabilityReport(uint(id), now.AddDate(0, 0, -availabilityWindowDays), now)
	maintenanceWindows, _ := models.GetMaintenanceWindows(uint(id))

	c.HTML(http.StatusOK, "device.html", gin.H{
		"title":              device.Name + " - Power Monitor",
		"device":             device,
		"events":             events,
		"outages":            outages,
		"bootEvents":         bootEvents,
		"outageStats":        outageStats,
		"availability":       availability,
		"availabilityDays":   availabilityWindowDays,
		"maintenanceWindows": maintenanceWindows,
	})
}
