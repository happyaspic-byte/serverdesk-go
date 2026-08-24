package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// listenerTransport validates the web listener before workers start. Plain HTTP is
// deliberately restricted to loopback. The opt-in is a break-glass compatibility
// mode, not a trusted-proxy declaration; webauth still ignores forwarded headers
// from non-loopback peers.
type listenerTransport struct {
	addr              string
	certFile          string
	keyFile           string
	allowInsecureHTTP bool
}

func (t listenerTransport) validate() error {
	if strings.TrimSpace(t.addr) == "" {
		return fmt.Errorf("웹 리스너 주소가 비어 있습니다; listen 또는 -listen에 host:port를 지정하십시오")
	}
	host, port, err := net.SplitHostPort(t.addr)
	if err != nil {
		return fmt.Errorf("웹 리스너 주소 %q가 올바른 host:port 형식이 아닙니다: %w", t.addr, err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("웹 리스너 주소 %q의 포트는 1~65535 범위의 숫자여야 합니다", t.addr)
	}
	if (t.certFile == "") != (t.keyFile == "") {
		return fmt.Errorf("TLS 인증서와 개인키는 함께 지정해야 합니다 (-tls-cert/-tls-key 또는 tls_cert_file/tls_key_file)")
	}
	if t.certFile != "" {
		pair, err := tls.LoadX509KeyPair(t.certFile, t.keyFile)
		if err != nil {
			return fmt.Errorf("TLS 인증서/개인키를 읽을 수 없습니다: %w", err)
		}
		if len(pair.Certificate) == 0 {
			return fmt.Errorf("TLS 인증서에 leaf certificate가 없습니다")
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return fmt.Errorf("TLS leaf 인증서를 해석할 수 없습니다: %w", err)
		}
		now := time.Now()
		if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
			return fmt.Errorf("TLS 인증서 유효기간이 현재 시각을 포함하지 않습니다 (not_before=%s not_after=%s)",
				leaf.NotBefore.UTC().Format(time.RFC3339), leaf.NotAfter.UTC().Format(time.RFC3339))
		}
		keyInfo, err := os.Stat(t.keyFile)
		if err != nil || !keyInfo.Mode().IsRegular() {
			return fmt.Errorf("TLS 개인키는 일반 파일이어야 합니다")
		}
		if runtime.GOOS != "windows" && keyInfo.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("TLS 개인키 권한은 group/other 읽기를 허용하면 안 됩니다 (got %o)", keyInfo.Mode().Perm())
		}
		return nil
	}
	if loopbackListenHost(host) || t.allowInsecureHTTP {
		return nil
	}
	return fmt.Errorf("보안상 평문 HTTP는 루프백 주소에서만 허용됩니다: listen=%q; "+
		"직접 HTTPS에는 tls_cert_file/tls_key_file(또는 -tls-cert/-tls-key)을 설정하고, "+
		"레거시 호환이 불가피한 경우에만 break-glass 옵션 allow_insecure_http=true "+
		"(또는 -allow-insecure-http)를 명시하십시오", t.addr)
}

func (t listenerTransport) tlsEnabled() bool { return t.certFile != "" && t.keyFile != "" }

func (t listenerTransport) configureServerTLS(srv *http.Server) {
	if t.tlsEnabled() {
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
}

func (t listenerTransport) loopback() bool {
	host, _, err := net.SplitHostPort(t.addr)
	return err == nil && loopbackListenHost(host)
}

func loopbackListenHost(host string) bool {
	host = strings.TrimSpace(strings.TrimSuffix(host, "."))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
