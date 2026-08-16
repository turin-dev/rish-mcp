package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestNewServer(t *testing.T) {
	s := NewServer("test-server", "1.0.0")
	if s.Name != "test-server" {
		t.Fatalf("expected Name 'test-server', got %q", s.Name)
	}
	if s.Version != "1.0.0" {
		t.Fatalf("expected Version '1.0.0', got %q", s.Version)
	}
	if len(s.tools) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(s.tools))
	}
}

func TestRegisterTool(t *testing.T) {
	s := NewServer("test", "1.0")
	s.RegisterTool(Tool{Name: "greet"})
	s.RegisterTool(Tool{Name: "echo"})
	if len(s.tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(s.tools))
	}
	if s.tools[0].Name != "greet" {
		t.Fatalf("expected tool[0] name 'greet', got %q", s.tools[0].Name)
	}
	if s.tools[1].Name != "echo" {
		t.Fatalf("expected tool[1] name 'echo', got %q", s.tools[1].Name)
	}
}

func TestHandleInitialize(t *testing.T) {
	s := NewServer("rish-relay", "2.0.1")
	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	})
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatal("expected Result to be map[string]any")
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Fatalf("expected protocolVersion 2024-11-05, got %v", result["protocolVersion"])
	}
	capabs, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatal("expected capabilities to be map[string]any")
	}
	if _, ok := capabs["tools"]; !ok {
		t.Fatal("expected capabilities.tools")
	}
	si, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatal("expected serverInfo to be map[string]any")
	}
	if si["name"] != "rish-relay" || si["version"] != "2.0.1" {
		t.Fatalf("unexpected serverInfo: %v", si)
	}
}

func TestHandleInitializePreservesID(t *testing.T) {
	s := NewServer("x", "1")
	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"req-42"`),
		Method:  "initialize",
	})
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	var id string
	if err := json.Unmarshal(resp.ID, &id); err != nil {
		t.Fatalf("expected ID \"req-42\", got %s", string(resp.ID))
	}
	if id != "req-42" {
		t.Fatalf("expected ID \"req-42\", got %q", id)
	}
}

func TestHandleNotificationsReturnNil(t *testing.T) {
	s := NewServer("x", "1")
	cases := []struct {
		name   string
		method string
	}{
		{"initialized", "notifications/initialized"},
		{"cancelled", "notifications/cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := s.Handle(context.Background(), Request{
				JSONRPC: "2.0",
				Method:  tc.method,
			})
			if resp != nil {
				t.Fatal("expected nil response for notification")
			}
		})
	}
}

func TestHandleToolsList(t *testing.T) {
	s := NewServer("test", "1")
	s.RegisterTool(Tool{
		Name:        "echo",
		Description: "Echoes input back",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"msg": {Type: "string", Description: "message to echo"},
			},
			Required: []string{"msg"},
		},
	})
	s.RegisterTool(Tool{
		Name:        "ping",
		Description: "Returns pong",
		InputSchema: InputSchema{
			Type:       "object",
			Properties: map[string]Property{},
		},
	})

	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
	})
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatal("expected Result to be map[string]any")
	}
	toolsRaw, ok := result["tools"]
	if !ok {
		t.Fatal("expected tools key in result")
	}
	toolsJSON, err := json.Marshal(toolsRaw)
	if err != nil {
		t.Fatalf("marshal tools: %v", err)
	}
	var defs []toolDef
	if err := json.Unmarshal(toolsJSON, &defs); err != nil {
		t.Fatalf("unmarshal tools: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(defs))
	}
	if defs[0].Name != "echo" {
		t.Fatalf("expected tool[0] name 'echo', got %q", defs[0].Name)
	}
	if defs[1].Name != "ping" {
		t.Fatalf("expected tool[1] name 'ping', got %q", defs[1].Name)
	}
}

func TestHandleToolsListEmpty(t *testing.T) {
	s := NewServer("test", "1")
	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
	})
	result := resp.Result.(map[string]any)
	tools := result["tools"].([]toolDef)
	if len(tools) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(tools))
	}
}

func TestHandleMethodNotFound(t *testing.T) {
	s := NewServer("test", "1")
	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "unknown",
	})
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32601 {
		t.Fatalf("expected error code -32601, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "method not found" {
		t.Fatalf("expected 'method not found', got %q", resp.Error.Message)
	}
	if resp.Result != nil {
		t.Fatal("expected nil Result on error")
	}
}

func TestHandleToolsCallSuccess(t *testing.T) {
	s := NewServer("test", "1")
	s.RegisterTool(Tool{
		Name: "greet",
		Handler: func(ctx context.Context, args json.RawMessage) (CallResult, error) {
			var p struct{ Name string `json:"name"` }
			json.Unmarshal(args, &p)
			return CallResult{Content: []ContentItem{{Type: "text", Text: "hello " + p.Name}}}, nil
		},
	})
	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"greet","arguments":{"name":"world"}}`),
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	cr, ok := resp.Result.(CallResult)
	if !ok {
		t.Fatal("expected Result to be CallResult")
	}
	if len(cr.Content) != 1 || cr.Content[0].Text != "hello world" {
		t.Fatalf("unexpected content: %+v", cr.Content)
	}
	if cr.IsError {
		t.Fatal("expected IsError to be false")
	}
}

