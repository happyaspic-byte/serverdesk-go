package web

import (
	"strings"
	"testing"
)

func TestProductionAssetsContainNoSimulationData(t *testing.T) {
	checks := map[string][]string{
		"js/model/data.js": {
			"buildFleet",
			"tickFleet",
			"SIM_SEED",
			"empty fleet",
			"한빛전자",
			"대원정밀",
		},
		"js/app.js": {
			"source: 'sim'",
			"simFallback",
		},
		"js/screens/manage.js": {
			"simTest",
		},
		"js/screens/detail.js": {
			"simulateAction",
			"Simulated:",
		},
		"index.html": {
			"황태원",
			"data-source=\"sim\"",
			">SIM</span>",
		},
	}

	for name, forbidden := range checks {
		data, err := FS.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(data)
		for _, value := range forbidden {
			if strings.Contains(text, value) {
				t.Errorf("%s still contains simulation marker %q", name, value)
			}
		}
	}
}

func TestDOMHelperHasNoRawHTMLAttributePath(t *testing.T) {
	data, err := FS.ReadFile("js/util/dom.js")
	if err != nil {
		t.Fatalf("read DOM helper: %v", err)
	}
	text := string(data)
	for _, forbidden := range []string{"k === 'html'", "node.innerHTML", "html → innerHTML"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("DOM helper reintroduced raw HTML attribute path %q", forbidden)
		}
	}
}

func TestCommercialMonitoringUIContracts(t *testing.T) {
	checks := map[string][]string{
		"js/app.js": {
			"collectionState(st)", "requestOperatorConfirmation", "formatConsoleTime(st.lastPoll)",
			"refreshSharedState", "createSharedSyncCoordinator", "startSharedRefresh", "syncError",
		},
		"js/screens/settings.js": {
			"/api/admin/notifications", "notificationDisplay", "sanitizeNotificationConfig", "rollbackConfig",
			"typedPhrase: 'RESTORE'",
		},
		"js/screens/overview.js": {
			"const initialLoading = !!state.pollPending", "N.attEmptyAdd", "C.confirmAction",
		},
		"js/screens/manage.js": {
			"Planned · not supported", "validateDeleteConfirmation", "deleteImpact",
			"'aria-controls': 'mng-tabpanel'",
		},
		"css/styles.css": {
			".confirm-dialog", ".hd-banner-meta", ".rail-live.is-stale", ".rail-live.is-offline",
			"@media (min-width:1025px) and (max-width:1279px)", "@media (forced-colors: active)",
		},
	}
	for name, required := range checks {
		data, err := FS.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, value := range required {
			if !strings.Contains(string(data), value) {
				t.Errorf("%s missing commercial UI contract %q", name, value)
			}
		}
	}

	for _, name := range []string{"js/screens/nodes.js", "js/screens/clusters.js"} {
		data, err := FS.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(data)
		if strings.Contains(text, "'data-nd-row': true, 'data-id': r.id, role: 'button'") ||
			strings.Contains(text, "'data-cl-row': row.id, role: 'button'") {
			t.Errorf("%s reintroduced role=button on a table row", name)
		}
	}
}

func TestInitialCollectionBadgeFailsClosed(t *testing.T) {
	data, err := FS.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	text := string(data)
	for _, required := range []string{`data-source="connecting"`, `>CONNECTING</span>`, `u-dot is-warn`} {
		if !strings.Contains(text, required) {
			t.Errorf("index.html missing fail-closed initial collection marker %q", required)
		}
	}
	for _, forbidden := range []string{`data-source="live"`, `rail-live is-live`} {
		if strings.Contains(text, forbidden) {
			t.Errorf("index.html advertises collection success before JavaScript poll: %q", forbidden)
		}
	}
}
