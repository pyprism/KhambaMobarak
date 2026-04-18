// Package main is the entry point for the Khamba Mobarak server
package main

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

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
	serveCmd.Flags().StringP("db", "d", "", "Database file path")

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

	// Install command
	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install Khamba as a system service",
		Long:  `Install the Khamba binary to XDG config directory and set up systemd service for auto start.`,
		RunE:  runInstall,
	}

	// Uninstall command
	uninstallCmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall Khamba system service",
		RunE:  runUninstall,
	}

	rootCmd.AddCommand(serveCmd, deviceCmd, installCmd, uninstallCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runServe(cmd *cobra.Command, args []string) error {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Override with flags
	if port, _ := cmd.Flags().GetInt("port"); port > 0 {
		cfg.Port = port
	}
	if dbPath, _ := cmd.Flags().GetString("db"); dbPath != "" {
		cfg.DBPath = dbPath
	}

	// Ensure directories exist
	if err := config.EnsureDirectories(); err != nil {
		return fmt.Errorf("failed to create directories: %w", err)
	}

	// Initialize database
	if err := models.InitDB(cfg.DBPath); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}

	// Set Gin mode
	gin.SetMode(gin.ReleaseMode)

	// Create Gin router
	r := gin.Default()

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

	// Start server
	addr := fmt.Sprintf(":%d", cfg.Port)
	fmt.Printf("Khamba Mobarak Power Monitor\n")
	fmt.Printf("Dashboard: http://localhost%s\n", addr)
	fmt.Printf("Database: %s\n", cfg.DBPath)
	fmt.Printf("Server starting on %s\n", addr)

	return r.Run(addr)
}

func loadTemplates() (*template.Template, error) {
	tmpl := template.New("")

	// Add template functions
	tmpl.Funcs(template.FuncMap{
		"formatDuration": func(d interface{}) string {
			return fmt.Sprintf("%v", d)
		},
	})

	// Get embedded templates filesystem
	templatesFS, err := web.GetTemplatesFS()
	if err != nil {
		return nil, err
	}

	// Read all template files
	entries, err := fs.ReadDir(templatesFS, ".")
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".html") {
			continue
		}

		content, err := fs.ReadFile(templatesFS, entry.Name())
		if err != nil {
			return nil, err
		}

		_, err = tmpl.New(entry.Name()).Parse(string(content))
		if err != nil {
			return nil, err
		}
	}

	return tmpl, nil
}

func runDeviceCreate(cmd *cobra.Command, args []string) error {
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

func runDeviceList(cmd *cobra.Command, args []string) error {
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

func runInstall(cmd *cobra.Command, args []string) error {
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

	// Create default config
	cfg, err := config.DefaultConfig()
	if err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	fmt.Printf("✅ Config created at: %s\n", filepath.Join(configDir, config.ConfigFileName))

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

func runUninstall(cmd *cobra.Command, args []string) error {
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
