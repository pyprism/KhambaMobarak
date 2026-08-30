/**
 * Khamba Mobarak - Power Outage Monitor Client
 * ESP8266/ESP32 firmware for monitoring power status
 *
 * Features:
 * - WiFiManager for configuration (server URL, device token)
 * - Sends "boot" event on power restoration
 * - Sends heartbeat every 60 seconds
 * - Persistent configuration storage using LittleFS
 *
 * Supports: ESP8266, ESP32, ESP32-C3, ESP32-S2, ESP32-S3
 */

#include <Arduino.h>
#include <ArduinoJson.h>

// Platform-specific includes
#if defined(ESP32)
    #include <WiFi.h>
    #include <HTTPClient.h>
    #include <WiFiClientSecure.h>
    #include <LittleFS.h>
	#include <esp_system.h>
    #define FILESYSTEM LittleFS
#elif defined(ESP8266)
    #include <ESP8266WiFi.h>
    #include <ESP8266HTTPClient.h>
    #include <WiFiClientSecure.h>
    #include <LittleFS.h>
    #define FILESYSTEM LittleFS
#else
    #error "Unsupported platform! Use ESP8266 or ESP32."
#endif

#include <WiFiManager.h>

// Configuration
#define CONFIG_FILE "/config.json"
#define EVENT_QUEUE_FILE "/event-queue.json"
#define MAX_QUEUED_EVENTS 16
#define HEARTBEAT_INTERVAL 60000  // 60 seconds in milliseconds
// Must stay below the server's offline threshold (default 3x heartbeat, i.e.
// 180s) or a device stuck in max backoff gets marked offline while it's
// still actively (if slowly) retrying.
#define MAX_RETRY_DELAY 120000    // 2 minutes max backoff
#define INITIAL_RETRY_DELAY 5000  // 5 seconds initial retry
#define WIFI_RECOVERY_AFTER_MS 120000
#define SETUP_PASSWORD_PREFIX "KM-"

// LED indicator (platform-specific)
#if defined(ESP32)
    #ifndef LED_BUILTIN
        #define LED_BUILTIN 2  // Common ESP32 DevKit LED pin
    #endif
    #define LED_ON HIGH
    #define LED_OFF LOW
#elif defined(ESP8266)
    #define LED_ON LOW   // ESP8266 LED is active LOW
    #define LED_OFF HIGH
#endif

// KHAMBA_LED_PIN lets a board's platformio.ini env override the status LED
// pin (set via -D KHAMBA_LED_PIN=<n>) without colliding with the core's own
// LED_BUILTIN definition, which on some boards (e.g. ESP32-C3-DevKitM-1) is
// a typed `static const uint8_t` rather than a plain macro number and can't
// be safely redefined from the command line.
#ifndef KHAMBA_LED_PIN
    #define KHAMBA_LED_PIN LED_BUILTIN
#endif
#define LED_PIN KHAMBA_LED_PIN

// BOOT/FLASH button is used for factory reset of saved configuration.
// Override RESET_BUTTON_PIN via build flags if your board wiring differs.
#ifndef RESET_BUTTON_PIN
    #define RESET_BUTTON_PIN 0
#endif
#define RESET_BUTTON_ACTIVE LOW
#define RESET_HOLD_TIME_MS 5000

// Configuration structure. The CA certificate (up to 2KB of PEM) is
// deliberately NOT kept here: it's loaded on demand from CONFIG_FILE only
// when an HTTPS request is about to be made, so it doesn't permanently
// occupy 2KB of RAM on ESP8266.
struct Config {
    char serverUrl[128];
	char deviceToken[65];  // 64 hex chars + null terminator
    bool configured;
};

