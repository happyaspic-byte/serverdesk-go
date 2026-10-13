package alerting

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// DeviceSignalReadiness separates host recovery permission from edge round authority.
// EdgeWorkerReady must describe the same snapshot as devices. Nil readiness leaves
// conversion ungated; notification sources must supply readiness before ingestion.
type DeviceSignalReadiness struct {
	HostRecoveryReady map[string]bool
	EdgeHosts         map[string]bool
	EdgeWorkerReady   bool
}

// DeviceSignals converts device alerts using the UI's stable time and ACK identity.
// For edge alerts only, a boolean observed overrides host recovery readiness:
// true admits current positive evidence, false retains an uncertain condition.
// Neither can bypass EdgeWorkerReady. Absent flags preserve other collectors'
// host readiness contract; recovery permission remains separately owned by the source.
func DeviceSignals(devices []map[string]any, readiness *DeviceSignalReadiness) []Signal {
	var out []Signal
	for _, device := range devices {
		host := deviceString(device, "id")
		if host == "" {
			host = deviceString(device, "host")
		}
		if host == "" {
			continue
		}
		unready := readiness != nil && (!readiness.HostRecoveryReady[host] || (readiness.EdgeHosts[host] && !readiness.EdgeWorkerReady))
		meta, _ := device["meta"].(map[string]any)
		if strings.EqualFold(deviceString(device, "status"), "down") {
			description := "Device offline — no node responded to the collector"
			downSince := deviceString(meta, "downSince")
			ackTime := normalizeNotificationAckTime(downSince)
			out = append(out, Signal{
				Key: "state:" + host + ":down", Host: host,
				Description:   description,
				AckKey:        strings.Join([]string{host, "DEVICE_STATE", description, ackTime}, "\x01"),
				StartedAt:     parseNotificationTime(downSince),
				SourceUnready: unready,
			})
		}
		alerts, _ := meta["alerts"].([]any)
		for _, raw := range alerts {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			severity := strings.ToLower(deviceString(item, "sev"))
			if severity == "" {
				severity = strings.ToLower(deviceString(item, "severity"))
			}
			name := deviceString(item, "name")
			if severity != "critical" || name == "DEVICE_STATE" {
				continue
			}
			description := deviceString(item, "desc")
			if description == "" {
				description = deviceString(item, "description")
			}
			if description == "" {
				description = name
			}
			rawTime := deviceString(item, "time")
			ackTime := normalizeNotificationAckTime(rawTime)
			material := strings.Join([]string{host, name, description, ackTime}, "\x00")
			digest := sha256.Sum256([]byte(material))
			alertUnready := unready
			if readiness != nil && readiness.EdgeHosts[host] {
				if observed, ok := item["observed"].(bool); ok {
					alertUnready = !readiness.EdgeWorkerReady || !observed
				}
			}
			out = append(out, Signal{
				Key: "alert:" + fmt.Sprintf("%x", digest[:16]), Host: host,
				Description: description, AckKey: strings.Join([]string{host, name, description, ackTime}, "\x01"),
				StartedAt: parseNotificationTime(rawTime), SourceUnready: alertUnready,
			})
		}
	}
	return out
}

func deviceString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func normalizeNotificationAckTime(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "no-time"
	}
	value = strings.Replace(value, "T", " ", 1)
	return strings.TrimSuffix(value, "Z")
}

func parseNotificationTime(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	kst := time.FixedZone("KST", 9*60*60)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, kst); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
