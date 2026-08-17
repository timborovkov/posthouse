package httpauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccessKeyPrefersAccessKeyAndAcceptsAlias(t *testing.T) {
	t.Setenv("POSTHOUSE_ACCESS_KEY", "a-sufficiently-long-key")
	t.Setenv("POSTHOUSE_MCP_TOKEN", "")
	key, err := AccessKey()
	if err != nil || key != "a-sufficiently-long-key" {
		t.Fatalf("AccessKey() = %q, %v", key, err)
	}

	t.Setenv("POSTHOUSE_ACCESS_KEY", "")
	t.Setenv("POSTHOUSE_MCP_TOKEN", "legacy-token-value")
	key, err = AccessKey()
	if err != nil || key != "legacy-token-value" {
		t.Fatalf("alias AccessKey() = %q, %v", key, err)
	}
}

func TestAccessKeyRejectsMismatchAndShortKeys(t *testing.T) {
	t.Setenv("POSTHOUSE_ACCESS_KEY", "aaaaaaaaaaaaaaaa")
	t.Setenv("POSTHOUSE_MCP_TOKEN", "bbbbbbbbbbbbbbbb")
	if _, err := AccessKey(); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("mismatched keys error = %v", err)
	}

	t.Setenv("POSTHOUSE_ACCESS_KEY", "short")
	t.Setenv("POSTHOUSE_MCP_TOKEN", "")
	if _, err := AccessKey(); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("short key error = %v", err)
	}

	t.Setenv("POSTHOUSE_ACCESS_KEY", "")
	t.Setenv("POSTHOUSE_MCP_TOKEN", "")
	if _, err := AccessKey(); err == nil || !strings.Contains(err.Error(), "POSTHOUSE_ACCESS_KEY") {
		t.Fatalf("missing key error = %v", err)
	}
}

func TestGuardAcceptsBearerAndHeaderKey(t *testing.T) {
	guard, err := NewGuard("test-access-key-1")
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	handler := guard.Middleware(next)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/cache", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth status = %d", unauthorized.Code)
	}

	bearer := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/cache", nil)
	request.Header.Set("Authorization", "Bearer test-access-key-1")
	handler.ServeHTTP(bearer, request)
	if bearer.Code != http.StatusNoContent {
		t.Fatalf("bearer status = %d", bearer.Code)
	}

	lowercase := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/v1/cache", nil)
	request.Header.Set("Authorization", "bearer test-access-key-1")
	handler.ServeHTTP(lowercase, request)
	if lowercase.Code != http.StatusNoContent {
		t.Fatalf("lowercase bearer status = %d", lowercase.Code)
	}

	headerKey := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/v1/cache", nil)
	request.Header.Set("X-Posthouse-Key", "test-access-key-1")
	handler.ServeHTTP(headerKey, request)
	if headerKey.Code != http.StatusNoContent {
		t.Fatalf("header key status = %d", headerKey.Code)
	}
}