Config config;
unsigned long lastHeartbeat = 0;
// Shared by the boot-retry and heartbeat-retry phases; safe because they
// never overlap (boot retries own the loop exclusively until bootEventSent).
unsigned long retryDelay = INITIAL_RETRY_DELAY;
unsigned long nextRetryAt = 0;
unsigned long wifiDisconnectedAt = 0;
bool bootEventSent = false;
// Each in-flight send keeps its own event ID so a retry reuses the same ID
// instead of minting a new one (which would let the server record the same
// boot/heartbeat twice). Kept separate per flow so the boot retry and the
// heartbeat cycle never stomp on each other.
String bootEventID;
String heartbeatEventID;
bool replayingQueue = false;

// Function declarations
void loadConfig();
bool saveConfig(const char* serverCA);
bool loadServerCA(String& out);
bool requiresCA(const char* url);
bool performRequest(HTTPClient& http, const String& payload);
bool sendEvent(const char* eventType, String& eventID);
void blinkLed(int times, int delayMs);
void setupWiFiManager();
bool initFilesystem();
bool isFactoryResetRequested();
void clearSavedConfiguration();
bool validConfig(const char* url, const char* token);
bool isPrivateHost(const String& url);
#if defined(ESP32)
String resetReasonString();
#endif
String nextEventID();
void queueEvent(const char* eventType, const String& eventID);
void replayQueuedEvents();

void setup() {
    Serial.begin(115200);
    delay(1000);
    randomSeed(micros());

    Serial.println("\n\n");
    Serial.println("========================================");
    Serial.println("  Khamba Mobarak - Power Monitor");
    #if defined(ESP32)
    Serial.println("  Platform: ESP32");
    #elif defined(ESP8266)
    Serial.println("  Platform: ESP8266");
    #endif
    Serial.println("========================================");

    // Initialize LED
    pinMode(LED_PIN, OUTPUT);
    digitalWrite(LED_PIN, LED_OFF);

    // BOOT/FLASH button (active LOW with pull-up)
    pinMode(RESET_BUTTON_PIN, INPUT_PULLUP);

    // Initialize filesystem
    if (!initFilesystem()) {
        Serial.println("[FATAL] Storage is unavailable; configuration cannot be safely saved.");
        blinkLed(10, 80);
        delay(5000);
        ESP.restart();
    }

    // Load configuration
    loadConfig();

    // Hold BOOT/FLASH for RESET_HOLD_TIME_MS during startup to wipe config.
    if (isFactoryResetRequested()) {
        clearSavedConfiguration();
    }

    // Setup WiFiManager
    setupWiFiManager();

    Serial.println("[INFO] WiFi connected!");
    Serial.print("[INFO] IP Address: ");
    Serial.println(WiFi.localIP());
    Serial.print("[INFO] RSSI: ");
    Serial.print(WiFi.RSSI());
    Serial.println(" dBm");

    // Visual feedback - connected
    blinkLed(3, 200);
}

bool isFactoryResetRequested() {
    // Button must stay continuously pressed for the full window.
    if (digitalRead(RESET_BUTTON_PIN) != RESET_BUTTON_ACTIVE) {
        return false;
    }

    Serial.printf("[INFO] Reset button detected. Hold for %d ms to clear config...\n", RESET_HOLD_TIME_MS);
    unsigned long start = millis();
    while (millis() - start < RESET_HOLD_TIME_MS) {
        if (digitalRead(RESET_BUTTON_PIN) != RESET_BUTTON_ACTIVE) {
            Serial.println("[INFO] Reset canceled");
            return false;
        }
        blinkLed(1, 60);
        delay(80);
    }

    Serial.println("[WARN] Factory reset requested");
    return true;
}