func TestHandleToolsCallWithoutArguments(t *testing.T) {
	s := NewServer("test", "1")
	var receivedArgs json.RawMessage
	s.RegisterTool(Tool{
		Name: "noop",
		Handler: func(ctx context.Context, args json.RawMessage) (CallResult, error) {
			receivedArgs = args
			return CallResult{Content: []ContentItem{{Type: "text", Text: "ok"}}}, nil
		},
	})
	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"noop"}`),
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if receivedArgs != nil {
		t.Fatalf("expected nil arguments, got %s", string(receivedArgs))
	}
}

func TestHandleToolsCallHandlerError(t *testing.T) {
	s := NewServer("test", "1")
	s.RegisterTool(Tool{
		Name: "fail",
		Handler: func(ctx context.Context, args json.RawMessage) (CallResult, error) {
			return CallResult{}, errors.New("something went wrong")
		},
	})
	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"fail","arguments":{}}`),
	})
	if resp.Error != nil {
		t.Fatalf("handler error should be wrapped in Result, not top-level Error: %+v", resp.Error)
	}
	cr, ok := resp.Result.(CallResult)
	if !ok {
		t.Fatal("expected Result to be CallResult")
	}
	if !cr.IsError {
		t.Fatal("expected IsError to be true")
	}
	if len(cr.Content) != 1 || cr.Content[0].Text != "error: something went wrong" {
		t.Fatalf("unexpected error content: %+v", cr.Content)
	}
}

func TestHandleToolsCallInvalidParams(t *testing.T) {
	s := NewServer("test", "1")
	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`invalid json`),
	})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32602 {
		t.Fatalf("expected error code -32602, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "invalid params" {
		t.Fatalf("expected 'invalid params', got %q", resp.Error.Message)
	}
}

func TestHandleToolsCallUnknownTool(t *testing.T) {
	s := NewServer("test", "1")
	s.RegisterTool(Tool{Name: "known"})
	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"unknown","arguments":{}}`),
	})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != -32602 {
		t.Fatalf("expected error code -32602, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "unknown tool: unknown" {
		t.Fatalf("expected 'unknown tool: unknown', got %q", resp.Error.Message)
	}
}

func TestHandleToolsCallParamsNil(t *testing.T) {
	s := NewServer("test", "1")
	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
	})
	if resp.Error == nil {
		t.Fatal("expected error when params is nil")
	}
	if resp.Error.Code != -32602 {
		t.Fatalf("expected error code -32602, got %d", resp.Error.Code)
	}
}

func TestHandleContextCancellation(t *testing.T) {
	s := NewServer("test", "1")
	called := false
	s.RegisterTool(Tool{
		Name: "slow",
		Handler: func(ctx context.Context, args json.RawMessage) (CallResult, error) {
			called = true
			return CallResult{Content: []ContentItem{{Type: "text", Text: "done"}}}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	resp := s.Handle(ctx, Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"slow","arguments":{}}`),
	})
	// The handler is called regardless; the tool itself decides whether to
	// check ctx.Err(). Since our test handler doesn't check, it succeeds.
	// This test verifies that context cancellation is passed through without
	// the framework itself dropping the request.
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if !called {
		t.Fatal("expected handler to be called even with cancelled context")
	}
}

