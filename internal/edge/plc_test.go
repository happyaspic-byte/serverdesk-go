package edge

import (
	"context"
	"encoding/binary"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// Only FINS reads run over loopback; CIP uses the worker's internal dependency boundary.
func plcLoopback(t *testing.T, reply func([]byte) []byte) DeviceConfig {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 12 {
				t.Error("short FINS request")
				continue
			}
			cmd := buf[10:n]
			switch {
			case len(cmd) == 2 && cmd[0] == 6 && cmd[1] == 1:
			case len(cmd) == 2 && cmd[0] == 5 && cmd[1] == 1:
			case len(cmd) == 3 && cmd[0] == 6 && cmd[1] == 2 && cmd[2] == 0:
			case len(cmd) == 2 && cmd[0] == 7 && cmd[1] == 1:
			case len(cmd) == 6 && cmd[0] == 0x21 && cmd[1] == 1 && cmd[2] == 0 && cmd[3] == 0 && cmd[4] == 0 && cmd[5] == 5:
			default:
				t.Errorf("non-read FINS command: %x", cmd)
			}
			resp := reply(cmd)
			if resp == nil {
				resp = make([]byte, 14)
				resp[12], resp[13] = 0x01, 0x01
			}
			_, _ = conn.WriteToUDP(resp, addr)
		}
	}()
	t.Cleanup(func() { _ = conn.Close(); <-done })
	return DeviceConfig{Key: "plc1", Kind: "plc", IP: "127.0.0.1", FinsPort: conn.LocalAddr().(*net.UDPAddr).Port}
}

