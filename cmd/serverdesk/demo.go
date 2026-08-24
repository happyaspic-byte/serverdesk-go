package main

import (
	"fmt"
	"strings"

	"serverdesk/internal/config"
)

// validateDemoMode fails closed before any collector, trap receiver, notifier,
// or HTTP server can start. Sample mode is intentionally loopback-only and may
// not coexist with a live inventory.
func validateDemoMode(enabled, once bool, cfg *config.Config, transport listenerTransport) error {
	if !enabled {
		return nil
	}
	if once {
		return fmt.Errorf("-demo와 -once는 함께 사용할 수 없습니다")
	}
	if cfg == nil {
		return fmt.Errorf("데모 모드는 유효한 설정이 필요합니다")
	}
	if !transport.loopback() {
		return fmt.Errorf("데모 모드는 루프백 리스너에서만 사용할 수 있습니다: listen=%q", transport.addr)
	}
	if len(cfg.Clusters) != 0 {
		return fmt.Errorf("데모 모드는 실제 clusters 설정과 함께 사용할 수 없습니다 (got %d)", len(cfg.Clusters))
	}
	if len(cfg.EdgeDevices) != 0 {
		return fmt.Errorf("데모 모드는 실제 edge_devices 설정과 함께 사용할 수 없습니다 (got %d)", len(cfg.EdgeDevices))
	}
	if cfg.Notifications.Enabled {
		return fmt.Errorf("데모 모드에서는 notifications.enabled를 false로 설정해야 합니다")
	}
	if cfg.Trap.Enabled {
		return fmt.Errorf("데모 모드에서는 trap.enabled를 false로 설정해야 합니다")
	}
	return nil
}

// demoRuntimeDir keeps demo UI state and audit files away from production
// state even when an operator alternates modes with the same config file.
func demoRuntimeDir(runtimeDir string) string {
	runtimeDir = strings.TrimSpace(runtimeDir)
	if runtimeDir == "" {
		return "data-demo"
	}
	return runtimeDir + "-demo"
}
