package webauth

import (
	"fmt"
	"net/http"
)

// AuditMutations records every authenticated state change after the auth
// middleware has admitted the request. It deliberately excludes query strings
// and bodies so device credentials cannot enter logs.
func AuditMutations(next http.Handler, auditf func(string)) http.Handler {
	if auditf == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isMutationMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		recorder := &auditResponseWriter{ResponseWriter: w}
		path := r.URL.EscapedPath()
		if path == "" {
			path = "/"
		}
		completed := false
		defer func() {
			status := recorder.Status()
			if !completed && recorder.status == 0 {
				status = http.StatusInternalServerError
			}
			auditf(fmt.Sprintf("event=mutation operator=admin method=%s path=%q status=%d remote=%s",
				r.Method, path, status, TrustedClientIP(r)))
		}()
		next.ServeHTTP(recorder, r)
		completed = true
	})
}

func isMutationMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPut ||
		method == http.MethodDelete || method == http.MethodPatch
}

type auditResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (w *auditResponseWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *auditResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
