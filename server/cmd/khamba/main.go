// Package main is the entry point for the Khamba Mobarak server
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"khamba/internal/api"
	"khamba/internal/config"
	"khamba/internal/handlers"
	"khamba/internal/models"
	"khamba/web"

	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
)

var (
	version = "1.0.0"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	rootCmd := &cobra.Command{
		Use:     "khamba",
		Short:   "Khamba Mobarak - Power Outage Monitor",
		Long:    `A power outage monitoring system that tracks device connectivity and power status.`,
		Version: version,
	}

	// Serve command
	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the server",
		RunE:  runServe,
	}
	serveCmd.Flags().IntP("port", "p", 0, "Port to listen on (default: 8080)")
	serveCmd.Flags().String("host", "", "Bind address (default: all interfaces)")
	serveCmd.Flags().StringP("db", "d", "", "Database file path")
	serveCmd.Flags().Bool("clean", false, "Clear analytics data (events, last_seen) before starting")
	serveCmd.Flags().Bool("reset-analytics", false, "Alias of --clean")
	serveCmd.Flags().Int("offline-threshold", 0, "Seconds without a heartbeat before a device is offline (default: 180)")
	serveCmd.Flags().Int("retention-days", -1, "Days to keep raw events; 0 disables pruning (default: 7)")
	serveCmd.Flags().String("display-timezone", "", "IANA timezone for daily outage bucketing (default: UTC)")

	// Clean command (shortcut for `serve --clean`)
	cleanCmd := &cobra.Command{
		Use:   "clean",
		Short: "Clear analytics data and start the server",
		RunE:  runClean,
	}
	cleanCmd.Flags().IntP("port", "p", 0, "Port to listen on (default: 8080)")
	cleanCmd.Flags().String("host", "", "Bind address (default: all interfaces)")
	cleanCmd.Flags().StringP("db", "d", "", "Database file path")
	cleanCmd.Flags().Bool("clean", true, "Clear analytics data (always enabled for this command)")
	cleanCmd.Flags().Bool("reset-analytics", false, "Alias of --clean")
	cleanCmd.Flags().Int("offline-threshold", 0, "Seconds without a heartbeat before a device is offline (default: 180)")
	cleanCmd.Flags().Int("retention-days", -1, "Days to keep raw events; 0 disables pruning (default: 7)")
	cleanCmd.Flags().String("display-timezone", "", "IANA timezone for daily outage bucketing (default: UTC)")

	// Device commands
	deviceCmd := &cobra.Command{
		Use:   "device",
		Short: "Manage devices",
	}

	deviceCreateCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new device",
		RunE:  runDeviceCreate,
	}
	deviceCreateCmd.Flags().StringP("name", "n", "", "Device name (required)")
	deviceCreateCmd.Flags().StringP("location", "l", "", "Device location")
	deviceCreateCmd.MarkFlagRequired("name")

	deviceListCmd := &cobra.Command{
		Use:   "list",
		Short: "List all devices",
		RunE:  runDeviceList,
	}

	deviceDeleteCmd := &cobra.Command{
		Use:   "delete [id]",
		Short: "Delete a device",
		Args:  cobra.ExactArgs(1),
		RunE:  runDeviceDelete,
	}

	deviceCmd.AddCommand(deviceCreateCmd, deviceListCmd, deviceDeleteCmd)

	// Dummy client command
	dummyClientCmd := &cobra.Command{
		Use:   "dummy-client",
		Short: "Send test events to the server using an auto-managed device token",
		RunE:  runDummyClient,
	}
	dummyClientCmd.Flags().String("server", "", "Server base URL (default: http://localhost:<configured-port>)")
	dummyClientCmd.Flags().String("name", "Dummy Client", "Device name to reuse/create")
	dummyClientCmd.Flags().String("location", "CLI", "Device location when auto-creating")
	dummyClientCmd.Flags().String("event", models.EventTypeHeartbeat, "Event type to send: boot|heartbeat")
	dummyClientCmd.Flags().Int("count", 1, "Number of events to send")
	dummyClientCmd.Flags().Duration("interval", 10*time.Second, "Delay between events")
	dummyClientCmd.Flags().IntP("port", "p", 0, "Port used for default server URL when --server is unset")
	dummyClientCmd.Flags().StringP("db", "d", "", "Database file path")

	// Install command
	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install Khamba as a system service",
		Long:  `Install the Khamba binary to XDG config directory and set up systemd service for auto start.`,
		RunE:  runInstall,
	}
	installCmd.Flags().IntP("port", "p", 0, "Persist server port in config before installing service")
	installCmd.Flags().StringP("db", "d", "", "Persist database file path in config before installing service")

	// Uninstall command
	uninstallCmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall Khamba system service",
		RunE:  runUninstall,
	}

	rootCmd.AddCommand(serveCmd, cleanCmd, deviceCmd, dummyClientCmd, installCmd, uninstallCmd)
	backupCmd := &cobra.Command{Use: "backup [destination]", Short: "Create a consistent SQLite backup", Args: cobra.ExactArgs(1), RunE: runBackup}
	backupCmd.Flags().StringP("db", "d", "", "Database file path")
	restoreCmd := &cobra.Command{Use: "restore [backup]", Short: "Restore a SQLite backup while the server is stopped", Args: cobra.ExactArgs(1), RunE: runRestore}
	restoreCmd.Flags().StringP("db", "d", "", "Database file path")
	rootCmd.AddCommand(backupCmd, restoreCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func databasePathFor(cmd *cobra.Command) (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	db, err := cmd.Flags().GetString("db")
	if err != nil {
		return "", err
	}
	if db != "" {
		cfg.DBPath = db
	}
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	return cfg.DBPath, nil
}
func runBackup(cmd *cobra.Command, args []string) error {
	dbPath, err := databasePathFor(cmd)
	if err != nil {
		return err
	}
	if err := models.InitDB(dbPath); err != nil {
		return err
	}
	if err := models.BackupDatabase(args[0]); err != nil {
		return err
	}
	fmt.Printf("Backup written to %s\n", args[0])
	return nil
}
func runRestore(cmd *cobra.Command, args []string) error {
	dbPath, err := databasePathFor(cmd)
	if err != nil {
		return err
	}
	source, err := os.Open(args[0])
	if err != nil {
		return fmt.Errorf("failed to open backup: %w", err)
	}
	defer source.Close()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return err
	}
	temp := dbPath + ".restore"
	destination, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp, dbPath); err != nil {
		return fmt.Errorf("failed to replace database: %w", err)
	}
	if err := models.InitDB(dbPath); err != nil {
		return fmt.Errorf("restored database is invalid: %w", err)
	}
	fmt.Printf("Restored %s to %s\n", args[0], dbPath)
	return nil
}

