package edge

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestCIPFragmentedSession(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:44818")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	defer func() { <-done }()
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(time.Second))
		reg := make([]byte, 28)
		if _, err := io.ReadFull(c, reg); err != nil {
			return
		}
		response := cipEncap(0x65, 7, []byte{1, 0, 0, 0})
		c.Write(response[:4])
		time.Sleep(10 * time.Millisecond)
		c.Write(response[4:])
		req := make([]byte, 48)
		if _, err := io.ReadFull(c, req); err != nil {
			return
		}
		response = cipAttrResp(0, 0, []byte{0xe8, 3, 0, 0})
		for _, b := range response {
			if _, err := c.Write([]byte{b}); err != nil {
				return
			}
		}
	}()
	got := cipReadAttrs(context.Background(), "127.0.0.1", [][3]byte{{0xf6, 1, 1}}, time.Second)
	if len(got[0]) != 4 || binary.LittleEndian.Uint32(got[0]) != 1000 {
		t.Fatalf("fragmented response=%x", got)
	}
}

func TestCIPRejectsInvalidFrames(t *testing.T) {
	for _, name := range []string{"command", "status", "context", "options", "length", "cpf", "service", "truncated"} {
		t.Run(name, func(t *testing.T) {
			r := cipAttrResp(0, 0, []byte{1, 2, 3, 4})
			switch name {
			case "command":
				r[0] = 0x63
			case "status":
				r[8] = 1
			case "context":
				r[12] = 'x'
			case "options":
				r[20] = 1
			case "length":
				r[2]++
			case "cpf":
				r[36] = 0
			case "service":
				r[40] = 0x81
			case "truncated":
				r = r[:len(r)-1]
			}
			if got := parseCIPAttrData(r); got != nil {
				t.Fatalf("invalid %s accepted: %x", name, got)
			}
		})
	}
}

func TestCIPFramedReadRejectsEOFLengthAndSession(t *testing.T) {
	for _, kind := range []string{"headerEOF", "payloadEOF", "oversize", "session", "registerZero", "coalesced"} {
		t.Run(kind, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			a.SetDeadline(time.Now().Add(time.Second))
			frame := cipAttrResp(0, 0, []byte{1, 2, 3, 4})
			command := uint16(0x6f)
			session := uint32(7)
			switch kind {
			case "headerEOF":
				frame = frame[:23]
			case "payloadEOF":
				frame = frame[:len(frame)-1]
			case "oversize":
				frame = frame[:24]
				binary.LittleEndian.PutUint16(frame[2:4], 2049)
			case "session":
				frame[4] = 8
			case "registerZero":
				command = 0x65
				session = 0
				frame = cipEncap(command, 0, []byte{1, 0, 0, 0})
			case "coalesced":
				frame = append(frame, frame...)
			}
			done := make(chan struct{})
			go func() { defer close(done); defer b.Close(); b.Write(frame) }()
			r, err := readCIPFrame(a, command, session)
			if kind == "coalesced" {
				if err != nil || len(r) != 48 {
					t.Fatalf("first frame %x %v", r, err)
				}
				r, err = readCIPFrame(a, command, session)
				if err != nil || len(r) != 48 {
					t.Fatalf("second frame %x %v", r, err)
				}
			} else if err == nil {
				t.Fatalf("invalid %s accepted", kind)
			}
			a.Close()
			<-done
		})
	}
}

func TestEstablishedTransportCancellation(t *testing.T) {
	for _, kind := range []string{"fins", "cipTCP", "cipUDP"} {
		for _, deadline := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "Cancel", true: "Deadline"}[deadline], func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				if deadline {
					ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
				}
				defer cancel()
				received := make(chan struct{})
				done := make(chan struct{})
				start := time.Now()
				if kind == "cipTCP" {
					ln, err := net.Listen("tcp", "127.0.0.1:44818")
					if err != nil {
						t.Fatal(err)
					}
					defer ln.Close()
					go func() {
						c, err := ln.Accept()
						if err != nil {
							return
						}
						defer c.Close()
						b := make([]byte, 28)
						io.ReadFull(c, b)
						close(received)
						<-done
					}()
					go func() { cipReadAttrs(ctx, "127.0.0.1", [][3]byte{{1, 1, 1}}, time.Second); close(done) }()
				} else {
					port := 0
					if kind == "cipUDP" {
						port = 44818
					}
					c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
					if err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					go func() { b := make([]byte, 1024); c.ReadFromUDP(b); close(received) }()
					go func() {
						if kind == "fins" {
							finsReq(ctx, "127.0.0.1", c.LocalAddr().(*net.UDPAddr).Port, []byte{6, 1}, 1, time.Second)
						} else {
							cipIdentityReq(ctx, "127.0.0.1", time.Second)
						}
						close(done)
					}()
				}
				<-received
				if !deadline {
					cancel()
				}
				select {
				case <-done:
				case <-time.After(250 * time.Millisecond):
					t.Errorf("established %s ignores context (%v)", kind, time.Since(start))
					<-done
				}
			})
		}
	}
}