void clearSavedConfiguration() {
    Serial.println("[WARN] Clearing saved device configuration...");

    if (FILESYSTEM.exists(CONFIG_FILE)) {
        if (FILESYSTEM.remove(CONFIG_FILE)) {
            Serial.println("[OK] Removed local config file");
        } else {
            Serial.println("[ERROR] Failed to remove local config file");
        }
    }

    // Clear WiFi credentials stored by WiFiManager/SDK.
    WiFiManager wm;
    wm.resetSettings();

    #if defined(ESP32)
    WiFi.disconnect(true, true);
    #elif defined(ESP8266)
    WiFi.disconnect(true);
    #endif

    config.serverUrl[0] = '\0';
    config.deviceToken[0] = '\0';
    config.configured = false;

    Serial.println("[OK] Configuration cleared. Rebooting...");
    blinkLed(5, 120);
    delay(300);
    ESP.restart();
}

void loop() {
    // Check WiFi connection
    if (WiFi.status() != WL_CONNECTED) {
        Serial.println("[WARN] WiFi disconnected, reconnecting...");
		if (wifiDisconnectedAt == 0) wifiDisconnectedAt = millis();
		if (millis() - wifiDisconnectedAt >= WIFI_RECOVERY_AFTER_MS) {
			Serial.println("[WARN] WiFi recovery timeout; opening secured setup portal");
			setupWiFiManager();
			wifiDisconnectedAt = 0;
		}
        WiFi.reconnect();
        delay(5000);
        return;
    }
	wifiDisconnectedAt = 0;

    // Send boot event (only once after power restoration)
	replayQueuedEvents();
    unsigned long currentMillis = millis();
    // Signed-subtraction comparison: rollover-safe across the ~49.7-day
    // millis() wraparound (unlike a plain currentMillis >= nextRetryAt).
    bool retryDue = (long)(currentMillis - nextRetryAt) >= 0;
    if (!bootEventSent && config.configured) {
        if (!retryDue) {
            delay(1000);
            return;
        }
        Serial.println("[INFO] Sending boot event...");
        if (sendEvent("boot", bootEventID)) {
            bootEventSent = true;
            retryDelay = INITIAL_RETRY_DELAY;
            Serial.println("[OK] Boot event sent successfully");
            blinkLed(2, 100);
        } else {
            Serial.println("[ERROR] Failed to send boot event, will retry...");
            nextRetryAt = currentMillis + retryDelay + random(0, 1000);
            retryDelay = min(retryDelay * 2, (unsigned long)MAX_RETRY_DELAY);
        }
        return;
    }

    // Send heartbeat every HEARTBEAT_INTERVAL
    if (config.configured && retryDue && (unsigned long)(currentMillis - lastHeartbeat) >= HEARTBEAT_INTERVAL) {
        Serial.println("[INFO] Sending heartbeat...");
        if (sendEvent("heartbeat", heartbeatEventID)) {
            lastHeartbeat = currentMillis;
            retryDelay = INITIAL_RETRY_DELAY;
			nextRetryAt = currentMillis + HEARTBEAT_INTERVAL;
            Serial.println("[OK] Heartbeat sent");
            blinkLed(1, 50);
        } else {
            Serial.println("[ERROR] Failed to send heartbeat");
			nextRetryAt = currentMillis + retryDelay + random(0, 1000);
			retryDelay = min(retryDelay * 2, (unsigned long)MAX_RETRY_DELAY);
        }
    }

    delay(1000); // Small delay to prevent tight loop
}

bool initFilesystem() {
    Serial.println("[INFO] Initializing filesystem...");

    #if defined(ESP32)
    if (!LittleFS.begin(true)) {  // true = format if mount fails
        Serial.println("[ERROR] Failed to mount LittleFS");
		return false;
    }
    #elif defined(ESP8266)
    if (!LittleFS.begin()) {
        Serial.println("[ERROR] Failed to mount LittleFS");
        Serial.println("[INFO] Formatting LittleFS...");
		if (!LittleFS.format() || !LittleFS.begin()) return false;
    }
    #endif

    Serial.println("[OK] Filesystem mounted");
	return true;
}