func runServe(cmd *cobra.Command, _ []string) error {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Override with flags
	if err := applyConfigOverrides(cmd, cfg); err != nil {
		return err
	}
	if err := applyServeOverrides(cmd, cfg); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid server configuration: %w", err)
	}

	// Ensure directories exist
	if err := config.EnsureDirectories(); err != nil {
		return fmt.Errorf("failed to create directories: %w", err)
	}

	// Initialize database
	if err := models.InitDB(cfg.DBPath); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	models.OfflineThreshold = time.Duration(cfg.OfflineThresholdSeconds) * time.Second
	models.DisplayLocation = cfg.Location()

	cleanAnalytics, _ := cmd.Flags().GetBool("clean")
	resetAnalytics, _ := cmd.Flags().GetBool("reset-analytics")
	if cleanAnalytics || resetAnalytics {
		if err := models.ResetAnalyticsData(); err != nil {
			return fmt.Errorf("failed to reset analytics data: %w", err)
		}
		fmt.Println("Analytics data cleaned (events removed, device last_seen reset).")
	}

	// Set Gin mode
	gin.SetMode(gin.ReleaseMode)

	// Create Gin router with structured (JSON) request logging instead of
	// gin.Default()'s plain-text logger, so systemd/journald consumers can
	// filter and parse it.
	r := gin.New()
	r.Use(gin.Recovery(), slogRequestLogger())

	// Load templates from embedded filesystem
	tmpl, err := loadTemplates()
	if err != nil {
		return fmt.Errorf("failed to load templates: %w", err)
	}
	r.SetHTMLTemplate(tmpl)

	// Serve static assets from embedded filesystem.
	staticFS, err := web.GetStaticFS()
	if err != nil {
		return fmt.Errorf("failed to load static assets: %w", err)
	}
	r.StaticFS("/static", http.FS(staticFS))

	// Register routes
	api.RegisterRoutes(r)
	handlers.RegisterRoutes(r)
	r.NoRoute(func(c *gin.Context) {
		c.HTML(http.StatusNotFound, "error.html", gin.H{"error": "Page not found"})
	})

	// Start event-retention background worker (delete events older than
	// cfg.RetentionDays days). RetentionDays == 0 disables it.
	if cfg.RetentionDays > 0 {
		go func() {
			runCleanup := func() {
				n, err := models.DeleteOldEvents(cfg.RetentionDays)
				if err != nil {
					slog.Error("retention cleanup failed", "error", err)
					return
				}
				if n > 0 {
					slog.Info("retention cleanup", "deleted", n, "retention_days", cfg.RetentionDays)
				}
			}
			runCleanup() // run once at startup
			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				runCleanup()
			}
		}()
	} else {
		slog.Info("event retention disabled (retention-days=0)")
	}

	// Start server
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	fmt.Printf("Khamba Mobarak Power Monitor\n")
	fmt.Printf("Dashboard: http://localhost:%d\n", cfg.Port)
	fmt.Printf("Database: %s\n", cfg.DBPath)
	fmt.Printf("Server starting on %s\n", addr)

	server := &http.Server{
		Addr: addr, Handler: r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Graceful shutdown: stop accepting new connections and let in-flight
	// requests finish when systemd (or an operator) sends SIGINT/SIGTERM,
	// instead of Restart=always killing them mid-request.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		stop()
		fmt.Println("Shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

// slogRequestLogger replaces gin.Logger() with one JSON line per request via
// log/slog, so deployments under systemd get structured, level-aware logs.
func slogRequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		c.Next()
		slog.Info("request",
			"method", c.Request.Method,
			"path", path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}

func runClean(cmd *cobra.Command, args []string) error {
	// Force cleanup behavior for the shortcut command.
	if err := cmd.Flags().Set("clean", "true"); err != nil {
		return fmt.Errorf("failed to enable clean mode: %w", err)
	}
	return runServe(cmd, args)
}

func loadTemplates() (*template.Template, error) {
	// Get embedded templates filesystem
	templatesFS, err := web.GetTemplatesFS()
	if err != nil {
		return nil, err
	}

	// Create root template. Duration formatting is done client-side (see
	// base.html's formatDuration JS helper); templates don't need a Go
	// FuncMap entry for it.
	root := template.New("")

	// Read all template files
	entries, err := fs.ReadDir(templatesFS, ".")
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".html") {
			continue
		}

		content, err := fs.ReadFile(templatesFS, name)
		if err != nil {
			return nil, err
		}

		// Parse all templates into the same root template set.
		// Each file uses {{ define "filename.html" }} to define its template.
		// base.html defines shared partials: styles, navbar, footer, scripts.
		_, err = root.Parse(string(content))
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", name, err)
		}
	}

	return root, nil
}