func plcReadFixture(cmd []byte) []byte {
	switch cmd[0] {
	case 6:
		if cmd[1] == 1 {
			return finsResp([]byte{1, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
		}
		body := make([]byte, 12)
		binary.BigEndian.PutUint32(body, 10)
		binary.BigEndian.PutUint32(body[4:], 20)
		binary.BigEndian.PutUint32(body[8:], 30)
		return finsResp(body)
	case 5:
		body := make([]byte, 40)
		copy(body, "NX102")
		copy(body[20:], "1.0")
		return finsResp(body)
	case 0x21:
		return finsResp(make([]byte, 6))
	default:
		return nil
	}
}

func plcWorker() (*Worker, *pollCtx) {
	w, pc := testWorker(fakeSNMP{})
	w.cipIdentity = func(context.Context, string, time.Duration) *cipIdentity {
		return &cipIdentity{Name: "OMRON NX102", Serial: "AABBCCDD", Rev: "1.01", State: 3}
	}
	w.cipLink = func(context.Context, string) map[string]any {
		return map[string]any{"speedMbps": int64(100), "fullDuplex": true, "linkUp": true}
	}
	w.cipCounters = func(context.Context, string) *cipCounters { return nil }
	return w, pc
}

func TestPollPLCRefreshesDynamicCIP(t *testing.T) {
	dev := plcLoopback(t, plcReadFixture)
	w, pc := plcWorker()
	first, st := w.pollPLC(pc, dev, nil)
	if first["status"] != "op" {
		t.Fatalf("healthy = %v", first)
	}
	w.cipIdentity = func(context.Context, string, time.Duration) *cipIdentity {
		return &cipIdentity{Name: "OMRON NX102", Serial: "AABBCCDD", Rev: "1.01", State: 4, MajorFault: true}
	}
	w.cipLink = func(context.Context, string) map[string]any {
		return map[string]any{"speedMbps": int64(10), "fullDuplex": false, "linkUp": false}
	}
	pc.now += 60
	fault, st := w.pollPLC(pc, dev, st)
	meta := fault["meta"].(map[string]any)
	plc := meta["plc"].(map[string]any)
	if plc["cipMajorFault"] != true || plc["cipStatus"] != "Major recoverable fault" || plc["linkSpeedMbps"] != int64(10) || plc["linkFullDuplex"] != false || fault["status"] != "deg" {
		t.Fatalf("dynamic CIP frozen: %v", fault)
	}
	alerts := meta["alerts"].([]any)
	if len(alerts) < 2 || alerts[1].(map[string]any)["name"] != "PLC_CIP_FAULT" {
		t.Fatalf("missing CIP critical: %v", alerts)
	}
	activation := alerts[1].(map[string]any)["time"]
	w.cipIdentity = func(context.Context, string, time.Duration) *cipIdentity { return nil }
	w.cipLink = func(context.Context, string) map[string]any { return nil }
	pc.now += 60
	missing, st := w.pollPLC(pc, dev, st)
	mm := missing["meta"].(map[string]any)
	mp := mm["plc"].(map[string]any)
	if mm["stale"] != true || mp["cipMajorFault"] != nil || mp["linkSpeedMbps"] != nil || mp["serial"] != "AABBCCDD" || missing["status"] != "deg" {
		t.Fatalf("failure must expose unknown dynamic + cached identity: %v", missing)
	}
	if mm["alerts"].([]any)[1].(map[string]any)["time"] != activation {
		t.Fatal("lost fault activation")
	}
	w.cipIdentity = func(context.Context, string, time.Duration) *cipIdentity { return &cipIdentity{State: 3} }
	w.cipLink = func(context.Context, string) map[string]any {
		return map[string]any{"speedMbps": int64(100), "fullDuplex": true, "linkUp": true}
	}
	pc.now += 60
	recovered, _ := w.pollPLC(pc, dev, st)
	if recovered["status"] != "op" || recovered["meta"].(map[string]any)["stale"] != false {
		t.Fatalf("explicit recovery = %v", recovered)
	}
	if first["meta"].(map[string]any)["plc"].(map[string]any)["cipMajorFault"] != false {
		t.Fatal("prior snapshot mutated")
	}
}

func TestPollPLCDownRetainsCIPFault(t *testing.T) {
	var down atomic.Bool
	dev := plcLoopback(t, func(cmd []byte) []byte {
		if down.Load() && cmd[0] == 6 && cmd[1] == 1 {
			return nil
		}
		return plcReadFixture(cmd)
	})
	w, pc := plcWorker()
	w.cipIdentity = func(context.Context, string, time.Duration) *cipIdentity {
		return &cipIdentity{Serial: "AABBCCDD", State: 4, MajorFault: true}
	}
	_, st := w.pollPLC(pc, dev, nil)
	down.Store(true)
	pc.now += 60
	d, _ := w.pollPLC(pc, dev, st)
	meta := d["meta"].(map[string]any)
	if d["status"] != "down" || len(meta["alerts"].([]any)) != 2 || meta["plc"].(map[string]any)["serial"] != "AABBCCDD" {
		t.Fatalf("down lost fault/identity: %v", d)
	}
}

func TestPollPLCMissingModelRetainsIdentity(t *testing.T) {
	dev := plcLoopback(t, func(cmd []byte) []byte {
		if cmd[0] == 5 {
			return nil
		}
		return plcReadFixture(cmd)
	})
	w, pc := plcWorker()
	_, st := w.pollPLC(pc, dev, nil)
	w.cipIdentity = func(context.Context, string, time.Duration) *cipIdentity { return nil }
	pc.now += 60
	d, _ := w.pollPLC(pc, dev, st)
	if d["meta"].(map[string]any)["plc"].(map[string]any)["serial"] != "AABBCCDD" {
		t.Fatalf("model retry erased cached identity: %v", d)
	}
}

func TestPollPLCExplicitUnsupportedExtensions(t *testing.T) {
	var cycleReads, logReads atomic.Int32
	dev := plcLoopback(t, func(cmd []byte) []byte {
		if cmd[0] == 0x21 || (cmd[0] == 6 && cmd[1] == 2) {
			if cmd[0] == 0x21 {
				logReads.Add(1)
			} else {
				cycleReads.Add(1)
			}
			d := make([]byte, 14)
			d[12], d[13] = 4, 1
			return d
		}
		return plcReadFixture(cmd)
	})
	w, pc := plcWorker()
	_, st := w.pollPLC(pc, dev, nil)
	pc.now += 60
	d, _ := w.pollPLC(pc, dev, st)
	if d["status"] != "op" || cycleReads.Load() != 1 || logReads.Load() != 1 {
		t.Fatalf("explicit unsupported retried: cycle=%d log=%d status=%v", cycleReads.Load(), logReads.Load(), d["status"])
	}
}

func TestPollPLCFINSFaultActivationSurvivesReadFailure(t *testing.T) {
	var phase atomic.Int32
	dev := plcLoopback(t, func(cmd []byte) []byte {
		if cmd[0] == 6 && cmd[1] == 1 {
			if phase.Load() == 2 {
				return nil
			}
			resp := plcReadFixture(cmd)
			if phase.Load() < 3 {
				resp[16], resp[17] = 0x12, 0x34
			}
			return resp
		}
		return plcReadFixture(cmd)
	})
	w, pc := plcWorker()
	first, st := w.pollPLC(pc, dev, nil)
	activation := first["meta"].(map[string]any)["alerts"].([]any)[1].(map[string]any)["time"]
	for _, p := range []int32{1, 2} {
		phase.Store(p)
		pc.now += 60
		d, next := w.pollPLC(pc, dev, st)
		st = next
		alerts := d["meta"].(map[string]any)["alerts"].([]any)
		if len(alerts) != 2 || alerts[1].(map[string]any)["name"] != "PLC_ERROR" || alerts[1].(map[string]any)["time"] != activation {
			t.Fatalf("phase %d lost FINS fault activation: %v", p, alerts)
		}
	}
	phase.Store(3)
	pc.now += 60
	d, _ := w.pollPLC(pc, dev, st)
	if d["status"] != "op" || len(d["meta"].(map[string]any)["alerts"].([]any)) != 0 {
		t.Fatalf("explicit FINS recovery = %v", d)
	}
}

func TestPollPLCErrorLogEmptyThenFault(t *testing.T) {
	var round atomic.Int32
	dev := plcLoopback(t, func(cmd []byte) []byte {
		if cmd[0] == 0x21 && round.Load() > 0 {
			body := []byte{0, 5, 0, 1, 0, 1, 0x12, 0x34, 0x56, 0x78, 0x30, 0, 0x12, 0x10, 0x26, 0x10}
			return finsResp(body)
		}
		return plcReadFixture(cmd)
	})
	w, pc := plcWorker()
	_, st := w.pollPLC(pc, dev, nil)
	round.Store(1)
	pc.now += 60
	d, _ := w.pollPLC(pc, dev, st)
	log := d["meta"].(map[string]any)["plc"].(map[string]any)["errorLog"].([]any)
	if len(log) != 1 || log[0].(map[string]any)["code"] != "1234" {
		t.Fatalf("empty success disabled error log: %v", log)
	}
}

func TestPollPLCRetriesTransientExtensions(t *testing.T) {
	var round atomic.Int32
	dev := plcLoopback(t, func(cmd []byte) []byte {
		if round.Load() == 0 && (cmd[0] == 0x21 || (cmd[0] == 6 && cmd[1] == 2)) {
			return nil
		}
		if cmd[0] == 0x21 {
			return finsResp([]byte{0, 5, 0, 1, 0, 1, 0x12, 0x34, 0, 0, 0x30, 0, 0x12, 0x10, 0x26, 0x10})
		}
		return plcReadFixture(cmd)
	})
	w, pc := plcWorker()
	_, st := w.pollPLC(pc, dev, nil)
	round.Store(1)
	pc.now += 60
	d, _ := w.pollPLC(pc, dev, st)
	plc := d["meta"].(map[string]any)["plc"].(map[string]any)
	if plc["cycleAvgMs"] != float64(2) || len(plc["errorLog"].([]any)) != 1 {
		t.Fatalf("transient discovery frozen: %v", plc)
	}
}
