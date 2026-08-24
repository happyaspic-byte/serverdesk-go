package edge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxDeviceResponseBytes           = 8 << 20
	maxCompressedDeviceResponseBytes = 2 << 20
)

// httpStatusError — HTTP 4xx/5xx 응답. Python urllib.error.HTTPError 에 해당.
type httpStatusError struct{ Code int }

func (e *httpStatusError) Error() string { return "HTTP status " + strconv.Itoa(e.Code) }

// authFailed — Python 의 `exc.code in (401, 403)` 판정.
func authFailed(err error) bool {
	se, ok := err.(*httpStatusError)
	return ok && (se.Code == 401 || se.Code == 403)
}

// errClass — Python `type(exc).__name__` 에 해당하는 짧은 오류 분류명.
// meta.error 에 들어가 프런트가 그대로 보여주므로 Python 측 이름 관행을 따른다.
func errClass(err error) string {
	switch err.(type) {
	case *httpStatusError:
		return "HTTPError"
	case *jsonSyntaxError:
		return "JSONDecodeError"
	case *valueError:
		return "ValueError"
	}
	return "URLError"
}

// jsonSyntaxError — JSON 디코딩 실패 래퍼.
type jsonSyntaxError struct{ Err error }

func (e *jsonSyntaxError) Error() string { return e.Err.Error() }

// valueError — Python ValueError 에 해당(숫자 변환 실패 등).
type valueError struct{ Msg string }

func (e *valueError) Error() string { return e.Msg }

// ParseFingerprint 는 hex 또는 base64 로 인코딩된 SHA-256 지문(32바이트)을 파싱한다.
// sha256: 접두어, 콜론(:), 하이픈(-), 공백 등 일반적인 표기 형식을 허용한다.
func ParseFingerprint(fp string) ([]byte, error) {
	s := strings.TrimSpace(fp)
	if s == "" {
		return nil, errors.New("tls: 핑거프린트가 비어 있습니다")
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "sha256:") || strings.HasPrefix(lower, "sha-256:") {
		s = s[strings.Index(s, ":")+1:]
		s = strings.TrimSpace(s)
	}

	// 콜론, 하이픈, 공백 제거 후 hex 디코딩 시도
	cleanHex := strings.Map(func(r rune) rune {
		if r == ':' || r == '-' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, s)
	if len(cleanHex) == 64 {
		if b, err := hex.DecodeString(cleanHex); err == nil {
			return b, nil
		}
	}

	// base64 디코딩 시도 (표준/URL-safe, 패딩 유무 허용)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == sha256.Size {
			return b, nil
		}
	}

	return nil, fmt.Errorf("tls: 유효하지 않은 핑거프린트 형식입니다: %q", fp)
}

// NewTLSConfig 는 시스템 CA 검증(기본) 또는 명시적 SPKI SHA-256 피닝을 사용하는
// tls.Config 를 반환한다. 핑거프린트가 지정된 경우에만 자체서명 장비를 허용하며,
// VerifyPeerCertificate 에서 공개키 지문을 상수시간 비교해 불일치 연결을 차단한다.
func NewTLSConfig(fingerprint string) (*tls.Config, error) {
	if strings.TrimSpace(fingerprint) == "" {
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	}
	expectedFP, err := ParseFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec — CA 체인 없는 자체서명 허용 후 아래 콜백에서 SPKI 지문 검증
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("tls: 피어 인증서가 제공되지 않았습니다")
			}
			cert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("tls: 피어 인증서 파싱 실패: %w", err)
			}
			now := time.Now()
			if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
				return fmt.Errorf("tls: 피어 인증서 유효기간을 벗어났습니다 (not_before=%s, not_after=%s)",
					cert.NotBefore.UTC().Format(time.RFC3339), cert.NotAfter.UTC().Format(time.RFC3339))
			}
			spkiHash := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
			if subtle.ConstantTimeCompare(spkiHash[:], expectedFP) != 1 {
				return fmt.Errorf("tls: 인증서 SPKI 핑거프린트 불일치 (기대: %x, 수신: %x)", expectedFP, spkiHash[:])
			}
			return nil
		},
	}, nil
}

// DeviceHTTPClient 는 지정된 타임아웃과 핑거프린트로 HTTPS 클라이언트를 반환한다.
// 핑거프린트 형식이 잘못되었을 경우 연결 시도시 핸드셰이크 단계에서 즉시 실패한다.
func DeviceHTTPClient(timeout time.Duration, fingerprint string) *http.Client {
	tlsCfg, err := NewTLSConfig(fingerprint)
	if err != nil {
		tlsCfg = &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, //nolint:gosec
			VerifyPeerCertificate: func([][]byte, [][]*x509.Certificate) error {
				return fmt.Errorf("유효하지 않은 tls_fingerprint 설정: %w", err)
			},
		}
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) == 0 || !sameHTTPSOrigin(req.URL, via[0].URL) {
				return errors.New("edge: cross-origin or non-HTTPS redirect refused")
			}
			if len(via) >= 5 {
				return errors.New("edge: too many redirects")
			}
			return nil
		},
		Transport: &http.Transport{
			TLSClientConfig:     tlsCfg,
			MaxIdleConnsPerHost: 2,
		},
	}
}

func sameHTTPSOrigin(a, b *url.URL) bool {
	if a == nil || b == nil || !strings.EqualFold(a.Scheme, "https") || !strings.EqualFold(b.Scheme, "https") ||
		!strings.EqualFold(a.Hostname(), b.Hostname()) {
		return false
	}
	port := func(u *url.URL) string {
		if value := u.Port(); value != "" {
			return value
		}
		return "443"
	}
	return port(a) == port(b)
}

func readLimitedBody(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("edge: device response exceeds %d bytes", limit)
	}
	return data, nil
}

// verifiedClient — 시스템 신뢰 저장소와 호스트명 검증을 사용하는 기본 HTTPS 클라이언트.
// 자체서명 장비는 장비별 tls_fingerprint 를 명시해야 한다.
func verifiedClient(timeout time.Duration) *http.Client {
	return DeviceHTTPClient(timeout, "")
}

// pollCtx — 한 라운드의 공유 환경.
type pollCtx struct {
	ctx     context.Context
	snmp    SNMPGetFunc
	now     float64 // epoch 초(float) — Python time.time() 과 같은 스케일
	refresh bool    // 이번 라운드에 정적 정보 재조회 여부
	sws     *http.Client
	pve     *http.Client
	rf      *http.Client
}