func TestHandleCallWithMultipleTools(t *testing.T) {
	s := NewServer("test", "1")
	s.RegisterTool(Tool{
		Name: "alpha",
		Handler: func(ctx context.Context, args json.RawMessage) (CallResult, error) {
			return CallResult{Content: []ContentItem{{Type: "text", Text: "alpha"}}}, nil
		},
	})
	s.RegisterTool(Tool{
		Name: "beta",
		Handler: func(ctx context.Context, args json.RawMessage) (CallResult, error) {
			return CallResult{Content: []ContentItem{{Type: "text", Text: "beta"}}}, nil
		},
	})
	// Call beta, verify alpha's handler is not invoked
	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"beta","arguments":{}}`),
	})
	cr := resp.Result.(CallResult)
	if cr.Content[0].Text != "beta" {
		t.Fatalf("expected 'beta', got %q", cr.Content[0].Text)
	}
}

func TestToolRegistrationPreservesOrder(t *testing.T) {
	s := NewServer("test", "1")
	names := []string{"first", "second", "third"}
	for _, n := range names {
		s.RegisterTool(Tool{Name: n})
	}
	for i, n := range names {
		if s.tools[i].Name != n {
			t.Fatalf("at index %d: expected %q, got %q", i, n, s.tools[i].Name)
		}
	}
}

func TestHandleCallToolWithNestedArgs(t *testing.T) {
	s := NewServer("test", "1")
	s.RegisterTool(Tool{
		Name: "config",
		Handler: func(ctx context.Context, args json.RawMessage) (CallResult, error) {
			var cfg struct {
				Nested struct {
					Key string `json:"key"`
				} `json:"nested"`
			}
			json.Unmarshal(args, &cfg)
			return CallResult{Content: []ContentItem{{Type: "text", Text: cfg.Nested.Key}}}, nil
		},
	})
	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"config","arguments":{"nested":{"key":"deep"}}}`),
	})
	cr := resp.Result.(CallResult)
	if cr.Content[0].Text != "deep" {
		t.Fatalf("expected 'deep', got %q", cr.Content[0].Text)
	}
}

func TestResponseJSONRoundTrip(t *testing.T) {
	// Verify that the response serializes correctly to JSON.
	s := NewServer("test", "1")
	s.RegisterTool(Tool{
		Name: "echo",
		Handler: func(ctx context.Context, args json.RawMessage) (CallResult, error) {
			return CallResult{Content: []ContentItem{{Type: "text", Text: "hi"}}}, nil
		},
	})
	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"echo","arguments":{"msg":"hi"}}`),
	})
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var out struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.JSONRPC != "2.0" {
		t.Fatalf("expected jsonrpc 2.0, got %q", out.JSONRPC)
	}
	var id int
	json.Unmarshal(out.ID, &id)
	if id != 1 {
		t.Fatalf("expected id 1, got %d", id)
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "hi" {
		t.Fatalf("unexpected result content: %+v", result.Content)
	}
}

func TestErrorResponseJSONRoundTrip(t *testing.T) {
	s := NewServer("test", "1")
	resp := s.Handle(context.Background(), Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "bogus",
	})
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal error response: %v", err)
	}
	var out struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if out.Error.Code != -32601 {
		t.Fatalf("expected error code -32601, got %d", out.Error.Code)
	}
	if out.Error.Message != "method not found" {
		t.Fatalf("expected 'method not found', got %q", out.Error.Message)
	}
}