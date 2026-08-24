package edge

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type integrationRoundTripper func(*http.Request) (*http.Response, error)

func (f integrationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func integrationClient(fn integrationRoundTripper) *http.Client {
	return &http.Client{Transport: fn, Timeout: time.Second}
}

func integrationResponse(status int, body []byte, headers map[string]string) *http.Response {
	h := make(http.Header)
	for key, value := range headers {
		h.Set(key, value)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func integrationJSONResponse(t *testing.T, status int, data any) *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatal(err)
	}
	return integrationResponse(status, body, map[string]string{"Content-Type": "application/json"})
}

func pveSuccessTransport(t *testing.T, calls map[string]int) *http.Client {
	t.Helper()
	return integrationClient(func(req *http.Request) (*http.Response, error) {
		path := strings.TrimPrefix(req.URL.Path, "/api2/json")
		calls[path]++
		if path != "/access/ticket" && !strings.Contains(req.Header.Get("Cookie"), "PVEAuthCookie=PVE:test-ticket") {
			t.Errorf("PVE request %s missing ticket cookie: %q", path, req.Header.Get("Cookie"))
		}
		switch path {
		case "/access/ticket":
			if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Errorf("ticket request = %s %q", req.Method, req.Header.Get("Content-Type"))
			}
			body, _ := io.ReadAll(req.Body)
			values, _ := url.ParseQuery(string(body))
			if values.Get("username") != "root@pam" || values.Get("password") != "pve-secret" {
				t.Errorf("ticket form = %v", values)
			}
			return integrationJSONResponse(t, http.StatusOK, map[string]any{"ticket": "PVE:test-ticket"}), nil
		case "/cluster/status":
			return integrationJSONResponse(t, http.StatusOK, []any{
				map[string]any{"type": "node", "local": 1, "name": "pve-a"},
			}), nil
		case "/version":
			return integrationJSONResponse(t, http.StatusOK, map[string]any{"version": "8.3.1"}), nil
		case "/nodes/pve-a/network":
			return integrationJSONResponse(t, http.StatusOK, []any{
				map[string]any{"type": "eth", "iface": "eno1", "active": true, "address": "192.0.2.10"},
			}), nil
		case "/nodes/pve-a/disks/list":
			return integrationJSONResponse(t, http.StatusOK, []any{
				map[string]any{"devpath": "/dev/sda", "model": "SSD", "size": 100e9, "type": "ssd", "health": "PASSED", "wearout": 90},
			}), nil
		case "/nodes/pve-a/storage":
			return integrationJSONResponse(t, http.StatusOK, []any{
				map[string]any{"storage": "local", "type": "dir", "active": true, "total": 1000, "used": 100},
			}), nil
		case "/nodes/pve-a/status":
			return integrationJSONResponse(t, http.StatusOK, map[string]any{
				"cpu": 0.25, "uptime": 172800,
				"memory":   map[string]any{"total": 8 * 1073741824.0, "used": 2 * 1073741824.0},
				"loadavg":  []any{"0.10", "0.20", "0.30"},
				"cpuinfo":  map[string]any{"model": "Intel(R) Test CPU", "sockets": 1, "cores": 4, "cpus": 8},
				"kversion": "Linux 6.8.0-pve", "swap": map[string]any{"total": 100, "used": 25},
				"rootfs": map[string]any{"total": 100, "used": 50}, "boot-info": map[string]any{"mode": "efi"},
			}), nil
		case "/nodes/pve-a/qemu":
			return integrationJSONResponse(t, http.StatusOK, []any{
				map[string]any{"vmid": 100, "name": "vm-a", "status": "running", "maxmem": 1024, "mem": 512},
			}), nil
		case "/nodes/pve-a/lxc":
			return integrationJSONResponse(t, http.StatusOK, []any{
				map[string]any{"vmid": 200, "name": "ct-a", "status": "stopped", "maxmem": 1024},
			}), nil
		default:
			t.Fatalf("unexpected PVE request: %s", path)
			return nil, nil
		}
	})
}

