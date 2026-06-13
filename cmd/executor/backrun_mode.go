package main

import (
	"log/slog"
	"os"
	"strings"
	"sync"
)

// BackrunMode controls mempool-backrun submission behaviour.
type BackrunMode string

const (
	BackrunOff           BackrunMode = "off"
	BackrunShadowOnly    BackrunMode = "shadow_only"
	BackrunShadowAndLive BackrunMode = "shadow_and_live"
	BackrunLiveOnly      BackrunMode = "live_only"
)

var (
	backrunModeMu sync.RWMutex
	backrunMode   = BackrunShadowOnly
)

// initBackrunMode reads AETHER_BACKRUN_MODE (default shadow_only).
// Legacy AETHER_SHADOW=1 maps to shadow_only when BACKRUN_MODE is unset.
func initBackrunMode() {
	raw := strings.TrimSpace(os.Getenv("AETHER_BACKRUN_MODE"))
	if raw == "" {
		if isShadowMode() {
			raw = string(BackrunShadowOnly)
			slog.Info("AETHER_SHADOW=1 mapped to AETHER_BACKRUN_MODE=shadow_only")
		} else {
			raw = string(BackrunShadowOnly)
		}
	}
	mode := BackrunMode(strings.ToLower(raw))
	switch mode {
	case BackrunOff, BackrunShadowOnly, BackrunShadowAndLive, BackrunLiveOnly:
		setBackrunMode(mode)
	default:
		slog.Warn("unknown AETHER_BACKRUN_MODE, defaulting to shadow_only", "value", raw)
		setBackrunMode(BackrunShadowOnly)
	}
	slog.Info("mempool backrun mode", "mode", getBackrunMode())
}

func getBackrunMode() BackrunMode {
	backrunModeMu.RLock()
	defer backrunModeMu.RUnlock()
	return backrunMode
}

func setBackrunMode(mode BackrunMode) {
	backrunModeMu.Lock()
	defer backrunModeMu.Unlock()
	backrunMode = mode
}

// shouldProcessMempoolBackrun returns false when backrun mode is off.
func shouldProcessMempoolBackrun() bool {
	return getBackrunMode() != BackrunOff
}

// shouldShadowMempoolBackrun reports whether shadow logging applies.
func shouldShadowMempoolBackrun() bool {
	mode := getBackrunMode()
	return mode == BackrunShadowOnly || mode == BackrunShadowAndLive
}

// shouldSubmitMempoolBackrun reports whether live submission is allowed.
func shouldSubmitMempoolBackrun() bool {
	mode := getBackrunMode()
	return mode == BackrunShadowAndLive || mode == BackrunLiveOnly
}

// shouldShadowBlockDriven reports whether block-driven bundles are shadow-gated.
// Block-driven path still honours AETHER_SHADOW for backward compatibility.
func shouldShadowBlockDriven() bool {
	return isShadowMode()
}
