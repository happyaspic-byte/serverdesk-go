package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"serverdesk/internal/config"
	"serverdesk/internal/poller"
)

const maxOperatorReasonRunes = 500

// AuditRecorder persists a structured record before a destructive mutation is
// attempted and again only after it fully commits.
type AuditRecorder interface {
	RecordAudit(poller.AuditRecord) error
}

type mutationAudit struct {
	id     string
	action string
	target string
	reason string
}

func operatorReason(doc map[string]any) (string, error) {
	if doc == nil {
		return "", errors.New("작업 사유가 필요합니다")
	}
	reason, ok := doc["reason"].(string)
	if !ok {
		return "", errors.New("작업 사유(reason)가 필요합니다")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", errors.New("작업 사유(reason)는 비워 둘 수 없습니다")
	}
	if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > maxOperatorReasonRunes {
		return "", errors.New("작업 사유(reason)는 유효한 Unicode 500자 이하여야 합니다")
	}
	return reason, nil
}

func (s *Server) prepareMutationAudit(action, target, reason string) (*mutationAudit, error) {
	if s.Audit == nil {
		return nil, errors.New("감사 기록 저장소를 사용할 수 없습니다")
	}
	randomID := make([]byte, 16)
	if _, err := rand.Read(randomID); err != nil {
		return nil, errors.New("감사 기록 ID를 만들 수 없습니다")
	}
	audit := &mutationAudit{
		id: hex.EncodeToString(randomID), action: action, target: target, reason: reason,
	}
	if err := s.recordMutationAudit(audit, "prepared"); err != nil {
		return nil, err
	}
	return audit, nil
}

func (s *Server) recordMutationAudit(audit *mutationAudit, phase string) error {
	if audit == nil || s.Audit == nil {
		return errors.New("감사 기록 저장소를 사용할 수 없습니다")
	}
	return s.Audit.RecordAudit(poller.AuditRecord{
		ID: audit.id, Action: audit.action, Target: audit.target, Reason: audit.reason,
		Operator: "admin", Phase: phase, Timestamp: time.Now().UTC(),
	})
}

func edgeDeletionDocument(doc map[string]json.RawMessage, key string) (map[string]json.RawMessage, error) {
	raw, ok := doc[config.SectionEdgeDevices]
	if !ok {
		return nil, fmt.Errorf("config 에 항목 없음: %s", key)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("edge_devices 배열 파싱: %w", err)
	}
	filtered := make([]json.RawMessage, 0, len(entries))
	found := false
	for _, entry := range entries {
		var identity struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(entry, &identity); err != nil {
			return nil, fmt.Errorf("edge_devices 항목 파싱: %w", err)
		}
		if identity.Key == key {
			found = true
			continue
		}
		filtered = append(filtered, entry)
	}
	if !found {
		return nil, fmt.Errorf("config 에 항목 없음: %s", key)
	}
	nextRaw, err := json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	next := cloneRawDocument(doc)
	next[config.SectionEdgeDevices] = nextRaw
	return next, nil
}