func TestPVEFetchPollAndTicketCaching(t *testing.T) {
	calls := map[string]int{}
	client := pveSuccessTransport(t, calls)
	w := NewWorker(nil)
	pc := &pollCtx{ctx: context.Background(), now: 1_700_000_000, refresh: true, pve: client}
	dev := DeviceConfig{Key: "pve-1", Kind: "proxmox", IP: "192.0.2.10", Password: "pve-secret"}

	d, st := w.pollProxmox(pc, dev, nil)
	if d["status"] != "op" || st.Ticket != "PVE:test-ticket" || st.Node != "pve-a" || st.Version != "8.3.1" {
		t.Fatalf("successful PVE poll = status %v static %+v", d["status"], st)
	}
	if !st.HasNet || !st.HasDisks || !st.HasStorage || len(st.Net) != 1 || len(st.Disks) != 1 || len(st.Storage) != 1 {
		t.Fatalf("PVE inventory cache = %+v", st)
	}
	if calls["/access/ticket"] != 1 || calls["/cluster/status"] != 1 {
		t.Fatalf("initial PVE calls = %v", calls)
	}

	pc.now += 60
	pc.refresh = false
	if _, st = w.pollProxmox(pc, dev, st); st.Ticket == "" {
		t.Fatal("valid PVE ticket was discarded")
	}
	if calls["/access/ticket"] != 1 || calls["/cluster/status"] != 1 || calls["/version"] != 1 {
		t.Fatalf("cached PVE round repeated authentication/static calls: %v", calls)
	}

	worker := NewWorker([]DeviceConfig{dev})
	worker.pve = pveSuccessTransport(t, map[string]int{})
	worker.SNMPGet = fakeSNMP{}.get
	worker.pollRound(context.Background())
	if got := worker.LatestDevices(); len(got) != 1 || got[0]["status"] != "op" {
		t.Fatalf("PVE worker dispatch = %+v", got)
	}
}

func TestPVEFailureAndOptionalInventoryBranches(t *testing.T) {
	dev := DeviceConfig{Key: "pve-1", Kind: "proxmox", IP: "192.0.2.10", Password: "bad"}
	w := NewWorker(nil)
	pc := &pollCtx{ctx: context.Background(), now: 1_700_000_000, refresh: true}

	pc.pve = integrationClient(func(*http.Request) (*http.Response, error) {
		return integrationJSONResponse(t, http.StatusUnauthorized, map[string]any{"error": "denied"}), nil
	})
	d, st := w.pollProxmox(pc, dev, &pveStatic{Ticket: "stale", TicketTS: 1})
	if d["status"] != "down" || st.Ticket != "" {
		t.Fatalf("PVE auth failure = status %v ticket %q", d["status"], st.Ticket)
	}
	meta := d["meta"].(map[string]any)
	if meta["healthLevel"] != "critical" || !authFailed(&httpStatusError{Code: http.StatusForbidden}) || authFailed(errors.New("no")) {
		t.Fatalf("PVE auth classification = %+v", meta)
	}

	pc.pve = integrationClient(func(req *http.Request) (*http.Response, error) {
		path := strings.TrimPrefix(req.URL.Path, "/api2/json")
		if path == "/access/ticket" {
			return integrationJSONResponse(t, http.StatusOK, map[string]any{}), nil
		}
		t.Fatalf("unexpected request after missing ticket: %s", path)
		return nil, nil
	})
	if _, err := w.pveFetch(pc, dev, &pveStatic{}); err == nil || errClass(err) != "ValueError" {
		t.Fatalf("missing PVE ticket error = %T %v", err, err)
	}

	pc.pve = integrationClient(func(req *http.Request) (*http.Response, error) {
		path := strings.TrimPrefix(req.URL.Path, "/api2/json")
		switch path {
		case "/access/ticket":
			return integrationJSONResponse(t, http.StatusOK, map[string]any{"ticket": "PVE:t"}), nil
		case "/cluster/status":
			return integrationJSONResponse(t, http.StatusOK, []any{}), nil // localhost fallback
		case "/version":
			return integrationJSONResponse(t, http.StatusOK, map[string]any{"version": "8"}), nil
		case "/nodes/localhost/network", "/nodes/localhost/disks/list", "/nodes/localhost/storage", "/nodes/localhost/lxc":
			return integrationJSONResponse(t, http.StatusInternalServerError, nil), nil
		case "/nodes/localhost/status":
			return integrationJSONResponse(t, http.StatusOK, nil), nil
		case "/nodes/localhost/qemu":
			return integrationJSONResponse(t, http.StatusOK, []any{}), nil
		default:
			t.Fatalf("unexpected optional-inventory request: %s", path)
			return nil, nil
		}
	})
	st = &pveStatic{}
	raw, err := w.pveFetch(pc, dev, st)
	if err != nil || st.Node != "localhost" || !st.HasNet || !st.HasDisks || !st.HasStorage || raw.NodeStatus == nil || raw.Lxc != nil {
		t.Fatalf("optional PVE failures = raw %+v static %+v err %v", raw, st, err)
	}

	badJSON := integrationClient(func(*http.Request) (*http.Response, error) {
		return integrationResponse(http.StatusOK, []byte("{"), nil), nil
	})
	if _, err := pveAPI(context.Background(), badJSON, "pve.local", "/version", nil, ""); err == nil || errClass(err) != "JSONDecodeError" {
		t.Fatalf("PVE invalid JSON error = %T %v", err, err)
	}
}

