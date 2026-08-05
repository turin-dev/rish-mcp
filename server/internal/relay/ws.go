package relay

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// HandleAgent upgrades GET /agent from an Android device (outbound-only,
// token-gated) and keeps it registered until it disconnects.
func HandleAgent(reg *Registry, deviceToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("token") != deviceToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[agent] upgrade failed: %v", err)
			return
		}
		registerAgent(reg, conn, q)
	}
}

func registerAgent(reg *Registry, conn *websocket.Conn, q url.Values) {
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

	d := &Device{
		ID:               deviceID,
		Name:             name,
		SDK:              sdk,
		Kind:             kind,
		AgentVersion:     agentVersion,
		AgentVersionCode: agentVersionCode,
		ConnectedAt:      time.Now(),
		lastSeen:         time.Now(),
		conn:             conn,
		pending:          make(map[string]chan Result),
	}
	reg.add(d)
	log.Printf("[agent] connected: %s (%s, %s, sdk %s, agent %s)", deviceID, kind, name, sdk, agentVersion)

	// Wear OS pings less often to let the radio sleep. A pong (or any frame)
	// refreshes lastSeen; missing ~2.5 ping cycles means the socket is
	// half-open, so terminate it and let the agent redial.
	pingEvery := 25 * time.Second
	if kind == KindWatch {
		pingEvery = 60 * time.Second
	}
	staleAfter := time.Duration(float64(pingEvery) * 2.5)

	done := make(chan struct{})
	var closeOnce sync.Once
	closeConn := func() {
		closeOnce.Do(func() {
			close(done)
			reg.remove(deviceID, conn)
			_ = conn.Close()
			log.Printf("[agent] disconnected: %s", deviceID)
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
					log.Printf("[agent] stale, terminating: %s (no traffic for %s)", deviceID, staleAfter)
					closeConn()
					return
				}
				d.writeMu.Lock()
				err := conn.WriteMessage(websocket.PingMessage, nil)
				d.writeMu.Unlock()
				if err != nil {
					closeConn()
					return
				}
			}
		}
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		d.mu.Lock()
		d.lastSeen = time.Now()
		d.mu.Unlock()

		var msg resultFrame
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.Type == "result" && msg.ReqID != "" {
			reg.resolveResult(deviceID, msg.ReqID, Result{
				Code:       msg.Code,
				Stdout:     msg.Stdout,
				Stderr:     msg.Stderr,
				Truncated:  msg.Truncated,
				DurationMs: msg.DurationMs,
			})
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
}