func applyConfigOverrides(cmd *cobra.Command, cfg *config.Config) error {
	port, err := cmd.Flags().GetInt("port")
	if err != nil {
		return fmt.Errorf("failed to read port flag: %w", err)
	}
	if port > 0 {
		cfg.Port = port
	}

	dbPath, err := cmd.Flags().GetString("db")
	if err != nil {
		return fmt.Errorf("failed to read db flag: %w", err)
	}
	if dbPath != "" {
		cfg.DBPath = dbPath
	}

	return cfg.Validate()
}

// applyServeOverrides applies the serve/clean-only flags (host, offline
// threshold, retention, display timezone) on top of applyConfigOverrides.
func applyServeOverrides(cmd *cobra.Command, cfg *config.Config) error {
	host, err := cmd.Flags().GetString("host")
	if err != nil {
		return fmt.Errorf("failed to read host flag: %w", err)
	}
	if host != "" {
		cfg.Host = host
	}

	threshold, err := cmd.Flags().GetInt("offline-threshold")
	if err != nil {
		return fmt.Errorf("failed to read offline-threshold flag: %w", err)
	}
	if threshold > 0 {
		cfg.OfflineThresholdSeconds = threshold
	}

	retention, err := cmd.Flags().GetInt("retention-days")
	if err != nil {
		return fmt.Errorf("failed to read retention-days flag: %w", err)
	}
	if retention >= 0 {
		cfg.RetentionDays = retention
	}

	tz, err := cmd.Flags().GetString("display-timezone")
	if err != nil {
		return fmt.Errorf("failed to read display-timezone flag: %w", err)
	}
	if tz != "" {
		cfg.DisplayTimezone = tz
	}

	return cfg.Validate()
}