func redfishSuccessClient(t *testing.T) *http.Client {
	t.Helper()
	return integrationClient(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Accept") != "application/json" {
			t.Errorf("Redfish Accept = %q", req.Header.Get("Accept"))
		}
		if user, password, ok := req.BasicAuth(); !ok || user != "admin" || password != "secret" {
			t.Errorf("Redfish basic auth = %q %q %v", user, password, ok)
		}
		var body any
		switch req.URL.Path {
		case "/redfish/v1/Systems":
			body = map[string]any{"Members": []any{map[string]any{"@odata.id": "/redfish/v1/Systems/1"}}}
		case "/redfish/v1/Systems/1":
			body = map[string]any{
				"PowerState": "On", "Status": map[string]any{"Health": "OK"},
				"Manufacturer": "HPE", "Model": "DL360", "SerialNumber": "SER-1", "BiosVersion": "2.0", "HostName": "srv-a",
				"ProcessorSummary": map[string]any{"Model": "Xeon", "Count": 2},
				"MemorySummary":    map[string]any{"TotalSystemMemoryGiB": 64, "Status": map[string]any{"Health": "OK"}},
			}
		case "/redfish/v1/Chassis":
			body = map[string]any{"Members": []any{map[string]any{"@odata.id": "/redfish/v1/Chassis/1/"}}}
		case "/redfish/v1/Chassis/1/Thermal":
			body = map[string]any{
				"Temperatures": []any{map[string]any{"Name": "Inlet", "ReadingCelsius": 24.5, "Status": map[string]any{"Health": "OK"}}},
				"Fans":         []any{map[string]any{"Name": "Fan 1", "Reading": 4200, "Status": map[string]any{"Health": "OK"}}},
			}
		default:
			t.Fatalf("unexpected Redfish request: %s", req.URL.Path)
		}
		encoded, _ := json.Marshal(body)
		return integrationResponse(http.StatusOK, encoded, map[string]string{"Content-Type": "application/json"}), nil
	})
}

