package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
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

func decodeMapBody(t *testing.T, resp *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	return body
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

func TestHandleEventRejectsInvalidAuthorizationFormat(t *testing.T) {
	r := setupTestRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewBufferString(`{"event_type":"boot"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token-without-bearer-prefix")
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, resp.Code)
	}
}

func TestHandleEventRejectsInvalidToken(t *testing.T) {
	r := setupTestRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewBufferString(`{"event_type":"boot"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer invalid-token")
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, resp.Code)
	}
}

func TestHandleEventRejectsInvalidJSONBody(t *testing.T) {
	r := setupTestRouter(t)

	_, token, err := models.CreateDevice("Lab", "HQ")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewBufferString(`{"event_type":`))
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

func TestGetDeviceEndpoints(t *testing.T) {
	r := setupTestRouter(t)

	device, _, err := models.CreateDevice("Node A", "A")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	t.Run("get devices list", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
		}
		if !strings.Contains(resp.Body.String(), "Node A") {
			t.Fatalf("expected response to include created device name")
		}
	})

	t.Run("invalid device id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/devices/not-a-number", nil)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)

		if resp.Code != http.StatusBadRequest {
			t.Fatalf("expected status %d, got %d", http.StatusBadRequest, resp.Code)
		}
	})

	t.Run("missing device", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/devices/999999", nil)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)

		if resp.Code != http.StatusNotFound {
			t.Fatalf("expected status %d, got %d", http.StatusNotFound, resp.Code)
		}
	})

	t.Run("get existing device", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/devices/"+strconv.Itoa(int(device.ID)), nil)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
		}
		body := decodeMapBody(t, resp)
		if body["name"] != "Node A" {
			t.Fatalf("expected name Node A, got %#v", body["name"])
		}
	})
}

func TestDeleteDeviceEndpoint(t *testing.T) {
	r := setupTestRouter(t)

	device, _, err := models.CreateDevice("Node Delete", "A")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/devices/"+strconv.Itoa(int(device.ID)), nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
	}

	if _, err := models.GetDeviceByID(device.ID); err == nil {
		t.Fatalf("expected deleted device to be missing")
	}
}

func TestGetDeviceEventsAndOutagesEndpoints(t *testing.T) {
	r := setupTestRouter(t)

	device, _, err := models.CreateDevice("Node E", "A")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}
	if _, err := models.RecordEvent(device.ID, models.EventTypeBoot); err != nil {
		t.Fatalf("RecordEvent failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/devices/"+strconv.Itoa(int(device.ID))+"/events?limit=1&offset=0", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
	}
	body := decodeMapBody(t, resp)
	if body["total"].(float64) < 1 {
		t.Fatalf("expected total events >= 1, got %#v", body["total"])
	}

	outageReq := httptest.NewRequest(http.MethodGet, "/api/devices/"+strconv.Itoa(int(device.ID))+"/outages?limit=5", nil)
	outageResp := httptest.NewRecorder()
	r.ServeHTTP(outageResp, outageReq)
	if outageResp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, outageResp.Code)
	}
}

func TestGetDeviceChartDataEndpoint(t *testing.T) {
	r := setupTestRouter(t)

	device, _, err := models.CreateDevice("Node Chart", "A")
	if err != nil {
		t.Fatalf("CreateDevice failed: %v", err)
	}
	if _, err := models.RecordEvent(device.ID, models.EventTypeHeartbeat); err != nil {
		t.Fatalf("RecordEvent failed: %v", err)
	}

	t.Run("invalid range", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/devices/"+strconv.Itoa(int(device.ID))+"/chart?range=invalid", nil)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("expected status %d, got %d", http.StatusBadRequest, resp.Code)
		}
	})

	t.Run("default range", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/devices/"+strconv.Itoa(int(device.ID))+"/chart", nil)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
		}
		if !strings.Contains(resp.Body.String(), `"buckets"`) {
			t.Fatalf("expected buckets in chart response")
		}
	})
}

func TestGetAllOutagesEndpoint(t *testing.T) {
	r := setupTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/outages?limit=3", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.Code)
	}
}
