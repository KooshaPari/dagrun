// Package mcp serves a dagrun run over the Model Context Protocol.
//
// Transport is JSON-RPC 2.0, one object per line, over stdio. stdout carries
// protocol frames and nothing else: every human-facing message goes to the log
// writer (stderr in the CLI), because one stray fmt.Println would corrupt the
// stream and the client would see a parse error where an answer should be.
//
// The tools are thin wrappers over internal/plan, internal/store, and
// internal/verify. They never call the cmdXxx printers in package main, because
// those write to os.Stdout by design.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// ProtocolVersion is advertised when the client does not name one.
	ProtocolVersion = "2024-11-05"
	// DefaultVersion is used when the caller passes no build version.
	DefaultVersion = "0.3.0"

	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// Config is what a caller can tune. Zero values are safe.
type Config struct {
	// Version is the dagrun build version reported in serverInfo.
	Version string
	// Log receives diagnostics. Never stdout: that stream belongs to the
	// protocol. Defaults to io.Discard.
	Log io.Writer
}

// Server answers JSON-RPC requests by reading one object per line.
type Server struct {
	version string
	log     io.Writer
	out     io.Writer
}

// New builds a server. It holds no run state: every tool call opens the run
// directory named in its arguments, so several runs can be served at once.
func New(cfg Config) *Server {
	if cfg.Version == "" {
		cfg.Version = DefaultVersion
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	return &Server{version: cfg.Version, log: cfg.Log}
}

// Version is the version reported in serverInfo.
func (s *Server) Version() string { return s.version }

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve reads newline-delimited JSON-RPC from in and writes one response line
// per request to out. It returns nil at EOF, so a client that closes stdin sees
// a clean exit rather than an error.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	br := bufio.NewReaderSize(in, 64*1024)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			s.handleLine(ctx, line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
	}
}

func (s *Server) handleLine(ctx context.Context, line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}
	var req request
	if err := json.Unmarshal([]byte(trimmed), &req); err != nil {
		s.logf("parse error: %v", err)
		s.writeError(nil, codeParse, "parse error: "+err.Error())
		return
	}
	if len(req.ID) == 0 {
		// Notification: JSON-RPC forbids a reply. Act on the ones we know.
		s.handleNotification(req)
		return
	}
	s.dispatch(ctx, req)
}

// handleNotification accepts the lifecycle notifications a client sends. None
// of them need a response, and an unknown one is ignored rather than failed.
func (s *Server) handleNotification(req request) {
	switch req.Method {
	case "notifications/initialized", "initialized":
		s.logf("client initialized")
	case "notifications/cancelled", "notifications/progress":
		// nothing to do: tool calls are synchronous
	default:
		s.logf("ignoring notification %q", req.Method)
	}
}

func (s *Server) dispatch(ctx context.Context, req request) {
	switch req.Method {
	case "initialize":
		s.writeResult(req.ID, s.initialize(req.Params))
	case "ping":
		s.writeResult(req.ID, struct{}{})
	case "tools/list":
		s.writeResult(req.ID, map[string]any{"tools": toolList()})
	case "tools/call":
		s.handleToolsCall(ctx, req)
	default:
		s.writeError(req.ID, codeMethodNotFound, fmt.Sprintf("method not found: %s", req.Method))
	}
}

// initialize echoes the client's protocol version when it names one, so a newer
// client is not downgraded and an older one is not confused.
func (s *Server) initialize(params json.RawMessage) map[string]any {
	version := ProtocolVersion
	if len(params) > 0 {
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if err := json.Unmarshal(params, &p); err == nil && strings.TrimSpace(p.ProtocolVersion) != "" {
			version = p.ProtocolVersion
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    "dagrun",
			"version": s.version,
		},
	}
}

func (s *Server) handleToolsCall(ctx context.Context, req request) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			s.writeError(req.ID, codeInvalidParams, "invalid params: "+err.Error())
			return
		}
	}
	if strings.TrimSpace(params.Name) == "" {
		s.writeError(req.ID, codeInvalidParams, "invalid params: missing tool name")
		return
	}
	payload, isError, err := s.callTool(ctx, params.Name, params.Arguments)
	if err != nil {
		s.writeError(req.ID, codeInvalidParams, err.Error())
		return
	}
	s.writeResult(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": encodeText(payload)}},
		"isError": isError,
	})
}

// encodeText renders a tool payload as the text block MCP requires. Strings are
// passed through; everything else is indented JSON so a model can read it.
func encodeText(payload any) string {
	switch v := payload.(type) {
	case nil:
		return "{}"
	case string:
		return v
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", payload)
	}
	return string(raw)
}

func (s *Server) writeResult(id json.RawMessage, result any) {
	s.write(response{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) writeError(id json.RawMessage, code int, message string) {
	s.write(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

// write emits exactly one line per frame in a single Write, so two messages can
// never interleave.
func (s *Server) write(resp response) {
	raw, err := json.Marshal(resp)
	if err != nil {
		s.logf("marshal response: %v", err)
		return
	}
	raw = append(raw, '\n')
	if _, err := s.out.Write(raw); err != nil {
		s.logf("write response: %v", err)
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.log == nil {
		return
	}
	fmt.Fprintf(s.log, "dagrun mcp: "+format+"\n", args...)
}
