// Package httpauth authenticates Posthouse HTTP surfaces with a single access key
// and in-memory brute-force defence. It is not multi-tenant authorization.
package httpauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	MinAccessKeyLength = 16
	maxTrackedClients  = 4096
	failureWindow      = 15 * time.Minute
	lockoutDuration    = 15 * time.Minute
	maxFailures        = 8
)

// AccessKey returns the configured HTTP access key. POSTHOUSE_ACCESS_KEY is
// preferred; POSTHOUSE_MCP_TOKEN remains accepted as an alias so existing
// deployments keep working. If both are set they must be identical.
func AccessKey() (string, error) {
	access := strings.TrimSpace(os.Getenv("POSTHOUSE_ACCESS_KEY"))
	legacy := strings.TrimSpace(os.Getenv("POSTHOUSE_MCP_TOKEN"))
	switch {
	case access != "" && legacy != "" && access != legacy:
		return "", fmt.Errorf("POSTHOUSE_ACCESS_KEY and POSTHOUSE_MCP_TOKEN are both set but do not match")
	case access != "":
		return validateKey(access)
	case legacy != "":
		return validateKey(legacy)
	default:
		return "", fmt.Errorf("POSTHOUSE_ACCESS_KEY is required for HTTP (POSTHOUSE_MCP_TOKEN is accepted as an alias)")
	}
}

func validateKey(key string) (string, error) {
	if len(key) < MinAccessKeyLength {
		return "", fmt.Errorf("HTTP access key must be at least %d characters", MinAccessKeyLength)
	}
	return key, nil
}

type clientState struct {
	failures    int
	windowStart time.Time
	lockedUntil time.Time
	lastSeen    time.Time
}

// Guard authenticates Bearer tokens and locks out noisy client addresses.
type Guard struct {
	key        string
	trustProxy bool
	mu         sync.Mutex
	clients    map[string]*clientState
	now        func() time.Time
}

func NewGuard(key string) (*Guard, error) {
	key, err := validateKey(key)
	if err != nil {
		return nil, err
	}
	return &Guard{
		key:        key,
		trustProxy: truthy(os.Getenv("POSTHOUSE_TRUST_PROXY")),
		clients:    make(map[string]*clientState),
		now:        time.Now,
	}, nil
}

func (g *Guard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		client := g.clientID(request)
		if retry := g.lockout(client); retry > 0 {
			writeAuthError(writer, http.StatusTooManyRequests, retry)
			return
		}
		if !authorized(request, g.key) {
			retry := g.noteFailure(client)
			if retry > 0 {
				writeAuthError(writer, http.StatusTooManyRequests, retry)
				return
			}
			writeAuthError(writer, http.StatusUnauthorized, 0)
			return
		}
		g.noteSuccess(client)
		next.ServeHTTP(writer, request)
	})
}

func authorized(request *http.Request, key string) bool {
	bearerOK := secretEqual(bearerToken(request.Header.Get("Authorization")), key)
	headerOK := secretEqual(strings.TrimSpace(request.Header.Get("X-Posthouse-Key")), key)
	return bearerOK || headerOK
}

func bearerToken(header string) string {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

func secretEqual(provided, wanted string) bool {
	sumProvided := sha256.Sum256([]byte(provided))
	sumWanted := sha256.Sum256([]byte(wanted))
	return subtle.ConstantTimeCompare(sumProvided[:], sumWanted[:]) == 1
}

func (g *Guard) clientID(request *http.Request) string {
	host := clientAddress(request, g.trustProxy)
	sum := sha256.Sum256([]byte(host))
	return hex.EncodeToString(sum[:8])
}

func clientAddress(request *http.Request, trustProxy bool) string {
	if trustProxy && trustedForwardingPeer(request.RemoteAddr) {
		if realIP := strings.TrimSpace(request.Header.Get("X-Real-IP")); realIP != "" {
			return realIP
		}
		if forwarded := request.Header.Get("X-Forwarded-For"); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			if host := strings.TrimSpace(parts[len(parts)-1]); host != "" {
				return host
			}
		}
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || host == "" {
		return request.RemoteAddr
	}
	return host
}

func trustedForwardingPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil || host == "" {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func (g *Guard) lockout(client string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.pruneLocked(now)
	state := g.clients[client]
	if state == nil {
		return 0
	}
	if !state.lockedUntil.IsZero() && !now.Before(state.lockedUntil) {
		delete(g.clients, client)
		return 0
	}
	if now.Before(state.lockedUntil) {
		return state.lockedUntil.Sub(now).Truncate(time.Second) + time.Second
	}
	return 0
}

func (g *Guard) noteFailure(client string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.pruneLocked(now)
	state := g.clients[client]
	if state == nil {
		if len(g.clients) >= maxTrackedClients {
			g.evictOldest(now)
		}
		if len(g.clients) >= maxTrackedClients {
			return 0
		}
		state = &clientState{windowStart: now}
		g.clients[client] = state
	}
	if now.Sub(state.windowStart) > failureWindow {
		state.failures = 0
		state.windowStart = now
		state.lockedUntil = time.Time{}
	}
	state.failures++
	state.lastSeen = now
	if state.failures >= maxFailures {
		state.lockedUntil = now.Add(lockoutDuration)
		return lockoutDuration
	}
	return 0
}

func (g *Guard) noteSuccess(client string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.clients, client)
}

func (g *Guard) pruneLocked(now time.Time) {
	for id, state := range g.clients {
		if now.Before(state.lockedUntil) {
			continue
		}
		expiredLock := !state.lockedUntil.IsZero()
		stale := now.Sub(state.lastSeen) > failureWindow && now.Sub(state.windowStart) > failureWindow
		if expiredLock || stale {
			delete(g.clients, id)
		}
	}
}

func (g *Guard) evictOldest(now time.Time) {
	var oldest string
	var seen time.Time
	for id, state := range g.clients {
		if now.Before(state.lockedUntil) {
			continue
		}
		if oldest == "" || state.lastSeen.Before(seen) {
			oldest = id
			seen = state.lastSeen
		}
	}
	if oldest != "" {
		delete(g.clients, oldest)
	}
}

func writeAuthError(writer http.ResponseWriter, status int, retry time.Duration) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("WWW-Authenticate", "Bearer")
	if retry > 0 {
		writer.Header().Set("Retry-After", fmt.Sprintf("%d", int(retry.Seconds())))
	}
	writer.WriteHeader(status)
	if status == http.StatusTooManyRequests {
		_, _ = writer.Write([]byte(`{"error":"too many failed authentication attempts"}`))
		return
	}
	_, _ = writer.Write([]byte(`{"error":"unauthorized"}`))
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
