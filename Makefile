.PHONY: all server client client-esp8266 client-esp32 clean install

# Default target
all: server client

# Build server
server:
	cd server && go build -o khamba ./cmd/khamba/

# Build server with optimizations
server-release:
	cd server && CGO_ENABLED=1 go build -ldflags="-s -w" -o khamba ./cmd/khamba/

# Build client for all platforms
client: client-esp8266 client-esp32

# Build client for ESP8266
client-esp8266:
	cd client && pio run -e nodemcuv2

# Build client for ESP32
client-esp32:
	cd client && pio run -e esp32dev

# Upload to ESP8266 (NodeMCU v2)
upload-esp8266:
	cd client && pio run -e nodemcuv2 -t upload

# Upload to ESP32
upload-esp32:
	cd client && pio run -e esp32dev -t upload

# Upload (auto-detect connected board)
upload:
	cd client && pio run -t upload

# Monitor serial output
monitor:
	cd client && pio device monitor

# Run server
run: server
	./server/khamba serve

# Clean build artifacts
clean:
	cd server && rm -f khamba
	cd client && rm -rf .pio

# Install server dependencies
deps:
	cd server && go mod download

# Run tests
test:
	cd server && go test ./...

# Help
help:
	@echo "Khamba Mobarak - Power Outage Monitor"
	@echo ""
	@echo "Available targets:"
	@echo "  all            - Build both server and client (all platforms)"
	@echo "  server         - Build server binary"
	@echo "  server-release - Build optimized server binary"
	@echo "  client         - Build client for ESP8266 and ESP32"
	@echo "  client-esp8266 - Build client for ESP8266 only"
	@echo "  client-esp32   - Build client for ESP32 only"
	@echo "  upload         - Upload firmware (auto-detect board)"
	@echo "  upload-esp8266 - Upload to ESP8266 (NodeMCU v2)"
	@echo "  upload-esp32   - Upload to ESP32 DevKit"
	@echo "  monitor        - Monitor serial output"
	@echo "  run            - Build and run server"
	@echo "  clean          - Remove build artifacts"
	@echo "  deps           - Download server dependencies"
	@echo "  test           - Run server tests"
	@echo "  help           - Show this help message"