func TestGuardLocksOutRepeatedFailures(t *testing.T) {
	guard, err := NewGuard("test-access-key-1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	guard.now = func() time.Time { return now }
	handler := guard.Middleware(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest(http.MethodGet, "/v1/cache", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("Authorization", "Bearer wrong-access-key")
	var last int
	for i := 0; i < maxFailures; i++ {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		last = recorder.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("lockout status = %d, want 429", last)
	}
	locked := httptest.NewRecorder()
	good := httptest.NewRequest(http.MethodGet, "/v1/cache", nil)
	good.RemoteAddr = "192.0.2.10:1234"
	good.Header.Set("Authorization", "Bearer test-access-key-1")
	handler.ServeHTTP(locked, good)
	if locked.Code != http.StatusTooManyRequests || locked.Header().Get("Retry-After") == "" {
		t.Fatalf("correct key during lockout status=%d retry=%q", locked.Code, locked.Header().Get("Retry-After"))
	}

	other := httptest.NewRecorder()
	otherReq := httptest.NewRequest(http.MethodGet, "/v1/cache", nil)
	otherReq.RemoteAddr = "192.0.2.11:1234"
	otherReq.Header.Set("Authorization", "Bearer test-access-key-1")
	handler.ServeHTTP(other, otherReq)
	if other.Code != http.StatusNoContent {
		t.Fatalf("unrelated client status = %d", other.Code)
	}
}

func TestGuardTrustsForwardedClientWhenEnabled(t *testing.T) {
	t.Setenv("POSTHOUSE_TRUST_PROXY", "1")
	guard, err := NewGuard("test-access-key-1")
	if err != nil {
		t.Fatal(err)
	}
	if !guard.trustProxy {
		t.Fatal("expected trust proxy")
	}
	spoofed := httptest.NewRequest(http.MethodGet, "/", nil)
	spoofed.RemoteAddr = "127.0.0.1:1"
	spoofed.Header.Set("X-Forwarded-For", "198.51.100.9, 203.0.113.10")

	rightmost := httptest.NewRequest(http.MethodGet, "/", nil)
	rightmost.RemoteAddr = "203.0.113.10:1"

	leftmost := httptest.NewRequest(http.MethodGet, "/", nil)
	leftmost.RemoteAddr = "198.51.100.9:1"

	proxy := httptest.NewRequest(http.MethodGet, "/", nil)
	proxy.RemoteAddr = "127.0.0.1:1"

	if guard.clientID(spoofed) == guard.clientID(proxy) {
		t.Fatal("trusted forwarded client should not collapse onto the proxy address")
	}
	if guard.clientID(spoofed) != guard.clientID(rightmost) {
		t.Fatal("trusted X-Forwarded-For should use the rightmost hop")
	}
	if guard.clientID(spoofed) == guard.clientID(leftmost) {
		t.Fatal("leftmost X-Forwarded-For hop is spoofable and must not identify the client")
	}

	realIP := httptest.NewRequest(http.MethodGet, "/", nil)
	realIP.RemoteAddr = "127.0.0.1:1"
	realIP.Header.Set("X-Forwarded-For", "198.51.100.9")
	realIP.Header.Set("X-Real-IP", "203.0.113.10")
	if guard.clientID(realIP) != guard.clientID(rightmost) {
		t.Fatal("X-Real-IP should win over X-Forwarded-For")
	}

	private := httptest.NewRequest(http.MethodGet, "/", nil)
	private.RemoteAddr = "10.0.0.2:443"
	private.Header.Set("X-Forwarded-For", "198.51.100.9, 203.0.113.10")
	if guard.clientID(private) != guard.clientID(rightmost) {
		t.Fatal("private proxy hop should honor forwarded headers")
	}

	public := httptest.NewRequest(http.MethodGet, "/", nil)
	public.RemoteAddr = "198.51.100.20:443"
	public.Header.Set("X-Real-IP", "203.0.113.10")
	public.Header.Set("X-Forwarded-For", "203.0.113.10")
	if guard.clientID(public) == guard.clientID(rightmost) {
		t.Fatal("forwarded headers from a public peer must not identify the client")
	}
}

func TestGuardClearsFailuresAfterLockoutExpires(t *testing.T) {
	guard, err := NewGuard("test-access-key-1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	guard.now = func() time.Time { return now }
	handler := guard.Middleware(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))

	bad := httptest.NewRequest(http.MethodGet, "/v1/cache", nil)
	bad.RemoteAddr = "192.0.2.40:1234"
	bad.Header.Set("Authorization", "Bearer wrong-access-key")
	for i := 0; i < maxFailures; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), bad)
	}

	now = now.Add(lockoutDuration + time.Second)
	good := httptest.NewRequest(http.MethodGet, "/v1/cache", nil)
	good.RemoteAddr = "192.0.2.40:1234"
	good.Header.Set("Authorization", "Bearer test-access-key-1")
	unlocked := httptest.NewRecorder()
	handler.ServeHTTP(unlocked, good)
	if unlocked.Code != http.StatusNoContent {
		t.Fatalf("correct key after lockout expiry status = %d", unlocked.Code)
	}

	retry := httptest.NewRecorder()
	handler.ServeHTTP(retry, bad)
	if retry.Code != http.StatusUnauthorized {
		t.Fatalf("one failure after lockout expiry should be 401, got %d", retry.Code)
	}
}