// requiresCA reports whether url needs a CA certificate on file (HTTPS).
// Plain http:// is only ever valid for a LAN host (see validConfig), which
// needs no certificate.
bool requiresCA(const char* url) {
    return strncmp(url, "https://", 8) == 0;
}

// loadServerCA reads the PEM CA certificate out of CONFIG_FILE on demand,
// so it doesn't have to live in the global Config struct permanently.
bool loadServerCA(String& out) {
    if (!FILESYSTEM.exists(CONFIG_FILE)) return false;
    File file = FILESYSTEM.open(CONFIG_FILE, "r");
    if (!file) return false;
    JsonDocument doc;
    DeserializationError error = deserializeJson(doc, file);
    file.close();
    if (error) return false;
    out = doc["serverCA"] | "";
    return out.length() > 0;
}

void loadConfig() {
    Serial.println("[INFO] Loading configuration...");

    config.configured = false;
    config.serverUrl[0] = '\0';
    config.deviceToken[0] = '\0';

    if (FILESYSTEM.exists(CONFIG_FILE)) {
        File file = FILESYSTEM.open(CONFIG_FILE, "r");
        if (file) {
            JsonDocument doc;
            DeserializationError error = deserializeJson(doc, file);
            file.close();

            if (!error) {
                strlcpy(config.serverUrl, doc["serverUrl"] | "", sizeof(config.serverUrl));
                strlcpy(config.deviceToken, doc["deviceToken"] | "", sizeof(config.deviceToken));
                config.configured = validConfig(config.serverUrl, config.deviceToken);
                if (config.configured && requiresCA(config.serverUrl)) {
                    String ca;
                    config.configured = loadServerCA(ca);
                }

                Serial.println("[INFO] Configuration loaded:");
                Serial.print("  Server URL: ");
                Serial.println(config.serverUrl);
                Serial.print("  Device Token: ");
                Serial.println(config.deviceToken[0] ? "****" : "(not set)");
            } else {
                Serial.print("[ERROR] Failed to parse config: ");
                Serial.println(error.c_str());
            }
        }
    } else {
        Serial.println("[INFO] No configuration file found");
    }
}

bool saveConfig(const char* serverCA) {
    Serial.println("[INFO] Saving configuration...");

    File file = FILESYSTEM.open(CONFIG_FILE, "w");
    if (file) {
        JsonDocument doc;
        doc["serverUrl"] = config.serverUrl;
        doc["deviceToken"] = config.deviceToken;
		doc["serverCA"] = serverCA;

        if (serializeJson(doc, file)) {
            Serial.println("[OK] Configuration saved");
			file.close();
			return true;
        } else {
            Serial.println("[ERROR] Failed to write config");
        }
        file.close();
    } else {
        Serial.println("[ERROR] Failed to open config file for writing");
    }
	return false;
}

// performRequest sends the already-built payload over an already-begin()'d
// HTTPClient and reports whether the server accepted it. Split out so both
// the HTTPS and plain-HTTP branches of sendEvent can share it while their
// respective WiFiClient(Secure) stays in scope for the whole call.
bool performRequest(HTTPClient& http, const String& payload) {
    http.addHeader("Content-Type", "application/json");
    http.addHeader("Authorization", String("Bearer ") + config.deviceToken);
    http.setTimeout(10000); // 10 second timeout

    int httpCode = http.POST(payload);
    String response = http.getString();
    http.end();

    Serial.print("[DEBUG] HTTP Response Code: ");
    Serial.println(httpCode);
    Serial.print("[DEBUG] Response: ");
    Serial.println(response);

    return httpCode == 200 || httpCode == 201;
}

