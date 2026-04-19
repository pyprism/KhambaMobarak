package models

import (
	"regexp"
	"testing"
	"time"
)

func TestGenerateToken(t *testing.T) {
	tokenA, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken returned error: %v", err)
	}
	tokenB, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken returned error: %v", err)
	}

	if len(tokenA) != 64 {
		t.Fatalf("expected token length 64, got %d", len(tokenA))
	}
	if len(tokenB) != 64 {
		t.Fatalf("expected token length 64, got %d", len(tokenB))
	}

	hexPattern := regexp.MustCompile(`^[0-9a-f]+$`)
	if !hexPattern.MatchString(tokenA) || !hexPattern.MatchString(tokenB) {
		t.Fatalf("expected hex encoded tokens, got %q and %q", tokenA, tokenB)
	}

	if tokenA == tokenB {
		t.Fatalf("expected unique tokens, got duplicate %q", tokenA)
	}
}

func TestDeviceIsDeviceOnline(t *testing.T) {
	deviceWithoutHeartbeat := &Device{}
	if deviceWithoutHeartbeat.IsDeviceOnline() {
		t.Fatalf("device with nil LastSeen must be offline")
	}

	recent := time.Now().Add(-30 * time.Second)
	deviceOnline := &Device{LastSeen: &recent}
	if !deviceOnline.IsDeviceOnline() {
		t.Fatalf("device with recent LastSeen must be online")
	}

	stale := time.Now().Add(-3 * time.Minute)
	deviceOffline := &Device{LastSeen: &stale}
	if deviceOffline.IsDeviceOnline() {
		t.Fatalf("device with stale LastSeen must be offline")
	}
}

func TestAfterFindSetsComputedOnlineFlag(t *testing.T) {
	recent := time.Now().Add(-45 * time.Second)
	d := &Device{LastSeen: &recent}

	if err := d.AfterFind(nil); err != nil {
		t.Fatalf("AfterFind returned error: %v", err)
	}
	if !d.IsOnline {
		t.Fatalf("expected IsOnline to be true after AfterFind")
	}
}
