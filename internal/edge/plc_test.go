package edge

import (
	"encoding/binary"
	"encoding/hex"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

// finsReadOnlyCmds — pollPLC 가 보낼 수 있는 FINS 명령(읽기 전용 계약).
var finsReadOnlyCmds = map[string]bool{
	"0501":         true, // CPU UNIT DATA READ
	"0601":         true, // CONTROLLER STATUS READ
	"060200":       true, // CYCLE TIME READ (param 0x00 = read)
	"0701":         true, // CLOCK READ
	"210100000005": true, // ERROR LOG READ
}

// fakePLC — 루프백 FINS/UDP 응답기. 받은 명령을 기록하고 상태를 흉내 낸다.
type fakePLC struct {
	conn *net.UDPConn

	mu          sync.Mutex
	cmds        []string
	status      []byte // 0601 응답 body
	unsupported bool   // NX 처럼 0602/2101 에 0401(미지원) 응답
	clockEpoch  int64  // PLC 로컬(KST) 시각을 naive UTC 로 본 epoch
}

func newFakePLC(t *testing.T) *fakePLC {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakePLC{conn: conn, status: plcStatusBody(0x01, 0x04, 0, 0, "")}
	t.Cleanup(func() { _ = conn.Close() })
	go f.serve()
	return f
}

func (f *fakePLC) port() int { return f.conn.LocalAddr().(*net.UDPAddr).Port }

func (f *fakePLC) serve() {
	buf := make([]byte, 2048)
	for {
		n, addr, err := f.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n < 12 {
			continue
		}
		cmd := hex.EncodeToString(buf[10:n])
		f.mu.Lock()
		f.cmds = append(f.cmds, cmd)
		resp := f.respond(cmd)
		f.mu.Unlock()
		if resp != nil {
			_, _ = f.conn.WriteToUDP(resp, addr)
		}
	}
}

func (f *fakePLC) respond(cmd string) []byte {
	switch cmd {
	case "0601":
		return finsResp(f.status)
	case "0501":
		body := make([]byte, 40)
		copy(body[0:20], "NJ501-1300")
		copy(body[20:40], "1.48")
		return finsResp(body)
	case "060200", "210100000005":
		if f.unsupported {
			d := finsResp(nil)
			d[12], d[13] = 0x04, 0x01
			return d
		}
		if cmd == "060200" {
			body := make([]byte, 12) // 0.1ms 단위, 정렬 전 순서
			binary.BigEndian.PutUint32(body[0:], 50)
			binary.BigEndian.PutUint32(body[4:], 20)
			binary.BigEndian.PutUint32(body[8:], 35)
			return finsResp(body)
		}
		body := make([]byte, 6+10)
		binary.BigEndian.PutUint16(body[4:], 1) // 레코드 수
		binary.BigEndian.PutUint16(body[6:], 0x80F1)
		binary.BigEndian.PutUint16(body[8:], 0x0002)
		copy(body[10:], []byte{0x30, 0x15, 0x09, 0x08, 0x24, 0x03}) // 분 초 일 시 년 월
		return finsResp(body)
	case "0701":
		tm := time.Unix(f.clockEpoch, 0).UTC()
		enc := func(v int) byte { return byte(v/10<<4 | v%10) }
		return finsResp([]byte{
			enc(tm.Year() % 100), enc(int(tm.Month())), enc(tm.Day()),
			enc(tm.Hour()), enc(tm.Minute()), enc(tm.Second()),
		})
	}
	return nil
}

func (f *fakePLC) takeCmds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.cmds
	f.cmds = nil
	return out
}

