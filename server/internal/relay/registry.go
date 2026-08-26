// Package relay holds the device registry, WS handling, and command
// dispatch for Android agents connected to the rish-mcp relay.
package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
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
	ErrNoDevice           = errors.New("no device is connected to the relay")
	ErrAmbiguousDevice    = errors.New("multiple devices connected; pass deviceId")
	ErrDeviceNotFound     = errors.New("device not found")
	ErrDeviceDisconnected = errors.New("device disconnected while command was running")
	ErrDeviceReplaced     = errors.New("device connection was replaced")
	ErrTimeout            = errors.New("command timed out")
	ErrTooManyPending     = errors.New("too many pending commands on device")
)

// maxPendingPerDevice is the maximum number of concurrent commands allowed to
// queue on a single device. Without this cap, a malicious or buggy caller
// could issue thousands of concurrent Exec() calls, each adding a channel to
// the pending map. The channels are 1-buffered, so each entry is ~200 bytes;
// even at 1000 entries that's only ~200 KB of memory, but the unbounded map
// growth is still a DoS risk. 32 is a generous limit — a real device handles
// one command at a time anyway.
const maxPendingPerDevice = 32

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
	ShellBackend     string
	ConnectedAt      time.Time

	mu       sync.Mutex
	lastSeen time.Time
	conn     *websocket.Conn
	writeMu  sync.Mutex // serializes writes to conn (exec frames + pings)
	pending  map[string]chan pendingResult
}

type pendingResult struct {
	result Result
	err    error
}

// DeviceInfo is the read-only view returned by list_devices.
type DeviceInfo struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Kind             Kind   `json:"kind"`
	SDK              string `json:"sdk"`
	AgentVersion     string `json:"agentVersion"`
	AgentVersionCode int    `json:"agentVersionCode"`
	ShellBackend     string `json:"shellBackend"`
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

// add installs d as the current owner of its device ID and returns the
// connection it replaced, if any. The caller owns closing the old connection.
func (r *Registry) add(d *Device) *Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.devices[d.ID]
	r.devices[d.ID] = d
	return old
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
		shellBackend := d.ShellBackend
		d.mu.Unlock()
		out = append(out, DeviceInfo{
			ID:               d.ID,
			Name:             d.Name,
			Kind:             d.Kind,
			SDK:              d.SDK,
			AgentVersion:     d.AgentVersion,
			AgentVersionCode: d.AgentVersionCode,
			ShellBackend:     shellBackend,
			ConnectedForMs:   now.Sub(d.ConnectedAt).Milliseconds(),
			Pending:          pending,
		})
	}
	return out
}

// resolve picks the target device: the explicit id, or the sole connected
// device when none was given. Mirrors run_shell's deviceId semantics.
// disconnect removes the device only when d is still the registered
// connection, then fails all commands waiting on that connection.
func (r *Registry) disconnect(d *Device, cause error) {
	r.mu.Lock()
	if current, ok := r.devices[d.ID]; ok && current == d {
		delete(r.devices, d.ID)
	}
	r.mu.Unlock()

	d.mu.Lock()
	pending := d.pending
	d.pending = make(map[string]chan pendingResult)
	d.conn = nil
	d.mu.Unlock()
	for _, ch := range pending {
		// A result may already be buffered when the connection drops. Do not
		// block disconnect cleanup on a value that the timed-out caller may
		// never receive; the pending command is being removed regardless.
		select {
		case ch <- pendingResult{err: cause}:
		default:
		}
	}
}

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
	ch := make(chan pendingResult, 1)
	d.mu.Lock()
	if d.conn == nil {
		d.mu.Unlock()
		return Result{}, ErrDeviceDisconnected
	}
	if len(d.pending) >= maxPendingPerDevice {
		d.mu.Unlock()
		return Result{}, ErrTooManyPending
	}
	d.pending[reqID] = ch
	conn := d.conn
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.pending, reqID)
		d.mu.Unlock()
	}()

	frame := execFrame{Type: "exec", ReqID: reqID, Cmd: cmd, TimeoutMs: timeout.Milliseconds()}
	d.writeMu.Lock()
	err = conn.WriteJSON(frame)
	d.writeMu.Unlock()
	if err != nil {
		return Result{}, fmt.Errorf("send command to device %q: %w", d.ID, err)
	}

	const grace = 2 * time.Second
	timer := time.NewTimer(timeout + grace)
	defer timer.Stop()
	select {
	case outcome := <-ch:
		return outcome.result, outcome.err
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
		case ch <- pendingResult{result: res}:
		default:
			// Buffered slot already taken (a disconnect error, or a result
			// raced with a previous frame for the same reqId) — the waiting
			// caller already got its answer; log so late results aren't
			// silently dropped.
			log.Printf("[registry] dropped late result for reqId %s (device %s)", reqID, deviceID)
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