func TestRedfishFetchThermalAndServerPoll(t *testing.T) {
	client := redfishSuccessClient(t)
	system, err := fetchRedfishSystem(context.Background(), client, "bmc.local", "admin", "secret")
	if err != nil || system == nil || system.Model != "DL360" || system.Power != "On" {
		t.Fatalf("Redfish system = %+v, %v", system, err)
	}
	thermal, err := fetchRedfishThermal(context.Background(), client, "bmc.local", "admin", "secret")
	if err != nil || thermal == nil || len(thermal.Temps) != 1 || len(thermal.Fans) != 1 {
		t.Fatalf("Redfish thermal = %+v, %v", thermal, err)
	}

	w, pc := testWorker(fakeSNMP{})
	pc.rf = redfishSuccessClient(t)
	pc.refresh = true
	dev := DeviceConfig{Key: "srv-1", Kind: "server", IP: "192.0.2.20", BMCIP: "bmc.local", BMCUser: "admin", BMCPassword: "secret"}
	d, st := w.pollServer(pc, dev, nil)
	if d["status"] != "op" || st == nil || !st.ThermalTried || st.Thermal == nil {
		t.Fatalf("BMC-only server poll = status %v static %+v", d["status"], st)
	}
	meta := d["meta"].(map[string]any)
	if meta["vendor"] != "HPE" || meta["healthLevel"] != "ok" {
		t.Fatalf("BMC-only server metadata = %+v", meta)
	}

	dev.Community = "public"
	d, _ = w.pollServer(pc, dev, st)
	if d["status"] != "deg" {
		t.Fatalf("BMC up with SNMP down status = %v", d["status"])
	}
}

func TestRedfishProtocolFailureBranches(t *testing.T) {
	empty := integrationClient(func(*http.Request) (*http.Response, error) {
		body, _ := json.Marshal(map[string]any{"Members": []any{}})
		return integrationResponse(http.StatusOK, body, nil), nil
	})
	if got, err := fetchRedfishSystem(context.Background(), empty, "bmc", "", ""); err != nil || got != nil {
		t.Fatalf("empty Redfish systems = %+v, %v", got, err)
	}
	if got, err := fetchRedfishThermal(context.Background(), empty, "bmc", "", ""); err != nil || got != nil {
		t.Fatalf("empty Redfish chassis = %+v, %v", got, err)
	}

	unauthorized := integrationClient(func(*http.Request) (*http.Response, error) {
		return integrationResponse(http.StatusUnauthorized, nil, nil), nil
	})
	if _, err := redfishGet(context.Background(), unauthorized, "bmc", "u", "p", "/redfish/v1/Systems"); err == nil || !authFailed(err) || errClass(err) != "HTTPError" {
		t.Fatalf("Redfish HTTP error = %T %v", err, err)
	}
	malformed := integrationClient(func(*http.Request) (*http.Response, error) {
		return integrationResponse(http.StatusOK, []byte("{"), nil), nil
	})
	if _, err := redfishGet(context.Background(), malformed, "bmc", "", "", "/x"); err == nil || errClass(err) != "JSONDecodeError" {
		t.Fatalf("Redfish JSON error = %T %v", err, err)
	}
	network := integrationClient(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial blocked")
	})
	if _, err := redfishGet(context.Background(), network, "bmc", "", "", "/x"); err == nil || errClass(err) != "URLError" {
		t.Fatalf("Redfish network error = %T %v", err, err)
	}
}

