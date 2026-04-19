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
#define HEARTBEAT_INTERVAL 60000  // 60 seconds in milliseconds
#define MAX_RETRY_DELAY 300000    // 5 minutes max backoff
#define INITIAL_RETRY_DELAY 5000  // 5 seconds initial retry

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

#define LED_PIN LED_BUILTIN

// BOOT/FLASH button is used for factory reset of saved configuration.
// Override RESET_BUTTON_PIN via build flags if your board wiring differs.
#ifndef RESET_BUTTON_PIN
    #define RESET_BUTTON_PIN 0
#endif
#define RESET_BUTTON_ACTIVE LOW
#define RESET_HOLD_TIME_MS 5000

// Configuration structure
struct Config {
    char serverUrl[128];
    char deviceToken[65];  // 64 hex chars + null terminator
    bool configured;
};

Config config;
unsigned long lastHeartbeat = 0;
unsigned long retryDelay = INITIAL_RETRY_DELAY;
bool bootEventSent = false;

// Function declarations
void loadConfig();
void saveConfig();
bool sendEvent(const char* eventType);
void blinkLed(int times, int delayMs);
void setupWiFiManager();
void initFilesystem();
bool isFactoryResetRequested();
void clearSavedConfiguration();

void setup() {
    Serial.begin(115200);
    delay(1000);

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
    initFilesystem();

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
        WiFi.reconnect();
        delay(5000);
        return;
    }

    // Send boot event (only once after power restoration)
    if (!bootEventSent && config.configured) {
        Serial.println("[INFO] Sending boot event...");
        if (sendEvent("boot")) {
            bootEventSent = true;
            retryDelay = INITIAL_RETRY_DELAY;
            Serial.println("[OK] Boot event sent successfully");
            blinkLed(2, 100);
        } else {
            Serial.println("[ERROR] Failed to send boot event, will retry...");
            delay(retryDelay);
            // Exponential backoff
            retryDelay = min(retryDelay * 2, (unsigned long)MAX_RETRY_DELAY);
        }
        return;
    }

    // Send heartbeat every HEARTBEAT_INTERVAL
    unsigned long currentMillis = millis();
    if (config.configured && (currentMillis - lastHeartbeat >= HEARTBEAT_INTERVAL)) {
        Serial.println("[INFO] Sending heartbeat...");
        if (sendEvent("heartbeat")) {
            lastHeartbeat = currentMillis;
            retryDelay = INITIAL_RETRY_DELAY;
            Serial.println("[OK] Heartbeat sent");
            blinkLed(1, 50);
        } else {
            Serial.println("[ERROR] Failed to send heartbeat");
            // Don't update lastHeartbeat, try again next loop
        }
    }

    delay(1000); // Small delay to prevent tight loop
}

void initFilesystem() {
    Serial.println("[INFO] Initializing filesystem...");

    #if defined(ESP32)
    if (!LittleFS.begin(true)) {  // true = format if mount fails
        Serial.println("[ERROR] Failed to mount LittleFS");
        return;
    }
    #elif defined(ESP8266)
    if (!LittleFS.begin()) {
        Serial.println("[ERROR] Failed to mount LittleFS");
        Serial.println("[INFO] Formatting LittleFS...");
        LittleFS.format();
        LittleFS.begin();
    }
    #endif

    Serial.println("[OK] Filesystem mounted");
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
                config.configured = strlen(config.serverUrl) > 0 && strlen(config.deviceToken) > 0;

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

void saveConfig() {
    Serial.println("[INFO] Saving configuration...");

    File file = FILESYSTEM.open(CONFIG_FILE, "w");
    if (file) {
        JsonDocument doc;
        doc["serverUrl"] = config.serverUrl;
        doc["deviceToken"] = config.deviceToken;

        if (serializeJson(doc, file)) {
            Serial.println("[OK] Configuration saved");
        } else {
            Serial.println("[ERROR] Failed to write config");
        }
        file.close();
    } else {
        Serial.println("[ERROR] Failed to open config file for writing");
    }
}

bool sendEvent(const char* eventType) {
    if (!config.configured) {
        Serial.println("[WARN] Device not configured");
        return false;
    }

    HTTPClient http;

    String url = String(config.serverUrl);
    if (!url.endsWith("/")) {
        url += "/";
    }
    url += "api/events";

    Serial.print("[DEBUG] Sending to: ");
    Serial.println(url);

    bool isHttps = url.startsWith("https://");

    // Use appropriate client based on URL scheme
    WiFiClient *client;
    WiFiClientSecure *secureClient = nullptr;
    WiFiClient *plainClient = nullptr;

    if (isHttps) {
        secureClient = new WiFiClientSecure();
        secureClient->setInsecure();  // Skip certificate verification (for self-signed certs)
        client = secureClient;
        Serial.println("[DEBUG] Using HTTPS (insecure mode)");
    } else {
        plainClient = new WiFiClient();
        client = plainClient;
        Serial.println("[DEBUG] Using HTTP");
    }

    #if defined(ESP32)
    http.begin(url);
    #elif defined(ESP8266)
    http.begin(*client, url);
    #endif

    http.addHeader("Content-Type", "application/json");
    http.addHeader("Authorization", String("Bearer ") + config.deviceToken);
    http.setTimeout(10000); // 10 second timeout

    JsonDocument doc;
    doc["event_type"] = eventType;
    doc["timestamp"] = millis(); // Device uptime as reference

    String payload;
    serializeJson(doc, payload);

    Serial.print("[DEBUG] Payload: ");
    Serial.println(payload);

    int httpCode = http.POST(payload);
    String response = http.getString();
    http.end();

    // Clean up allocated clients
    if (secureClient) delete secureClient;
    if (plainClient) delete plainClient;

    Serial.print("[DEBUG] HTTP Response Code: ");
    Serial.println(httpCode);
    Serial.print("[DEBUG] Response: ");
    Serial.println(response);

    return httpCode == 200 || httpCode == 201;
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

    // Custom parameters for server URL and device token
    WiFiManagerParameter customServerUrl("server", "Server URL", config.serverUrl, 128);
    WiFiManagerParameter customDeviceToken("token", "Device Token", config.deviceToken, 65);

    wm.addParameter(&customServerUrl);
    wm.addParameter(&customDeviceToken);

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
    wm.setSaveParamsCallback([&customServerUrl, &customDeviceToken]() {
        Serial.println("[INFO] WiFiManager params saved callback");
        strlcpy(config.serverUrl, customServerUrl.getValue(), sizeof(config.serverUrl));
        strlcpy(config.deviceToken, customDeviceToken.getValue(), sizeof(config.deviceToken));
        config.configured = strlen(config.serverUrl) > 0 && strlen(config.deviceToken) > 0;
        saveConfig();
    });

    // Start open setup AP (no password) for first-time provisioning.
    bool connected = wm.autoConnect("KhambaMobarak-Setup");

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

    config.configured = strlen(config.serverUrl) > 0 && strlen(config.deviceToken) > 0;

    if (config.configured) {
        saveConfig();
    } else {
        Serial.println("[WARN] Device not fully configured!");
        Serial.println("[WARN] Please configure server URL and device token");
    }
}
