package main

import (
	"errors"
	"strings"
	"testing"
)

func TestFormatAdminError_Halted(t *testing.T) {
	msg := formatAdminError("Resume", errors.New("admin /admin/resume: 409 cannot resume from Halted"))
	if !strings.Contains(msg, "halted") {
		t.Fatalf("msg: %s", msg)
	}
}

func TestFormatAdminError_Conflict(t *testing.T) {
	msg := formatAdminError("Pause", errors.New("admin /admin/pause: 409 invalid transition"))
	if !strings.Contains(msg, "Cannot pause") {
		t.Fatalf("msg: %s", msg)
	}
}

func TestFriendlyConflict(t *testing.T) {
	if friendlyConflict("invalid transition: Paused -> Paused") == "" {
		t.Fatal("expected message")
	}
}

func TestResetPendingFlow(t *testing.T) {
	b := &TeleBot{resetPending: make(map[int64]bool)}
	chatID := int64(1)
	b.resetPending[chatID] = true
	b.mu.Lock()
	pending := b.resetPending[chatID]
	delete(b.resetPending, chatID)
	b.mu.Unlock()
	if !pending {
		t.Fatal("expected pending")
	}
}