func gzipBytes(t *testing.T, raw string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSyncThruGzipFetchAndFailureBranches(t *testing.T) {
	home := `{identity:{model_name:"SL-C565W",host_name:"prn-a",},status:{status1:"Ready..."},toner_black:{cnt:55,},}`
	counters := `{"GXI_BILLING_SIMPLEX_BW_TOTAL_CNT":10,"GXI_BILLING_DUPLEX_BW_TOTAL_CNT":2}`
	client := integrationClient(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Accept-Encoding") != "gzip" {
			t.Errorf("SyncThru Accept-Encoding = %q", req.Header.Get("Accept-Encoding"))
		}
		switch req.URL.Path {
		case "/sws/app/information/home/home.json":
			return integrationResponse(http.StatusOK, gzipBytes(t, home), nil), nil
		case "/sws/app/information/counters/counters.json":
			return integrationResponse(http.StatusOK, []byte(counters), nil), nil
		default:
			t.Fatalf("unexpected SyncThru path: %s", req.URL.Path)
			return nil, nil
		}
	})
	got := fetchSyncThru(context.Background(), client, "printer.local")
	if got == nil || got.WebModel != "SL-C565W" || got.HostName != "prn-a" || got.MonoTotal != 12 || got.TonerCnt["black"] != 55 {
		t.Fatalf("SyncThru fetch = %+v", got)
	}

	for name, response := range map[string]*http.Response{
		"http status": integrationResponse(http.StatusForbidden, nil, nil),
		"bad json":    integrationResponse(http.StatusOK, []byte("{"), nil),
		"array root":  integrationResponse(http.StatusOK, []byte(`[]`), nil),
		"bad gzip":    integrationResponse(http.StatusOK, []byte{0x1f, 0x8b, 0x00}, nil),
	} {
		t.Run(name, func(t *testing.T) {
			cl := integrationClient(func(*http.Request) (*http.Response, error) { return response, nil })
			if _, err := swsGet(context.Background(), cl, "printer.local", "/x"); err == nil {
				t.Fatal("invalid SyncThru response accepted")
			}
		})
	}
	if got := fetchSyncThru(context.Background(), blockedClient(), "printer.local"); got != nil {
		t.Fatalf("failed SyncThru fetch = %+v", got)
	}
}

func TestWorkerStartConfigErrorsAndUtilityBranches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := NewWorker(nil)
	w.Start(ctx)
	if at, errText := w.CollectionStatus(); at.IsZero() || errText != "" {
		t.Fatalf("canceled worker first round = %v %q", at, errText)
	}

	inner := errors.New("decode failed")
	ce := &ConfigError{Index: 3, Err: inner}
	if !strings.Contains(ce.Error(), "edge_devices[3]") || !errors.Is(ce, inner) {
		t.Fatalf("ConfigError contract = %q / %v", ce.Error(), ce.Unwrap())
	}
	for kind, want := range map[string]string{"printer": "PRN", "nas": "NAS", "plc": "PLC", "proxmox": "SRV", "server": "SRV", "unknown": "SRV"} {
		if got := dtypeForKind(kind); got != want {
			t.Errorf("dtypeForKind(%q) = %q, want %q", kind, got, want)
		}
	}
	if siteOrIP(DeviceConfig{Site: "Seoul", IP: "192.0.2.1"}) != "Seoul" || siteOrIP(DeviceConfig{IP: "192.0.2.1"}) != "192.0.2.1" || siteOrIP(DeviceConfig{Name: "no-ip"}) != "" {
		t.Fatal("site/IP fallback contract changed")
	}
	if itoa(42) != "42" {
		t.Fatal("itoa conversion failed")
	}
	for input, want := range map[any]bool{
		true: true, false: false, float64(1): true, float64(0): false,
		int(1): true, int64(0): false, "yes": true, "": false, nil: false,
	} {
		if got := jtruthy(input); got != want {
			t.Errorf("jtruthy(%#v) = %v, want %v", input, got, want)
		}
	}
	if jtruthy([]any{}) || jtruthy(map[string]any{}) || !jtruthy([]any{1}) || !jtruthy(map[string]any{"x": 1}) {
		t.Fatal("jtruthy container semantics changed")
	}
	for _, input := range []any{float64(1.25), int(2), int64(3), "6.5"} {
		if _, ok := jf(input); !ok {
			t.Errorf("jf rejected %#v", input)
		}
	}
	if _, ok := jf(struct{}{}); ok {
		t.Fatal("jf accepted struct")
	}
	if pyFloat(6) != "6.0" || pyFloat(6.5) != "6.5" {
		t.Fatal("pyFloat formatting contract changed")
	}
}
