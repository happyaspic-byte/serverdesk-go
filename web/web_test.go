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
