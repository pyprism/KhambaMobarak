package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"khamba/internal/models"

	"github.com/gin-gonic/gin"
)

func setupTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(t.TempDir(), "khamba-api-test.db")
	if err := models.InitDB(dbPath); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	r := gin.New()
	RegisterRoutes(r)
	return r
}

func TestHandleEventRequiresAuthorizationHeader(t *testing.T) {
	r := setupTestRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewBufferString(`{"event_type":"boot"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, resp.Code)
	}
}

func TestHandleEventRejectsInvalidEventType(t *testing.T) {
	r := setupTestRouter(t)

	_, token, err := models.CreateDevice("Lab", "HQ")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewBufferString(`{"event_type":"unknown"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, resp.Code)
	}
}

func TestHandleEventRecordsEvent(t *testing.T) {
	r := setupTestRouter(t)

	device, token, err := models.CreateDevice("Garage", "Home")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewBufferString(`{"event_type":"heartbeat"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
	}

	events, total, err := models.GetDeviceEvents(device.ID, 10, 0)
	if err != nil {
		t.Fatalf("GetDeviceEvents failed: %v", err)
	}
	if total != 1 || len(events) != 1 {
		t.Fatalf("expected 1 recorded event, total=%d len=%d", total, len(events))
	}
	if events[0].EventType != models.EventTypeHeartbeat {
		t.Fatalf("expected heartbeat event, got %q", events[0].EventType)
	}
}

func TestGetDashboardStatsReturnsCounts(t *testing.T) {
	r := setupTestRouter(t)

	deviceA, _, err := models.CreateDevice("Node A", "A")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}
	_, _, err = models.CreateDevice("Node B", "B")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}
	if _, err := models.RecordEvent(deviceA.ID, models.EventTypeHeartbeat); err != nil {
		t.Fatalf("RecordEvent failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
	}

	var body map[string]int
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["total_devices"] != 2 {
		t.Fatalf("expected total_devices=2, got %d", body["total_devices"])
	}
}
