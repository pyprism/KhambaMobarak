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
	"time"

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

func TestHandleEventDeduplicatesEventID(t *testing.T) {
	r := setupTestRouter(t)
	device, token, err := models.CreateDevice("Retry", "HQ")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		req := httptest.NewRequest(http.MethodPost, "/api/events", bytes.NewBufferString(`{"event_type":"heartbeat","event_id":"retry-1"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.Code)
		}
	}
	_, total, err := models.GetDeviceEvents(device.ID, 10, 0)
	if err != nil || total != 1 {
		t.Fatalf("expected one event after retry, total=%d err=%v", total, err)
	}
}

func TestPaginationAndReportValidation(t *testing.T) {
	r := setupTestRouter(t)
	device, _, err := models.CreateDevice("Report", "HQ")
	if err != nil {
		t.Fatal(err)
	}
	bad := httptest.NewRequest(http.MethodGet, "/api/devices/"+strconv.Itoa(int(device.ID))+"/events?limit=-1", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, bad)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected bad pagination to return 400, got %d", resp.Code)
	}
	report := httptest.NewRequest(http.MethodGet, "/api/devices/"+strconv.Itoa(int(device.ID))+"/report?days=30", nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, report)
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "uptime_percent") {
		t.Fatalf("expected availability report, got %d %s", resp.Code, resp.Body.String())
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

func TestSweepExpiredIngestEntriesEvictsOnlyExpired(t *testing.T) {
	ingestRates.Lock()
	ingestRates.entries = map[string]rateEntry{
		"expired": {count: 5, reset: time.Now().Add(-time.Minute)},
		"active":  {count: 5, reset: time.Now().Add(time.Minute)},
	}
	ingestRates.Unlock()

	sweepExpiredIngestEntries(time.Now())

	ingestRates.Lock()
	_, expiredStillThere := ingestRates.entries["expired"]
	_, activeStillThere := ingestRates.entries["active"]
	ingestRates.Unlock()

	if expiredStillThere {
		t.Fatalf("expected expired entry to be evicted")
	}
	if !activeStillThere {
		t.Fatalf("expected active entry to survive the sweep")
	}
}

func TestCreateAndUpdateDeviceEndpoints(t *testing.T) {
	r := setupTestRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/api/devices", bytes.NewBufferString(`{"name":"New Node","location":"Attic"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d: %s", http.StatusCreated, resp.Code, resp.Body.String())
	}
	body := decodeMapBody(t, resp)
	if body["token"] == nil || body["token"].(string) == "" {
		t.Fatalf("expected a device token in response, got %#v", body)
	}
	device := body["device"].(map[string]any)
	id := int(device["id"].(float64))

	badReq := httptest.NewRequest(http.MethodPost, "/api/devices", bytes.NewBufferString(`{"name":""}`))
	badReq.Header.Set("Content-Type", "application/json")
	badResp := httptest.NewRecorder()
	r.ServeHTTP(badResp, badReq)
	if badResp.Code != http.StatusBadRequest {
		t.Fatalf("expected empty name to be rejected, got %d", badResp.Code)
	}

	patchReq := httptest.NewRequest(http.MethodPatch, "/api/devices/"+strconv.Itoa(id), bytes.NewBufferString(`{"location":"Basement"}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp := httptest.NewRecorder()
	r.ServeHTTP(patchResp, patchReq)
	if patchResp.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, patchResp.Code, patchResp.Body.String())
	}
	patched := decodeMapBody(t, patchResp)
	if patched["name"] != "New Node" || patched["location"] != "Basement" {
		t.Fatalf("expected name unchanged and location updated, got %#v", patched)
	}
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
