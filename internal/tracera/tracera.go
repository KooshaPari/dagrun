// Package tracera posts a run to a Tracera instance.
//
// Tracera's ingest is a bulk endpoint: it takes {"items":[...]} and answers
// with a report, or with HTTP 422 plus an error entry when the payload is
// unusable. The contract that matters here is fail-loud: a non-2xx status is an
// error, and a 2xx whose report carries a non-empty error list is *also* an
// error. This client never reports success on a response that reported failure.
package tracera

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/KooshaPari/dagrun/internal/export"
	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
)

// IngestPath is the bulk ingest route Tracera exposes.
const IngestPath = "/ingest/agileplus"

// DefaultTimeout is how long one ingest request may take.
const DefaultTimeout = 30 * time.Second

// maxBody caps how much of a response body is quoted in an error message.
const maxBody = 4 << 10

// Formats accepted by BuildPayload.
const (
	FormatTracera   = "tracera"
	FormatAgilePlus = "agileplus"
)

// Client posts a run to a Tracera instance.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New builds a client. A missing base URL is an error, because a client that
// silently points nowhere turns a wiring mistake into a false success.
func New(baseURL, token string) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil, errors.New("tracera: base URL is required")
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return nil, fmt.Errorf("tracera: base URL %q must start with http:// or https://", base)
	}
	return &Client{BaseURL: base, Token: token, HTTP: &http.Client{Timeout: DefaultTimeout}}, nil
}

// Payload is the body of an ingest request.
type Payload struct {
	Items []Item `json:"items"`
}

// Item is one unit in an ingest request. The tracera format emits one item per
// node, edge, and artifact; the agileplus format emits a single item whose
// Bundle field carries the ImportBundle verbatim.
type Item struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Title    string `json:"title,omitempty"`
	Status   string `json:"status,omitempty"`
	Type     string `json:"type,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Path     string `json:"path,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	Verified bool   `json:"verified,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Estimate string `json:"estimate,omitempty"`
	Role     string `json:"role,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
	LastErr  string `json:"last_error,omitempty"`
	Started  string `json:"started_at,omitempty"`
	Finished string `json:"finished_at,omitempty"`
	// Bundle carries a whole document (the AgilePlus ImportBundle) verbatim.
	Bundle json.RawMessage `json:"bundle,omitempty"`
}

// Result is the server's report.
type Result struct {
	TotalProcessed      int      `json:"total_processed"`
	RequirementsCreated int      `json:"requirements_created"`
	TraceLinksCreated   int      `json:"trace_links_created"`
	Errors              []string `json:"errors"`
}

// trDoc mirrors the export package's relational-tier document. It is a copy
// rather than a shared type because that type is unexported, and re-deriving
// the shape here would let the two drift.
type trDoc struct {
	Nodes     []trNode     `json:"nodes"`
	Edges     []trEdge     `json:"edges"`
	Artifacts []trArtifact `json:"artifacts"`
}

type trNode struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	Estimate   string `json:"estimate"`
	Role       string `json:"role"`
	Attempts   int    `json:"attempts"`
	LastError  string `json:"last_error"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

type trEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
}

