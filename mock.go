package main

// mock.go is the built-in mock server: canned endpoints the user defines so a
// flow can be tested before the real upstream exists (or while it is down).
// The rules live in the store; the server runs only while started on the Mock
// sidebar tab. The handler reads the rules under mockMu — the UI thread edits
// them in place while requests arrive.
import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vivaui/viva/ui"
)

const mockDefaultPort = 18888

// mockPortOrDefault is the port the mock server binds.
func (m *model) mockPortOrDefault() int {
	if m.mockPort > 0 {
		return m.mockPort
	}
	return mockDefaultPort
}

// mockServeHTTP answers a request with the first enabled rule of the published
// snapshot whose method and path match; the query string is ignored. JSON
// bodies get the JSON content type, anything else plain text. No match: a 404
// that names what was missed.
func (m *model) mockServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mockMu.Lock()
	rules := m.mockServeRules
	m.mockMu.Unlock()
	for i := range rules {
		ru := &rules[i]
		if !ru.Enabled || ru.Method != "" && ru.Method != r.Method || ru.Path != r.URL.Path {
			continue
		}
		status := 200
		if s, err := strconv.Atoi(strings.TrimSpace(ru.Status)); err == nil && s > 0 {
			status = s
		}
		if ct := mockContentType(ru.Body); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(ru.Body))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"no mock for ` + r.Method + " " + r.URL.Path + `"}`))
}

// publishMockRules hands the server goroutine a fresh snapshot whenever a
// rule's content changed. It runs each paint: the fingerprint walk touches no
// allocation, so an unchanged frame costs a few compares.
func (m *model) publishMockRules() {
	fp := mockFingerprint(m.mockRules)
	if fp == m.mockFingerprint {
		return
	}
	m.mockFingerprint = fp
	snap := make([]mockRule, len(m.mockRules))
	copy(snap, m.mockRules)
	m.mockMu.Lock()
	m.mockServeRules = snap
	m.mockMu.Unlock()
}

// mockFingerprint hashes the rules' content (FNV-1a), so any edit — including
// same-length ones — republishes.
func mockFingerprint(rules []mockRule) uint64 {
	const offset, prime = uint64(1469598103934665603), uint64(1099511628211)
	h := offset
	step := func(s string) {
		for i := 0; i < len(s); i++ {
			h ^= uint64(s[i])
			h *= prime
		}
		h ^= '\n'
		h *= prime
	}
	for i := range rules {
		ru := &rules[i]
		step(ru.Method)
		step(ru.Path)
		step(ru.Status)
		step(ru.Body)
		if ru.Enabled {
			h ^= 'e'
			h *= prime
		}
	}
	return h
}

// mockContentType picks a response content type from the body's shape.
func mockContentType(body string) string {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	if json.Valid([]byte(body)) {
		return "application/json; charset=utf-8"
	}
	return "text/plain; charset=utf-8"
}

// startMockServer binds 127.0.0.1 and serves until stopMockServer. A bind
// failure surfaces in the Mock tab (mockStartErr) via the invalidator — the
// goroutine must not touch the window thread itself.
func (m *model) startMockServer() {
	m.mockMu.Lock()
	if m.mockSrv != nil {
		m.mockMu.Unlock()
		return
	}
	addr := fmt.Sprintf("127.0.0.1:%d", m.mockPortOrDefault())
	srv := &http.Server{
		Addr:              addr,
		Handler:           http.HandlerFunc(m.mockServeHTTP),
		ReadHeaderTimeout: 5 * time.Second,
	}
	m.mockSrv = srv
	m.mockStartErr = ""
	m.mockOn = true
	m.mockMu.Unlock()
	m.persist()
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			m.mockMu.Lock()
			m.mockStartErr = err.Error()
			m.mockOn = false
			m.mockSrv = nil
			m.mockMu.Unlock()
			if m.inv != nil {
				m.inv.Invalidate()
			}
		}
	}()
	m.toaster.Show("Mock 已启动", "http://"+addr, ui.ToastSuccess)
}

// stopMockServer shuts the listener down; an idle server closes at once, and
// the shutdown itself runs off the window thread.
func (m *model) stopMockServer() {
	m.mockMu.Lock()
	srv := m.mockSrv
	if srv == nil {
		m.mockMu.Unlock()
		return
	}
	m.mockSrv = nil
	m.mockOn = false
	m.mockMu.Unlock()
	m.persist()
	go func() { _ = srv.Shutdown(context.Background()) }()
	m.toaster.Show("Mock 已停止", "", ui.ToastSuccess)
}

// mockAddr is the base URL the running server answers on.
func (m *model) mockAddr() string {
	return "http://127.0.0.1:" + strconv.Itoa(m.mockPortOrDefault())
}