// sendEvent posts one event. eventID is an in/out parameter: if empty, a
// fresh ID is minted and left in place on failure (so the caller's next
// retry reuses it instead of the server recording a duplicate); it's reset
// to empty on success.
bool sendEvent(const char* eventType, String& eventID) {
    if (!config.configured) {
        Serial.println("[WARN] Device not configured");
        return false;
    }

    String url = String(config.serverUrl);
    if (!url.endsWith("/")) {
        url += "/";
    }
    url += "api/events";

    Serial.print("[DEBUG] Sending to: ");
    Serial.println(url);

    if (eventID.length() == 0) eventID = nextEventID();

    JsonDocument doc;
    doc["event_type"] = eventType;
    doc["event_id"] = eventID;
	#if defined(ESP32)
		doc["reset_reason"] = resetReasonString();
	#else
		doc["reset_reason"] = ESP.getResetReason();
	#endif

    String payload;
    serializeJson(doc, payload);

    Serial.print("[DEBUG] Payload: ");
    Serial.println(payload);

    HTTPClient http;
    bool accepted = false;

    if (url.startsWith("https://")) {
        String ca;
        if (!loadServerCA(ca)) {
            Serial.println("[ERROR] HTTPS configured but no CA certificate is stored");
            return false;
        }
        // A stack transport prevents heap fragmentation; both ESP platforms use it.
        WiFiClientSecure secureClient;
        #if defined(ESP32)
            secureClient.setCACert(ca.c_str());
        #else
            BearSSL::X509List caCert(ca.c_str());
            secureClient.setTrustAnchors(&caCert);
        #endif
        if (!http.begin(secureClient, url)) {
            Serial.println("[ERROR] Failed to initialize HTTPS client");
            return false;
        }
        accepted = performRequest(http, payload);
    } else {
        // Only reached for http:// URLs, which validConfig only accepts for
        // private/LAN hosts.
        WiFiClient plainClient;
        if (!http.begin(plainClient, url)) {
            Serial.println("[ERROR] Failed to initialize HTTP client");
            return false;
        }
        accepted = performRequest(http, payload);
    }

	if (!accepted && !replayingQueue) queueEvent(eventType, eventID);
	if (accepted) eventID = "";
	return accepted;
}

void queueEvent(const char* eventType, const String& eventID) {
	if (eventID.length() == 0) return;
	JsonDocument doc;
	File in = FILESYSTEM.open(EVENT_QUEUE_FILE, "r");
	if (in) { deserializeJson(doc, in); in.close(); }
	JsonArray events = doc["events"].to<JsonArray>();
	while (events.size() >= MAX_QUEUED_EVENTS) events.remove(0);
	JsonObject event = events.add<JsonObject>();
	event["type"] = eventType; event["id"] = eventID; event["timestamp"] = millis();
	File out = FILESYSTEM.open(EVENT_QUEUE_FILE, "w");
	if (!out || !serializeJson(doc, out)) Serial.println("[ERROR] Failed to queue event");
	if (out) out.close();
}

void replayQueuedEvents() {
	if (!FILESYSTEM.exists(EVENT_QUEUE_FILE) || replayingQueue) return;
	JsonDocument doc;
	File in = FILESYSTEM.open(EVENT_QUEUE_FILE, "r");
	if (!in || deserializeJson(doc, in)) { if (in) in.close(); return; }
	in.close(); JsonArray events = doc["events"].as<JsonArray>();
	if (events.isNull() || events.size() == 0) { FILESYSTEM.remove(EVENT_QUEUE_FILE); return; }

	replayingQueue = true;
	// Built fresh (rather than mutating the loaded doc in place) so the
	// written file only ever has an "events" key — reusing doc left a stale
	// "remaining" key behind too, so every flush persisted both arrays and
	// the file grew forever.
	JsonDocument outDoc;
	JsonArray remaining = outDoc["events"].to<JsonArray>();
	for (JsonObject event : events) {
		String id = event["id"].as<String>();
		if (!sendEvent(event["type"] | "heartbeat", id)) remaining.add(event);
	}
	replayingQueue = false;

	File out = FILESYSTEM.open(EVENT_QUEUE_FILE, "w");
	if (out) { serializeJson(outDoc, out); out.close(); }
}

