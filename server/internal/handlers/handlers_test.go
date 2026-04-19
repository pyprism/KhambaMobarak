package handlers

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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
}
