package relay

import (
	"crypto/hmac"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const maxReadLimit = 1 * 1024 * 1024 // 1 MB max WebSocket frame size

// AgentConnLimiter caps concurrent WebSocket connections per IP to prevent
// connection-exhaustion attacks. The token gate already authenticates, but a
// leaked token or buggy device could still open many connections.
type AgentConnLimiter struct {
	mu     sync.Mutex
	active map[string]int
}

const maxConnsPerIP = 10

// NewAgentConnLimiter returns an empty limiter (zero active connections).
func NewAgentConnLimiter() *AgentConnLimiter {
	return &AgentConnLimiter{active: make(map[string]int)}
}

func (l *AgentConnLimiter) acquire(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.active[ip]
	if n >= maxConnsPerIP {
		return false
	}
	l.active[ip] = n + 1
	return true
}

func (l *AgentConnLimiter) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.active[ip]
	if n <= 1 {
		delete(l.active, ip)
	} else {
		l.active[ip] = n - 1
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		// The /agent endpoint is designed for Android agents (non-browser
		// clients) that don't send an Origin header. Accept empty origins.
		// When origin is present, only accept same-origin requests to prevent
		// CSWSH attacks from malicious websites. A proxy or intermediary may
		// add an Origin header, so we match against the request's Host rather
		// than blindly rejecting all non-empty origins.
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return u.Host == r.Host
	},
}

// HandleAgent upgrades GET /agent from an Android device (outbound-only,
// token-gated) and keeps it registered until it disconnects.
func HandleAgent(reg *Registry, deviceToken string, limiter *AgentConnLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if !hmac.Equal([]byte(q.Get("token")), []byte(deviceToken)) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !limiter.acquire(clientIP(r)) {
			log.Printf("[agent] too many connections from %s", clientIP(r))
			http.Error(w, "too many connections", http.StatusTooManyRequests)
			return
		}
		defer limiter.release(clientIP(r))
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[agent] upgrade failed: %v", err)
			return
		}
		// Cap the frame size so a buggy or hostile agent can't balloon memory:
		// gorilla/websocket's default read limit is effectively unbounded.
		conn.SetReadLimit(maxReadLimit)
		registerAgent(reg, conn, q)
	}
}

// clientIP returns the remote address without the port, ignoring X-Forwarded-For:
// trusting that header here would let a client spoof its identity to the
// per-IP connection limiter.
func clientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// maxLogFieldLen truncates attacker-influenced metadata (device name, agent
// version, …) before it reaches log.Printf. Together with sanitizeLogField it
// keeps a malicious agent from forging fake log lines or flooding the log:
// values are cut to a readable length and control characters removed.
const maxLogFieldLen = 64

