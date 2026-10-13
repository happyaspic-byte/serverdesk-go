package snmp

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

// Independent BER fields, including negative INTEGER sentinel -1.
func responsePacket(reqID []byte, version, community, pdu byte, oid, value []byte) []byte {
	return tlv(0x30, concat(berInt(int64(version)), tlv(tagOctets, []byte{community}), tlv(pdu, concat(tlv(tagInt, reqID), berInt(0), berInt(0), tlv(0x30, tlv(0x30, concat(tlv(tagOID, oid), value)))))))
}

func TestGetIgnoresUnmatchedDatagrams(t *testing.T) {
	cases := []string{"source", "request", "version", "community", "pdu", "oid", "tag", "trailing", "malformed"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			agent, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close()
			done := make(chan struct{})
			defer func() { <-done }()
			go func() {
				defer close(done)
				b := make([]byte, 2048)
				n, src, err := agent.ReadFromUDP(b)
				if err != nil {
					return
				}
				r := &reader{b: b[:n]}
				_, seq, _ := r.read()
				r = &reader{b: seq}
				r.read()
				r.read()
				_, p, _ := r.read()
				r = &reader{b: p}
				_, id, _ := r.read()
				oid := []byte{0x2b, 6, 1, 2, 1, 1, 3, 0}
				good := responsePacket(id, 1, 's', pduResponse, oid, []byte{tagInt, 1, 0xff})
				bad := append([]byte(nil), good...)
				switch name {
				case "request":
					bad = responsePacket([]byte{0}, 1, 's', pduResponse, oid, []byte{tagInt, 1, 0x7f})
				case "version":
					bad = responsePacket(id, 0, 's', pduResponse, oid, []byte{tagInt, 1, 0x7f})
				case "community":
					bad = responsePacket(id, 1, 'x', pduResponse, oid, []byte{tagInt, 1, 0x7f})
				case "pdu":
					bad = responsePacket(id, 1, 's', pduGet, oid, []byte{tagInt, 1, 0x7f})
				case "oid":
					bad = responsePacket(id, 1, 's', pduResponse, []byte{0x2b, 6, 1, 2, 1, 1, 5, 0}, []byte{tagInt, 1, 0x7f})
				case "tag":
					bad[0] = 0x31
				case "trailing":
					bad = append(bad, 0)
				case "malformed":
					bad = []byte{0x30, 0x80}
				}
				if name == "source" {
					rogue, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
					defer rogue.Close()
					rogue.WriteToUDP(responsePacket(id, 1, 's', pduResponse, oid, []byte{tagInt, 1, 0x7f}), src)
				} else {
					agent.WriteToUDP(bad, src)
				}
				time.Sleep(10 * time.Millisecond)
				agent.WriteToUDP(good, src)
			}()
			got, err := Get(context.Background(), "127.0.0.1", agent.LocalAddr().(*net.UDPAddr).Port, "s", []string{OIDSysUpTime}, time.Second)
			if err != nil || got[OIDSysUpTime].Int != -1 {
				t.Fatalf("matched response = %v, %v; want -1", got, err)
			}
		})
	}
}

func TestBERSignedValuesAndBounds(t *testing.T) {
	for _, tc := range []struct {
		hex  []byte
		want int64
	}{{[]byte{0xff}, -1}, {[]byte{0x80}, -128}, {[]byte{0xff, 0x7f}, -129}, {[]byte{0, 0x80}, 128}} {
		packet := responsePacket([]byte{1}, 1, 's', pduResponse, []byte{0x2b, 6, 1, 2, 1, 1, 3, 0}, tlv(tagInt, tc.hex))
		got, err := parseGetResponse(packet)
		if err != nil || got[OIDSysUpTime].Int != tc.want {
			t.Errorf("GET %x = %v %v, want %d", tc.hex, got, err, tc.want)
		}
		trap := tlv(0x30, concat(berInt(1), tlv(tagOctets, []byte("s")), tlv(pduV2Trap, concat(berInt(1), berInt(0), berInt(0), tlv(0x30, tlv(0x30, concat([]byte{6, 8, 0x2b, 6, 1, 2, 1, 1, 3, 0}, tlv(tagInt, tc.hex))))))))
		tr, ok := NewDecoder().Decode(trap, "127.0.0.1")
		if !ok || tr.Varbinds[0].Value != tc.want {
			t.Errorf("trap %x = %+v %v", tc.hex, tr, ok)
		}
	}
	for _, v := range [][]byte{{}, bytes.Repeat([]byte{1}, 9)} {
		if _, err := parseGetResponse(responsePacket([]byte{1}, 1, 's', pduResponse, []byte{0x2b, 6, 1, 2, 1, 1, 3, 0}, tlv(tagInt, v))); err == nil {
			t.Errorf("invalid INTEGER %x accepted", v)
		}
	}
	for _, v := range [][]byte{{0xff}, {1, 0, 0, 0, 0}, {0, 1, 0, 0, 0, 0}, {}} {
		if _, err := parseGetResponse(responsePacket([]byte{1}, 1, 's', pduResponse, []byte{0x2b, 6, 1, 2, 1, 1, 3, 0}, tlv(tagGauge32, v))); err == nil {
			t.Errorf("invalid Gauge32 %x accepted", v)
		}
	}
}

