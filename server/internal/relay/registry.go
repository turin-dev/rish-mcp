// Package relay holds the device registry, WS handling, and command
// dispatch for Android agents connected to the rish-mcp relay.
package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Kind string

const (
	KindAndroid Kind = "android"
	KindWatch   Kind = "watch"
)

// NormalizeKind coerces anything but "watch" to "android", mirroring the
// original TS relay's behavior for unrecognized `kind` query values.
func NormalizeKind(s string) Kind {
	if Kind(s) == KindWatch {
		return KindWatch
	}
	return KindAndroid
}

var (
	ErrNoDevice        = errors.New("no device is connected to the relay")
	ErrAmbiguousDevice = errors.New("multiple devices connected; pass deviceId")
	ErrDeviceNotFound  = errors.New("device not found")
	ErrTimeout         = errors.New("command timed out")
)

// Result is the outcome of one executed shell command.
type Result struct {
	Code       int
	Stdout     string
	Stderr     string
	Truncated  bool
	DurationMs int64
}

// Device is one connected Android agent (phone, tablet, or watch).
type Device struct {
	ID               string
	Name             string
	SDK              string
	Kind             Kind
	AgentVersion     string
	AgentVersionCode int
	ConnectedAt      time.Time

	mu       sync.Mutex
	lastSeen time.Time
	conn     *websocket.Conn
	writeMu  sync.Mutex // serializes writes to conn (exec frames + pings)
	pending  map[string]chan Result
}

// DeviceInfo is the read-only view returned by list_devices.
type DeviceInfo struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Kind             Kind   `json:"kind"`
	SDK              string `json:"sdk"`
	AgentVersion     string `json:"agentVersion"`
	AgentVersionCode int    `json:"agentVersionCode"`
	ConnectedForMs   int64  `json:"connectedForMs"`
	Pending          int    `json:"pending"`
}

// Registry tracks all devices currently connected to the relay.
type Registry struct {
	mu      sync.RWMutex
	devices map[string]*Device
}

func NewRegistry() *Registry {
	return &Registry{devices: make(map[string]*Device)}
}

func (r *Registry) add(d *Device) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.devices[d.ID] = d
}

func (r *Registry) remove(id string, conn *websocket.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.devices[id]; ok && d.conn == conn {
		delete(r.devices, id)
	}
}

func (r *Registry) get(id string) (*Device, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.devices[id]
	return d, ok
}

// List returns a snapshot of every connected device.
func (r *Registry) List() []DeviceInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]DeviceInfo, 0, len(r.devices))
	now := time.Now()
	for _, d := range r.devices {
		d.mu.Lock()
		pending := len(d.pending)
		d.mu.Unlock()
		out = append(out, DeviceInfo{
			ID:               d.ID,
			Name:             d.Name,
			Kind:             d.Kind,
			SDK:              d.SDK,
			AgentVersion:     d.AgentVersion,
			AgentVersionCode: d.AgentVersionCode,
			ConnectedForMs:   now.Sub(d.ConnectedAt).Milliseconds(),
			Pending:          pending,
		})
	}
	return out
}

// resolve picks the target device: the explicit id, or the sole connected
// device when none was given. Mirrors run_shell's deviceId semantics.
func (r *Registry) resolve(deviceID string) (*Device, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if deviceID != "" {
		d, ok := r.devices[deviceID]
		if !ok {
			return nil, ErrDeviceNotFound
		}
		return d, nil
	}
	switch len(r.devices) {
	case 0:
		return nil, ErrNoDevice
	case 1:
		for _, d := range r.devices {
			return d, nil
		}
	}
	return nil, ErrAmbiguousDevice
}

// Exec runs cmd on the resolved device and blocks until the result arrives,
// the timeout elapses (plus a grace period for the round trip), or ctx is
// cancelled.
func (r *Registry) Exec(ctx context.Context, deviceID, cmd string, timeout time.Duration) (Result, error) {
	d, err := r.resolve(deviceID)
	if err != nil {
		return Result{}, err
	}

	reqID := randomHex(16)
	ch := make(chan Result, 1)
	d.mu.Lock()
	d.pending[reqID] = ch
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.pending, reqID)
		d.mu.Unlock()
	}()

	frame := execFrame{Type: "exec", ReqID: reqID, Cmd: cmd, TimeoutMs: timeout.Milliseconds()}
	d.writeMu.Lock()
	err = d.conn.WriteJSON(frame)
	d.writeMu.Unlock()
	if err != nil {
		return Result{}, err
	}

	const grace = 2 * time.Second
	timer := time.NewTimer(timeout + grace)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res, nil
	case <-timer.C:
		return Result{}, ErrTimeout
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// resolveResult delivers a "result" frame from the device to whichever Exec
// call is waiting on that reqId, if any.
func (r *Registry) resolveResult(deviceID, reqID string, res Result) {
	d, ok := r.get(deviceID)
	if !ok {
		return
	}
	d.mu.Lock()
	ch, ok := d.pending[reqID]
	d.mu.Unlock()
	if ok {
		select {
		case ch <- res:
		default:
		}
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type execFrame struct {
	Type      string `json:"type"`
	ReqID     string `json:"reqId"`
	Cmd       string `json:"cmd"`
	TimeoutMs int64  `json:"timeoutMs"`
}