// sanitizeLogField strips CR, LF, NUL and other control characters so an
// agent-controlled query param can't inject fake log lines.
func sanitizeLogField(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s) && b.Len() < maxLogFieldLen; i++ {
		c := s[i]
		if c >= 0x20 || c == '\t' {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// sendPing sends a WebSocket ping frame to the agent. Extracted so tests can
// observe the write path deterministically (the ticker goroutine in
// registerAgentPing has a race between a real ping and a test-controlled close).
func sendPing(conn *websocket.Conn, writeMu *sync.Mutex) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	return conn.WriteMessage(websocket.PingMessage, nil)
}

func registerAgent(reg *Registry, conn *websocket.Conn, q url.Values) {
	registerAgentPing(reg, conn, q, pingIntervalFor(NormalizeKind(q.Get("kind"))))
}

// pingIntervalFor returns how often a device of the given kind should be
// pinged. Wear OS pings less often to let the radio sleep.
func pingIntervalFor(kind Kind) time.Duration {
	if kind == KindWatch {
		return 60 * time.Second
	}
	return 25 * time.Second
}

func registerAgentPing(reg *Registry, conn *websocket.Conn, q url.Values, pingEvery time.Duration) {
	deviceID := q.Get("deviceId")
	if deviceID == "" {
		deviceID = randomHex(8)
	}
	name := q.Get("name")
	if name == "" {
		name = "Android"
	}
	sdk := q.Get("sdk")
	if sdk == "" {
		sdk = "?"
	}
	kind := NormalizeKind(q.Get("kind"))
	agentVersion := q.Get("ver")
	if agentVersion == "" {
		agentVersion = "unknown"
	}
	agentVersionCode, _ := strconv.Atoi(q.Get("vc"))
	shellBackend := normalizeShellBackend(q.Get("backend"))

	// The raw values are used for routing (deviceID is the registry key), but
	// anything that reaches a log line goes through sanitizeLogField first.
	logDeviceID, logName, logSDK, logVersion := sanitizeLogField(deviceID), sanitizeLogField(name), sanitizeLogField(sdk), sanitizeLogField(agentVersion)

	d := &Device{
		ID:               deviceID,
		Name:             name,
		SDK:              sdk,
		Kind:             kind,
		AgentVersion:     agentVersion,
		AgentVersionCode: agentVersionCode,
		ShellBackend:     shellBackend,
		ConnectedAt:      time.Now(),
		lastSeen:         time.Now(),
		conn:             conn,
		pending:          make(map[string]chan pendingResult),
	}
	if old := reg.add(d); old != nil && old != d {
		log.Printf("[agent] connection replaced for %s (device reconnected)", logDeviceID)
		oldConn := old.conn
		reg.disconnect(old, ErrDeviceReplaced)
		if oldConn != nil {
			_ = oldConn.Close()
		}
	}
	log.Printf("[agent] connected: %s (%s, %s, sdk %s, agent %s)", logDeviceID, kind, logName, logSDK, logVersion)

	// A pong (or any frame) refreshes lastSeen; missing ~2.5 ping cycles means
	// the socket is half-open, so terminate it and let the agent redial.
	staleAfter := time.Duration(float64(pingEvery) * 2.5)

	done := make(chan struct{})
	var closeOnce sync.Once
	closeConn := func() {
		closeOnce.Do(func() {
			close(done)
			reg.disconnect(d, ErrDeviceDisconnected)
			_ = conn.Close()
			log.Printf("[agent] disconnected: %s", logDeviceID)
		})
	}
	defer closeConn()

	conn.SetPongHandler(func(string) error {
		d.mu.Lock()
		d.lastSeen = time.Now()
		d.mu.Unlock()
		return nil
	})

	go func() {
		ticker := time.NewTicker(pingEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				d.mu.Lock()
				stale := time.Since(d.lastSeen) > staleAfter
				d.mu.Unlock()
				if stale {
					log.Printf("[agent] stale, terminating: %s (no traffic for %s)", logDeviceID, staleAfter)
					closeConn()
					return
				}
				if err := sendPing(conn, &d.writeMu); err != nil {
					log.Printf("[agent] ping failed for %s: %v", logDeviceID, err)
					closeConn()
					return
				}
			}
		}
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[agent] read failed for %s: %v", logDeviceID, err)
			return
		}
		d.mu.Lock()
		d.lastSeen = time.Now()
		d.mu.Unlock()

		var msg resultFrame
		if err := json.Unmarshal(raw, &msg); err != nil {
			const maxLoggedFrame = 256
			frame := string(raw)
			if len(frame) > maxLoggedFrame {
				frame = frame[:maxLoggedFrame] + "..."
			}
			log.Printf("[agent] invalid result frame from %s: %v (frame=%q)", logDeviceID, err, frame)
			continue
		}
		switch {
		case msg.Type == "result" && msg.ReqID != "":
			reg.resolveResult(deviceID, msg.ReqID, Result{
				Code:       msg.Code,
				Stdout:     msg.Stdout,
				Stderr:     msg.Stderr,
				Truncated:  msg.Truncated,
				DurationMs: msg.DurationMs,
			})
		case msg.Type == "status":
			d.mu.Lock()
			d.ShellBackend = normalizeShellBackend(msg.Backend)
			d.mu.Unlock()
		}
	}
}

type resultFrame struct {
	Type       string `json:"type"`
	ReqID      string `json:"reqId"`
	Code       int    `json:"code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Truncated  bool   `json:"truncated"`
	DurationMs int64  `json:"durationMs"`
	Backend    string `json:"backend"`
}

func normalizeShellBackend(value string) string {
	if value == "shizuku" || value == "adb" {
		return value
	}
	return "unknown"
}