func runDeviceCreate(cmd *cobra.Command, _ []string) error {
	name, _ := cmd.Flags().GetString("name")
	location, _ := cmd.Flags().GetString("location")

	// Load config and initialize DB
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := config.EnsureDirectories(); err != nil {
		return err
	}
	if err := models.InitDB(cfg.DBPath); err != nil {
		return err
	}

	device, token, err := models.CreateDevice(name, location)
	if err != nil {
		return fmt.Errorf("failed to create device: %w", err)
	}

	fmt.Println("Device created successfully!")
	fmt.Println()
	fmt.Printf("  ID:       %d\n", device.ID)
	fmt.Printf("  Name:     %s\n", device.Name)
	fmt.Printf("  Location: %s\n", device.Location)
	fmt.Println()
	fmt.Println("Device Token (save this, it won't be shown again):")
	fmt.Println()
	fmt.Printf("  %s\n", token)
	fmt.Println()
	fmt.Println("Use this token in your ESP device configuration.")

	return nil
}

func runDeviceList(_ *cobra.Command, _ []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := models.InitDB(cfg.DBPath); err != nil {
		return err
	}

	devices, err := models.GetAllDevices()
	if err != nil {
		return fmt.Errorf("failed to get devices: %w", err)
	}

	if len(devices) == 0 {
		fmt.Println("No devices registered.")
		fmt.Println("Use 'khamba device create --name \"Device Name\"' to add a device.")
		return nil
	}

	fmt.Printf("%-4s %-20s %-25s %-10s %-20s\n", "ID", "Name", "Location", "Status", "Last Seen")
	fmt.Println(strings.Repeat("-", 85))

	for _, d := range devices {
		status := "Offline"
		if d.IsOnline {
			status = "Online"
		}
		lastSeen := "Never"
		if d.LastSeen != nil {
			lastSeen = d.LastSeen.Format("2006-01-02 15:04:05")
		}
		location := d.Location
		if location == "" {
			location = "-"
		}
		fmt.Printf("%-4d %-20s %-25s %-10s %-20s\n", d.ID, d.Name, location, status, lastSeen)
	}

	return nil
}

func runDeviceDelete(cmd *cobra.Command, args []string) error {
	var id uint
	if _, err := fmt.Sscanf(args[0], "%d", &id); err != nil {
		return fmt.Errorf("invalid device ID: %s", args[0])
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := models.InitDB(cfg.DBPath); err != nil {
		return err
	}

	device, err := models.GetDeviceByID(id)
	if err != nil {
		return fmt.Errorf("device not found: %w", err)
	}

	if err := models.DeleteDevice(id); err != nil {
		return fmt.Errorf("failed to delete device: %w", err)
	}

	fmt.Printf("✅ Device '%s' (ID: %d) deleted successfully.\n", device.Name, device.ID)
	return nil
}

func runDummyClient(cmd *cobra.Command, _ []string) error {
	eventType, _ := cmd.Flags().GetString("event")
	if eventType != models.EventTypeBoot && eventType != models.EventTypeHeartbeat {
		return fmt.Errorf("invalid event type %q (must be boot or heartbeat)", eventType)
	}

	count, _ := cmd.Flags().GetInt("count")
	if count <= 0 {
		return fmt.Errorf("count must be greater than 0")
	}

	interval, _ := cmd.Flags().GetDuration("interval")
	if interval < 0 {
		return fmt.Errorf("interval cannot be negative")
	}

	name, _ := cmd.Flags().GetString("name")
	location, _ := cmd.Flags().GetString("location")
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("device name cannot be empty")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if err := applyConfigOverrides(cmd, cfg); err != nil {
		return err
	}
	if err := config.EnsureDirectories(); err != nil {
		return fmt.Errorf("failed to create directories: %w", err)
	}
	if err := models.InitDB(cfg.DBPath); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}

	serverURL, _ := cmd.Flags().GetString("server")
	if strings.TrimSpace(serverURL) == "" {
		serverURL = fmt.Sprintf("http://localhost:%d", cfg.Port)
	}
	serverURL = strings.TrimSpace(serverURL)
	if _, err := url.ParseRequestURI(serverURL); err != nil {
		return fmt.Errorf("invalid server URL %q: %w", serverURL, err)
	}

	device, token, created, err := models.GetOrCreateDeviceByName(name, location)
	if err != nil {
		return fmt.Errorf("failed to resolve device token: %w", err)
	}

	if created {
		fmt.Printf("Created device '%s' (ID: %d) and generated token.\n", device.Name, device.ID)
	} else {
		fmt.Printf("Reusing device '%s' (ID: %d) token.\n", device.Name, device.ID)
	}

	endpoint := strings.TrimRight(serverURL, "/") + "/api/events"
	for i := 0; i < count; i++ {
		if err := postDummyEvent(endpoint, token, eventType); err != nil {
			return fmt.Errorf("event %d/%d failed: %w", i+1, count, err)
		}
		fmt.Printf("Sent %s event %d/%d to %s\n", eventType, i+1, count, endpoint)

		if i < count-1 && interval > 0 {
			time.Sleep(interval)
		}
	}

	return nil
}