func (f *fakePLC) set(fn func(*fakePLC)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// plcStatusBody — 0601 응답 body(d[14:40]).
func plcStatusBody(runbit, mode byte, fatal, nonFatal uint16, msg string) []byte {
	body := make([]byte, 26)
	body[0], body[1] = runbit, mode
	binary.BigEndian.PutUint16(body[2:], fatal)
	binary.BigEndian.PutUint16(body[4:], nonFatal)
	copy(body[10:], msg)
	return body
}

// isolateCIP — pollPLC 의 CIP 호출(:44818 고정)이 실제 서비스에 닿지 않도록
// 루프백 CIP 포트를 테스트가 점유한다. TCP 는 즉시 끊고 UDP 는 잘못된 1바이트로
// 응답해 CIP 조회가 타임아웃 없이 nil 로 끝나게 한다. 포트가 사용 중이면 건너뛴다.
func isolateCIP(t *testing.T, ip string) {
	t.Helper()
	addr := net.JoinHostPort(ip, strconv.Itoa(cipPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("CIP port %s unavailable: %v", addr, err)
	}
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		_ = ln.Close()
		t.Skipf("CIP port %s unavailable: %v", addr, err)
	}
	t.Cleanup(func() { _ = ln.Close(); _ = pc.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	go func() {
		buf := make([]byte, 2048)
		for {
			_, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo([]byte{0}, from)
		}
	}()
}

func plcTestEnv(t *testing.T) (*fakePLC, *Worker, *pollCtx, DeviceConfig) {
	t.Helper()
	isolateCIP(t, "127.0.0.1")
	f := newFakePLC(t)
	w, pc := testWorker(fakeSNMP{})
	f.set(func(f *fakePLC) { f.clockEpoch = int64(pc.now+kstOffset.Seconds()) + 5 })
	dev := DeviceConfig{Key: "plc-1", Kind: "plc", IP: "127.0.0.1", FinsPort: f.port()}
	return f, w, pc, dev
}

func plcMeta(t *testing.T, d map[string]any) (map[string]any, []any) {
	t.Helper()
	m := d["meta"].(map[string]any)
	plc, ok := m["plc"].(map[string]any)
	if !ok {
		t.Fatalf("meta.plc missing: %#v", m)
	}
	return plc, m["alerts"].([]any)
}

func alertNames(alerts []any) []string {
	out := []string{}
	for _, a := range alerts {
		out = append(out, a.(map[string]any)["name"].(string))
	}
	return out
}

func assertReadOnly(t *testing.T, cmds []string) {
	t.Helper()
	for _, c := range cmds {
		if !finsReadOnlyCmds[c] {
			t.Fatalf("non read-only FINS command sent: %s (all: %v)", c, cmds)
		}
	}
}

func containsCmd(cmds []string, want string) bool {
	for _, c := range cmds {
		if c == want {
			return true
		}
	}
	return false
}

func TestPollPLCHealthyRunCachesStaticAndStaysReadOnly(t *testing.T) {
	f, w, pc, dev := plcTestEnv(t)

	d := w.pollOne(pc, dev)
	cmds := f.takeCmds()
	assertReadOnly(t, cmds)
	for _, want := range []string{"0601", "0501", "060200", "210100000005", "0701"} {
		if !containsCmd(cmds, want) {
			t.Fatalf("round 1 missing %s: %v", want, cmds)
		}
	}
	if d["status"] != "op" || d["type"] != "PLC" {
		t.Fatalf("status=%v type=%v", d["status"], d["type"])
	}
	plc, alerts := plcMeta(t, d)
	if len(alerts) != 0 {
		t.Fatalf("healthy PLC must have no alerts: %v", alertNames(alerts))
	}
	if plc["runState"] != "RUN" || plc["hasError"] != false || plc["errSev"] != "" {
		t.Fatalf("run/err = %v %v %q", plc["runState"], plc["hasError"], plc["errSev"])
	}
	if plc["model"] != "NJ501-1300" || plc["fwVersion"] != "1.48" || plc["productName"] != "OMRON NJ501-1300" {
		t.Fatalf("model=%v fw=%v product=%v", plc["model"], plc["fwVersion"], plc["productName"])
	}
	if plc["cycleMinMs"] != 2.0 || plc["cycleAvgMs"] != 3.5 || plc["cycleMaxMs"] != 5.0 {
		t.Fatalf("cycle = %v/%v/%v", plc["cycleMinMs"], plc["cycleAvgMs"], plc["cycleMaxMs"])
	}
	if plc["clockSkewSec"] != int64(5) {
		t.Fatalf("clockSkewSec = %#v", plc["clockSkewSec"])
	}
	errlog := plc["errorLog"].([]any)
	if len(errlog) != 1 {
		t.Fatalf("errorLog = %v", errlog)
	}
	e := errlog[0].(map[string]any)
	if e["code"] != "80F1" || e["detail"] != "0002" || e["time"] != "2024-03-09 08:30" {
		t.Fatalf("errorLog[0] = %v", e)
	}
	st, _ := w.static[dev.Key].(*plcStatic)
	if st == nil || st.Model != "NJ501-1300" || !st.HasCycle || !st.HasErrlog {
		t.Fatalf("static cache = %#v", st)
	}

	// 두 번째 라운드: 정적 정보(0501)는 다시 읽지 않는다.
	pc.now += 60
	d = w.pollOne(pc, dev)
	cmds = f.takeCmds()
	assertReadOnly(t, cmds)
	if containsCmd(cmds, "0501") {
		t.Fatalf("static model re-read on round 2: %v", cmds)
	}
	if !containsCmd(cmds, "060200") || !containsCmd(cmds, "210100000005") {
		t.Fatalf("round 2 must keep reading cycle/errlog: %v", cmds)
	}
	if d["status"] != "op" {
		t.Fatalf("round 2 status = %v", d["status"])
	}
}

func TestPollPLCFatalErrorRaisesCriticalAlert(t *testing.T) {
	f, w, pc, dev := plcTestEnv(t)
	f.set(func(f *fakePLC) { f.status = plcStatusBody(0x01, 0x04, 0x1234, 0x0001, "CPU ERR") })

	d := w.pollOne(pc, dev)
	assertReadOnly(t, f.takeCmds())
	if d["status"] != "deg" {
		t.Fatalf("status = %v", d["status"])
	}
	plc, alerts := plcMeta(t, d)
	if plc["hasError"] != true || plc["errSev"] != "major" {
		t.Fatalf("hasError=%v errSev=%v", plc["hasError"], plc["errSev"])
	}
	if plc["errorMessage"] != "FAL 1234 — CPU ERR" || plc["finsFatalCode"] != "1234" || plc["finsNonFatalCode"] != "0001" {
		t.Fatalf("msg=%v fatal=%v nonfatal=%v", plc["errorMessage"], plc["finsFatalCode"], plc["finsNonFatalCode"])
	}
	if plc["errSince"] != tsKST(pc.now) {
		t.Fatalf("errSince = %v", plc["errSince"])
	}
	names := alertNames(alerts)
	if len(names) != 2 || names[0] != "DEVICE_STATE" || names[1] != "PLC_ERROR" {
		t.Fatalf("alerts = %v", names)
	}
	a := alerts[1].(map[string]any)
	if a["severity"] != "critical" || a["desc"] != "FAL 1234 — CPU ERR" {
		t.Fatalf("PLC_ERROR alert = %v", a)
	}
}

func TestPollPLCNonFatalErrorRaisesWarning(t *testing.T) {
	f, w, pc, dev := plcTestEnv(t)
	f.set(func(f *fakePLC) { f.status = plcStatusBody(0x01, 0x04, 0, 0x0040, "") })

	d := w.pollOne(pc, dev)
	if d["status"] != "deg" {
		t.Fatalf("status = %v", d["status"])
	}
	plc, alerts := plcMeta(t, d)
	if plc["hasError"] != false || plc["errSev"] != "minor" || plc["errorMessage"] != "" {
		t.Fatalf("hasError=%v errSev=%v msg=%v", plc["hasError"], plc["errSev"], plc["errorMessage"])
	}
	names := alertNames(alerts)
	if len(names) != 2 || names[0] != "DEVICE_STATE" || names[1] != "PLC_WARNING" {
		t.Fatalf("alerts = %v", names)
	}
	if alerts[1].(map[string]any)["severity"] != "warning" {
		t.Fatalf("PLC_WARNING alert = %v", alerts[1])
	}
}

func TestPollPLCStoppedIsDegraded(t *testing.T) {
	f, w, pc, dev := plcTestEnv(t)
	f.set(func(f *fakePLC) { f.status = plcStatusBody(0x00, 0x00, 0, 0, "") })

	d := w.pollOne(pc, dev)
	plc, alerts := plcMeta(t, d)
	if plc["runState"] != "PROGRAM" {
		t.Fatalf("runState = %v", plc["runState"])
	}
	// PROGRAM 모드는 STOP 이 아니므로 정상(op) 유지.
	if d["status"] != "op" || len(alerts) != 0 {
		t.Fatalf("PROGRAM: status=%v alerts=%v", d["status"], alertNames(alerts))
	}

	// 모드 바이트 없이 운전 비트가 꺼져 있으면 STOP → deg.
	f.set(func(f *fakePLC) { f.status = []byte{0x00} })
	d = w.pollOne(pc, dev)
	plc, alerts = plcMeta(t, d)
	if plc["runState"] != "STOP" || d["status"] != "deg" {
		t.Fatalf("STOP: runState=%v status=%v", plc["runState"], d["status"])
	}
	if names := alertNames(alerts); len(names) != 1 || names[0] != "DEVICE_STATE" {
		t.Fatalf("STOP alerts = %v", names)
	}
}

func TestPollPLCUnsupportedExtendedCommandsAreSkipped(t *testing.T) {
	f, w, pc, dev := plcTestEnv(t)
	f.set(func(f *fakePLC) { f.unsupported = true })

	d := w.pollOne(pc, dev)
	assertReadOnly(t, f.takeCmds())
	st, _ := w.static[dev.Key].(*plcStatic)
	if st == nil || st.HasCycle || st.HasErrlog {
		t.Fatalf("static = %#v", st)
	}
	plc, _ := plcMeta(t, d)
	if plc["cycleAvgMs"] != nil || len(plc["errorLog"].([]any)) != 0 {
		t.Fatalf("cycle=%v errorLog=%v", plc["cycleAvgMs"], plc["errorLog"])
	}

	// 미지원으로 판별된 뒤에는 0602/2101 을 다시 보내지 않는다.
	pc.now += 60
	w.pollOne(pc, dev)
	cmds := f.takeCmds()
	if containsCmd(cmds, "060200") || containsCmd(cmds, "210100000005") {
		t.Fatalf("unsupported commands re-sent: %v", cmds)
	}
}

func TestPollPLCNoResponseIsDownAndKeepsStatic(t *testing.T) {
	f, w, pc, dev := plcTestEnv(t)
	w.pollOne(pc, dev)
	_ = f.conn.Close() // PLC 무응답

	pc.now += 60
	d := w.pollOne(pc, dev)
	if d["status"] != "down" {
		t.Fatalf("status = %v", d["status"])
	}
	plc, alerts := plcMeta(t, d)
	if plc["model"] != "NJ501-1300" || plc["fwVersion"] != "1.48" || plc["finsRttMs"] != nil {
		t.Fatalf("down plc = %v", plc)
	}
	names := alertNames(alerts)
	if len(names) != 1 || names[0] != "DEVICE_STATE" {
		t.Fatalf("alerts = %v", names)
	}
	if alerts[0].(map[string]any)["severity"] != "critical" {
		t.Fatalf("down alert = %v", alerts[0])
	}
	if st, _ := w.static[dev.Key].(*plcStatic); st == nil || st.Model != "NJ501-1300" {
		t.Fatalf("static cache lost on down: %#v", st)
	}
}