bool validConfig(const char* url, const char* token) {
    if (strlen(token) != 64) return false;
    for (size_t i = 0; i < 64; ++i) if (!isxdigit((unsigned char)token[i])) return false;

    String u(url);
    if (u.startsWith("https://")) return true;
    // Plain HTTP is only acceptable for a private/LAN deployment; a
    // reusable bearer token must never travel in cleartext over the
    // internet.
    return u.startsWith("http://") && isPrivateHost(u);
}

// isPrivateHost reports whether url's host is a LAN-only address:
// localhost, a .local mDNS name, or an RFC1918 IPv4 literal.
bool isPrivateHost(const String& url) {
    int schemeEnd = url.indexOf("://");
    if (schemeEnd < 0) return false;
    String rest = url.substring(schemeEnd + 3);
    int pathIdx = rest.indexOf('/');
    if (pathIdx >= 0) rest = rest.substring(0, pathIdx);
    int portIdx = rest.indexOf(':');
    String host = portIdx >= 0 ? rest.substring(0, portIdx) : rest;
    host.toLowerCase();

    if (host == "localhost" || host.endsWith(".local")) return true;

    // Parse as a dotted-quad IPv4 address; anything else (a public hostname)
    // is not treated as LAN-private.
    int octets[4];
    int idx = 0, start = 0;
    for (int i = 0; i <= (int)host.length(); i++) {
        if (i != (int)host.length() && host[i] != '.') continue;
        if (idx >= 4 || i == start) return false;
        String segment = host.substring(start, i);
        for (size_t j = 0; j < segment.length(); j++) {
            if (!isDigit(segment[j])) return false;
        }
        int value = segment.toInt();
        if (value < 0 || value > 255) return false;
        octets[idx++] = value;
        start = i + 1;
    }
    if (idx != 4) return false;

    if (octets[0] == 10) return true;
    if (octets[0] == 172 && octets[1] >= 16 && octets[1] <= 31) return true;
    if (octets[0] == 192 && octets[1] == 168) return true;
    if (octets[0] == 127) return true;
    return false;
}

#if defined(ESP32)
// resetReasonString maps esp_reset_reason() to a human-readable string, so
// the server can classify outage cause/confidence from it (see
// classifyOutage server-side) the same way it already can for ESP8266's
// descriptive ESP.getResetReason() strings.
String resetReasonString() {
    switch (esp_reset_reason()) {
        case ESP_RST_POWERON:   return "Power on";
        case ESP_RST_EXT:       return "External Reset";
        case ESP_RST_SW:        return "Software Reset";
        case ESP_RST_PANIC:     return "Exception/Panic";
        case ESP_RST_INT_WDT:   return "Interrupt Watchdog";
        case ESP_RST_TASK_WDT:  return "Task Watchdog";
        case ESP_RST_WDT:       return "Other Watchdog";
        case ESP_RST_DEEPSLEEP: return "Deep-Sleep Wake";
        case ESP_RST_BROWNOUT:  return "Brownout Reset";
        case ESP_RST_SDIO:      return "SDIO Reset";
        default:                return "Unknown";
    }
}
#endif

String nextEventID() {
    // The combination is stable enough for a single queued/retried submission
    // when callers persist it; the server also deduplicates device/event IDs.
	#if defined(ESP32)
		String chip = String((uint32_t)ESP.getEfuseMac(), HEX);
	#else
		String chip = String(ESP.getChipId(), HEX);
	#endif
    return chip + "-" + String(millis(), HEX) + "-" + String(random(0xffff), HEX);
}

void blinkLed(int times, int delayMs) {
    for (int i = 0; i < times; i++) {
        digitalWrite(LED_PIN, LED_ON);
        delay(delayMs);
        digitalWrite(LED_PIN, LED_OFF);
        delay(delayMs);
    }
}