func postDummyEvent(endpoint, token, eventType string) error {
	payload := map[string]any{
		"event_type": eventType,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to encode payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("request error: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil
}

func runInstall(cmd *cobra.Command, _ []string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd installation is only supported on Linux")
	}

	// Get paths
	configDir, err := config.GetConfigDir()
	if err != nil {
		return err
	}

	// Create directories
	if err := config.EnsureDirectories(); err != nil {
		return err
	}

	// Copy binary
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	binPath := filepath.Join(configDir, "khamba")

	// Read and copy binary
	input, err := os.ReadFile(execPath)
	if err != nil {
		return fmt.Errorf("failed to read binary: %w", err)
	}
	if err := os.WriteFile(binPath, input, 0755); err != nil {
		return fmt.Errorf("failed to copy binary: %w", err)
	}

	fmt.Printf("✅ Binary installed to: %s\n", binPath)

	// Load existing config (or defaults if not created yet), then persist install overrides.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if err := applyConfigOverrides(cmd, cfg); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✅ Config saved at: %s\n", filepath.Join(configDir, config.ConfigFileName))
	fmt.Printf("   Port: %d\n", cfg.Port)
	fmt.Printf("   Database: %s\n", cfg.DBPath)

	// Create systemd user service
	homeDir, _ := os.UserHomeDir()
	systemdDir := filepath.Join(homeDir, ".config", "systemd", "user")
	if err := os.MkdirAll(systemdDir, 0755); err != nil {
		return fmt.Errorf("failed to create systemd directory: %w", err)
	}

	serviceContent := fmt.Sprintf(`[Unit]
Description=Khamba Mobarak Power Outage Monitor
After=network.target

[Service]
Type=simple
ExecStart=%s serve
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, binPath)

	servicePath := filepath.Join(systemdDir, "khamba.service")
	if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
		return fmt.Errorf("failed to create service file: %w", err)
	}
	fmt.Printf("✅ Systemd service created: %s\n", servicePath)

	// Enable and start service
	fmt.Println("\n To enable and start the service, run:")
	fmt.Println("   systemctl --user daemon-reload")
	fmt.Println("   systemctl --user enable khamba")
	fmt.Println("   systemctl --user start khamba")
	fmt.Println("\n To enable lingering (run without login):")
	fmt.Println("   sudo loginctl enable-linger $USER")

	return nil
}

func runUninstall(_ *cobra.Command, _ []string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd uninstallation is only supported on Linux")
	}

	// Stop service if running
	exec.Command("systemctl", "--user", "stop", "khamba").Run()
	exec.Command("systemctl", "--user", "disable", "khamba").Run()

	homeDir, _ := os.UserHomeDir()
	servicePath := filepath.Join(homeDir, ".config", "systemd", "user", "khamba.service")

	if err := os.Remove(servicePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove service file: %w", err)
	}

	configDir, _ := config.GetConfigDir()
	binPath := filepath.Join(configDir, "khamba")
	os.Remove(binPath)

	exec.Command("systemctl", "--user", "daemon-reload").Run()

	fmt.Println(" Khamba service uninstalled.")
	fmt.Println(" Note: Config and database files were not removed.")
	fmt.Printf("   Config dir: %s\n", configDir)

	return nil
}