type trArtifact struct {
	Node     string `json:"node"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
	Verified bool   `json:"verified"`
	Detail   string `json:"detail"`
}

// BuildPayload turns a run into the ingest payload. Ids are namespaced by item
// class so a node, an edge, and an artifact can never collide in one bulk load.
func BuildPayload(p *plan.Plan, st *store.State, format string) (Payload, error) {
	if p == nil {
		return Payload{}, errors.New("tracera: nil plan")
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", FormatTracera:
		return buildTracera(p, st)
	case FormatAgilePlus, "agile-plus", "ap":
		return buildAgilePlus(p, st)
	default:
		return Payload{}, fmt.Errorf("tracera: unknown format %q; want %s or %s", format, FormatTracera, FormatAgilePlus)
	}
}

func buildTracera(p *plan.Plan, st *store.State) (Payload, error) {
	raw, err := export.Tracera(export.Input{Plan: p, State: st})
	if err != nil {
		return Payload{}, err
	}
	var doc trDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Payload{}, fmt.Errorf("tracera: decode export: %w", err)
	}

	out := Payload{Items: make([]Item, 0, len(doc.Nodes)+len(doc.Edges)+len(doc.Artifacts))}
	for _, n := range doc.Nodes {
		out.Items = append(out.Items, Item{
			ID: "node:" + n.ID, Kind: "node", Title: n.Label, Status: n.Status,
			Type: n.Kind, Estimate: n.Estimate, Role: n.Role, Attempts: n.Attempts,
			LastErr: n.LastError, Started: n.StartedAt, Finished: n.FinishedAt,
		})
	}
	for _, e := range doc.Edges {
		typ := e.Type
		if typ == "" {
			typ = "depends_on"
		}
		out.Items = append(out.Items, Item{
			ID: "edge:" + e.From + "->" + e.To, Kind: "edge", Type: typ,
			From: e.From, To: e.To, Title: e.From + " -> " + e.To,
		})
	}
	for _, a := range doc.Artifacts {
		out.Items = append(out.Items, Item{
			ID: "artifact:" + a.Node + ":" + a.Path, Kind: "artifact",
			Title: a.Path, Path: a.Path, SHA256: a.SHA256, Bytes: a.Bytes,
			Verified: a.Verified, Detail: a.Detail, From: a.Node,
		})
	}
	return out, nil
}

func buildAgilePlus(p *plan.Plan, st *store.State) (Payload, error) {
	raw, err := export.AgilePlus(export.Input{Plan: p, State: st})
	if err != nil {
		return Payload{}, err
	}
	if !json.Valid(raw) {
		return Payload{}, errors.New("tracera: agileplus export is not valid JSON")
	}
	name := p.Run
	if name == "" {
		name = "dagrun"
	}
	return Payload{Items: []Item{{
		ID:     "agileplus:" + name,
		Kind:   "agileplus-import-bundle",
		Title:  name,
		Bundle: json.RawMessage(raw),
	}}}, nil
}

// Send posts the payload and returns the server's report. Every failure path
// returns an error; a response that reports errors is a failure even at 2xx.
func (c *Client) Send(ctx context.Context, p Payload) (Result, error) {
	if c == nil {
		return Result{}, errors.New("tracera: nil client")
	}
	if c.BaseURL == "" {
		return Result{}, errors.New("tracera: base URL is required")
	}
	body, err := json.Marshal(p)
	if err != nil {
		return Result{}, fmt.Errorf("tracera: encode payload: %w", err)
	}
	url := c.BaseURL + IngestPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, c.redactErr(fmt.Errorf("tracera: build request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		// url and error text are redacted: the token lives in a header, but a
		// URL is sometimes pasted with credentials into --url.
		return Result{}, c.redactErr(fmt.Errorf("tracera: post %s: %w", url, err))
	}
	defer resp.Body.Close()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if readErr != nil {
		return Result{}, c.redactErr(fmt.Errorf("tracera: read response from %s: %w", url, readErr))
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{}, c.failStatus(url, resp.StatusCode, raw)
	}
	return c.parseReport(url, resp.StatusCode, raw)
}

// failStatus renders a non-2xx response. The server's own message is quoted so
// an operator sees why Tracera rejected the payload.
func (c *Client) failStatus(url string, status int, raw []byte) error {
	detail := strings.TrimSpace(string(raw))
	var r Result
	msgs := []string{}
	if json.Unmarshal(raw, &r) == nil && len(r.Errors) > 0 {
		msgs = r.Errors
	}
	msg := fmt.Sprintf("tracera: %s returned HTTP %d", url, status)
	if len(msgs) > 0 {
		msg += ": " + strings.Join(msgs, "; ")
	} else if detail != "" {
		msg += ": " + detail
	}
	return c.redactErr(errors.New(msg))
}

// parseReport decodes a 2xx report. A non-JSON body is an error, and a report
// listing errors is an error: 2xx alone does not prove the items landed.
func (c *Client) parseReport(url string, status int, raw []byte) (Result, error) {
	var r Result
	if err := json.Unmarshal(raw, &r); err != nil {
		body := strings.TrimSpace(string(raw))
		if body == "" {
			body = "(empty body)"
		}
		return Result{}, c.redactErr(fmt.Errorf("tracera: %s returned HTTP %d with a non-JSON body: %s", url, status, body))
	}
	if len(r.Errors) > 0 {
		return r, c.redactErr(fmt.Errorf("tracera: %s reported %d ingest error(s): %s",
			url, len(r.Errors), strings.Join(r.Errors, "; ")))
	}
	return r, nil
}

// redactErr removes the token from any message it renders.
func (c *Client) redactErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if c != nil && c.Token != "" {
		msg = strings.ReplaceAll(msg, c.Token, "[redacted]")
	}
	return errors.New(msg)
}

// UnmarshalJSON accepts Tracera's error list in either shape: plain strings, or
// objects carrying a message/detail field.
func (r *Result) UnmarshalJSON(data []byte) error {
	var wire struct {
		TotalProcessed      int               `json:"total_processed"`
		RequirementsCreated int               `json:"requirements_created"`
		TraceLinksCreated   int               `json:"trace_links_created"`
		Errors              []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	r.TotalProcessed = wire.TotalProcessed
	r.RequirementsCreated = wire.RequirementsCreated
	r.TraceLinksCreated = wire.TraceLinksCreated
	r.Errors = nil
	for _, e := range wire.Errors {
		r.Errors = append(r.Errors, renderError(e))
	}
	return nil
}

func renderError(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		for _, k := range []string{"message", "error", "detail", "reason"} {
			if v, ok := obj[k].(string); ok && v != "" {
				return v
			}
		}
	}
	return strings.TrimSpace(string(raw))
}