func TestBERCounter64RangePreservesRawUnsupported(t *testing.T) {
	for _, tc := range []struct {
		b    []byte
		kind ValueKind
		want int64
	}{{[]byte{0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, KindCounter64, 9223372036854775807}, {[]byte{0, 0x80, 0, 0, 0, 0, 0, 0, 0}, KindBytes, 0}, {[]byte{0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, KindBytes, 0}, {[]byte{0, 1}, KindCounter64, 1}} {
		got, err := parseGetResponse(responsePacket([]byte{1}, 1, 's', pduResponse, []byte{0x2b, 6, 1, 2, 1, 1, 3, 0}, tlv(tagCounter64, tc.b)))
		v := got[OIDSysUpTime]
		if err != nil || v.Kind != tc.kind || v.Int != tc.want || (tc.kind == KindBytes && !bytes.Equal(v.Bytes, tc.b)) {
			t.Errorf("Counter64 %x = %+v %v", tc.b, v, err)
		}
	}
	for _, b := range [][]byte{{0x80, 0, 0, 0, 0, 0, 0, 0}, {0, 1, 0, 0, 0, 0, 0, 0, 0, 0}} {
		if _, err := parseGetResponse(responsePacket([]byte{1}, 1, 's', pduResponse, []byte{0x2b, 6, 1, 2, 1, 1, 3, 0}, tlv(tagCounter64, b))); err == nil {
			t.Errorf("invalid Counter64 %x accepted", b)
		}
	}
}

func TestGetMismatchDoesNotExtendDeadline(t *testing.T) {
	agent, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	go func() {
		b := make([]byte, 2048)
		for {
			_, src, err := agent.ReadFromUDP(b)
			if err != nil {
				return
			}
			for i := 0; i < 4; i++ {
				agent.WriteToUDP([]byte{0x30, 0x80}, src)
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()
	start := time.Now()
	_, err = Get(context.Background(), "127.0.0.1", agent.LocalAddr().(*net.UDPAddr).Port, "s", []string{OIDSysUpTime}, 120*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil || elapsed < 90*time.Millisecond || elapsed > 400*time.Millisecond {
		t.Fatalf("mismatch deadline elapsed=%v err=%v", elapsed, err)
	}
}

func TestGetRejectsUnterminatedOID(t *testing.T) {
	packet := responsePacket([]byte{1}, 1, 's', pduResponse, []byte{0x2b, 6, 0x81}, []byte{tagInt, 1, 1})
	if _, err := parseGetResponse(packet); err == nil {
		t.Fatal("unterminated OID accepted")
	}
}

func TestTrapRejectsInvalidEnvelope(t *testing.T) {
	good := mustHex(t, goldenV2cHex)
	for _, bad := range [][]byte{append(append([]byte(nil), good...), 0), append([]byte{0x31}, good[1:]...)} {
		if _, ok := NewDecoder().Decode(bad, "127.0.0.1"); ok {
			t.Fatal("invalid envelope accepted")
		}
	}
	bad := append([]byte(nil), good...)
	bad[4] = 0
	if _, ok := NewDecoder().Decode(bad, "127.0.0.1"); ok {
		t.Fatal("v1/v2 trap mismatch accepted")
	}
}