void setupWiFiManager() {
    WiFiManager wm;

    // Best-effort load of whatever CA is already on file, so re-opening the
    // portal (e.g. after a WiFi recovery timeout) doesn't blank the field.
    String existingCA;
    loadServerCA(existingCA);

    // Secrets are deliberately not pre-filled: anyone in radio range of the
    // temporary portal must not be able to read a reusable bearer token.
    WiFiManagerParameter customServerUrl("server", "Server URL", config.serverUrl, 128);
    WiFiManagerParameter customDeviceToken("token", "Device Token", "", 65, "type=\"password\" autocomplete=\"new-password\"");
	WiFiManagerParameter customServerCA("ca", "Server CA certificate (PEM, only needed for https://)", existingCA.c_str(), 2048, "type=\"textarea\"");

    wm.addParameter(&customServerUrl);
    wm.addParameter(&customDeviceToken);
	wm.addParameter(&customServerCA);

    // Set custom HTML head for better styling
    wm.setCustomHeadElement("<style>"
        "body { font-family: Arial, sans-serif; }"
        ".c { text-align: center; }"
        "h1 { color: #333; }"
        ".msg { padding: 10px; background: #e7f3fe; border-left: 4px solid #2196F3; margin: 10px 0; }"
        "</style>");

    // Set config portal title
    wm.setTitle("Khamba Mobarak Setup");

    // Set timeout for config portal (5 minutes)
    wm.setConfigPortalTimeout(300);

    // Callback when config is saved
    wm.setSaveParamsCallback([&customServerUrl, &customDeviceToken, &customServerCA]() {
        Serial.println("[INFO] WiFiManager params saved callback");
        strlcpy(config.serverUrl, customServerUrl.getValue(), sizeof(config.serverUrl));
		if (strlen(customDeviceToken.getValue()) > 0) strlcpy(config.deviceToken, customDeviceToken.getValue(), sizeof(config.deviceToken));
		const char* ca = customServerCA.getValue();
        config.configured = validConfig(config.serverUrl, config.deviceToken);
        if (config.configured && requiresCA(config.serverUrl) && strlen(ca) == 0) config.configured = false;
		if (!config.configured || !saveConfig(ca)) Serial.println("[ERROR] Configuration was not saved; correct the URL (https://, or http:// on a private LAN address), the 64-hex token, and the CA certificate if using https.");
    });

	String suffix;
	#if defined(ESP32)
		suffix = String((uint32_t)ESP.getEfuseMac(), HEX);
	#else
		suffix = String(ESP.getChipId(), HEX);
	#endif
	String password = String(SETUP_PASSWORD_PREFIX) + suffix.substring(max(0, (int)suffix.length() - 6));
	Serial.printf("[INFO] Setup AP password: %s\n", password.c_str());
    bool connected = wm.autoConnect("KhambaMobarak-Setup", password.c_str());

    if (!connected) {
        Serial.println("[ERROR] Failed to connect to WiFi");
        Serial.println("[INFO] Restarting in 5 seconds...");
        delay(5000);
        ESP.restart();
    }

    // Check if new parameters were entered
    if (strlen(customServerUrl.getValue()) > 0) {
        strlcpy(config.serverUrl, customServerUrl.getValue(), sizeof(config.serverUrl));
    }
    if (strlen(customDeviceToken.getValue()) > 0) {
        strlcpy(config.deviceToken, customDeviceToken.getValue(), sizeof(config.deviceToken));
    }

    const char* ca = customServerCA.getValue();
    config.configured = validConfig(config.serverUrl, config.deviceToken);
    if (config.configured && requiresCA(config.serverUrl) && strlen(ca) == 0) config.configured = false;

    if (config.configured) {
		if (!saveConfig(ca)) { Serial.println("[ERROR] Configuration save failed"); ESP.restart(); }
    } else {
        Serial.println("[WARN] Device not fully configured!");
        Serial.println("[WARN] Please configure server URL and device token");
    }
}
