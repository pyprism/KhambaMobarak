package handlers

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"khamba/internal/models"

	"github.com/gin-gonic/gin"
)

func setupHandlersRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "khamba-handlers-test.db")
	if err := models.InitDB(dbPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	tmplFiles, err := filepath.Glob(filepath.Join("..", "..", "web", "templates", "*.html"))
	if err != nil {
		t.Fatalf("template glob failed: %v", err)
	}
	if len(tmplFiles) == 0 {
		t.Fatalf("no template files found")
	}

	tmpl, err := template.ParseFiles(tmplFiles...)
	if err != nil {
		t.Fatalf("template parse failed: %v", err)
	}

	r := gin.New()
	r.SetHTMLTemplate(tmpl)
	RegisterRoutes(r)
	return r
}

func TestDashboardHandlerRendersDeviceData(t *testing.T) {
	r := setupHandlersRouter(t)

	if _, _, err := models.CreateDevice("Node Dashboard", "Lab"); err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "Node Dashboard") {
		t.Fatalf("expected rendered dashboard to include device name")
	}
}

func TestDashboardHandlerRenders12HourVisibleTimesAndISOTimestamps(t *testing.T) {
	r := setupHandlersRouter(t)

	device, _, err := models.CreateDevice("Node Clock", "Control Room")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	lastSeen := time.Date(2026, time.May, 14, 15, 4, 5, 0, time.UTC)
	if err := models.DB.Model(&models.Device{}).Where("id = ?", device.ID).Update("last_seen", lastSeen).Error; err != nil {
		t.Fatalf("failed to update last_seen: %v", err)
	}

	events := []models.Event{
		{DeviceID: device.ID, EventType: models.EventTypeHeartbeat, Timestamp: lastSeen},
		{DeviceID: device.ID, EventType: models.EventTypeBoot, Timestamp: lastSeen.Add(10 * time.Minute)},
	}
	if err := models.DB.Create(&events).Error; err != nil {
		t.Fatalf("failed to create events: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
	}

	body := resp.Body.String()
	if !strings.Contains(body, `data-time="2026-05-14T15:04:05Z"`) {
		t.Fatalf("expected dashboard to preserve ISO timestamps in data-time attributes")
	}
	if !strings.Contains(body, "May 14 3:04 pm") {
		t.Fatalf("expected dashboard to render 12-hour visible time")
	}
}

func TestHealthzEndpointReturnsOK(t *testing.T) {
	r := setupHandlersRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
	}
	if !strings.Contains(resp.Body.String(), `"status":"ok"`) {
		t.Fatalf("expected ok status in body, got %s", resp.Body.String())
	}
}

func TestMetricsEndpointExposesCounters(t *testing.T) {
	r := setupHandlersRouter(t)

	if _, _, err := models.CreateDevice("Metrics Node", "Lab"); err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "khamba_devices_total 1") {
		t.Fatalf("expected khamba_devices_total gauge in output, got %s", body)
	}
}

func TestDeviceDetailHandlerValidationAndNotFound(t *testing.T) {
	r := setupHandlersRouter(t)

	t.Run("invalid id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/devices/not-a-number", nil)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)

		if resp.Code != http.StatusBadRequest {
			t.Fatalf("expected status %d, got %d", http.StatusBadRequest, resp.Code)
		}
	})

	t.Run("not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/devices/99999", nil)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)

		if resp.Code != http.StatusNotFound {
			t.Fatalf("expected status %d, got %d", http.StatusNotFound, resp.Code)
		}
	})
}

func TestDeviceDetailHandlerRendersExistingDevice(t *testing.T) {
	r := setupHandlersRouter(t)

	device, _, err := models.CreateDevice("Node Detail", "Warehouse")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}
	if _, err := models.RecordEvent(device.ID, models.EventTypeHeartbeat); err != nil {
		t.Fatalf("RecordEvent failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/devices/"+strconv.Itoa(int(device.ID)), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "Node Detail") {
		t.Fatalf("expected rendered device page to include device name")
	}
	if !strings.Contains(resp.Body.String(), "Last 20") {
		t.Fatalf("expected recent events badge to show the 20 event limit")
	}
}

func TestDeviceDetailHandlerRenders12HourTimesAndScrollablePowerLists(t *testing.T) {
	r := setupHandlersRouter(t)

	device, _, err := models.CreateDevice("Node Time Detail", "Warehouse")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	lastSeen := time.Date(2026, time.May, 14, 15, 4, 5, 0, time.UTC)
	if err := models.DB.Model(&models.Device{}).Where("id = ?", device.ID).Update("last_seen", lastSeen).Error; err != nil {
		t.Fatalf("failed to update last_seen: %v", err)
	}

	events := []models.Event{
		{DeviceID: device.ID, EventType: models.EventTypeHeartbeat, Timestamp: lastSeen},
		{DeviceID: device.ID, EventType: models.EventTypeBoot, Timestamp: lastSeen.Add(65 * time.Minute)},
	}
	if err := models.DB.Create(&events).Error; err != nil {
		t.Fatalf("failed to create events: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/devices/"+strconv.Itoa(int(device.ID)), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
	}

	body := resp.Body.String()
	if !strings.Contains(body, "May 14, 2026 3:04:05 pm") {
		t.Fatalf("expected device page to render last-seen time in 12-hour format")
	}
	if !strings.Contains(body, "May 14, 2026 4:09:05 pm") {
		t.Fatalf("expected device page to render event time in 12-hour format")
	}
	if !strings.Contains(body, "May 14, 2026 3:04 pm") || !strings.Contains(body, "→ 4:09 pm") {
		t.Fatalf("expected outage list to render 12-hour start and end times")
	}
	if !strings.Contains(body, `class="table-responsive power-list-scroll"`) {
		t.Fatalf("expected power restorations table to use the scrollable class")
	}
	if !strings.Contains(body, `class="card-body power-list-scroll"`) {
		t.Fatalf("expected outage list to use the scrollable class")
	}
	if !strings.Contains(body, `data-time="2026-05-14T15:04:05Z"`) {
		t.Fatalf("expected device page to preserve ISO timestamps in data-time attributes")
	}
}
