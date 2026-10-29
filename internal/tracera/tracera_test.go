package tracera

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KooshaPari/dagrun/internal/plan"
	"github.com/KooshaPari/dagrun/internal/store"
)

// fixture builds a two-node, independent plan: no edges, so the item count is
// exactly the node count.
func fixture() (*plan.Plan, *store.State) {
	p := &plan.Plan{
		Version: 1,
		Run:     "fixture-run",
		Nodes: []*plan.Node{
			{ID: "1.1", Kind: "IMPL", Title: "first"},
			{ID: "1.2", Kind: "TEST", Title: "second"},
		},
	}
	st := &store.State{
		Run:   p.Run,
		Nodes: map[string]*store.NodeState{"1.1": {Status: plan.StatusDone}},
	}
	return p, st
}

func TestBuildPayloadNodeIDsAreNamespacedAndUnique(t *testing.T) {
	p, st := fixture()
	payload, err := BuildPayload(p, st, "tracera")
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("items = %d, want 2: %+v", len(payload.Items), payload.Items)
	}
	seen := map[string]bool{}
	for _, it := range payload.Items {
		if it.ID == "" {
			t.Fatalf("item has empty id: %+v", it)
		}
		if !strings.HasPrefix(it.ID, "node:") {
			t.Fatalf("item id %q is not namespaced", it.ID)
		}
		if seen[it.ID] {
			t.Fatalf("duplicate item id %q", it.ID)
		}
		seen[it.ID] = true
	}
	if !seen["node:1.1"] || !seen["node:1.2"] {
		t.Fatalf("missing expected node ids: %+v", seen)
	}
	if payload.Items[0].Status != "Done" {
		t.Fatalf("status = %q, want Done (mapped from done)", payload.Items[0].Status)
	}
}

func TestBuildPayloadRejectsUnknownFormat(t *testing.T) {
	p, st := fixture()
	if _, err := BuildPayload(p, st, "nope"); err == nil {
		t.Fatal("BuildPayload accepted an unknown format")
	}
}

func TestBuildPayloadAgilePlusIsOneBundleItem(t *testing.T) {
	p, st := fixture()
	payload, err := BuildPayload(p, st, "agileplus")
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(payload.Items))
	}
	var bundle map[string]any
	if err := json.Unmarshal(payload.Items[0].Bundle, &bundle); err != nil {
		t.Fatalf("agileplus bundle is not an object: %v", err)
	}
	if _, ok := bundle["projects"]; !ok {
		t.Fatalf("agileplus bundle missing projects: %s", payload.Items[0].Bundle)
	}
}

func TestSendSuccessReturnsCounts(t *testing.T) {
	var gotPath, gotAuth, gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotMethod = r.URL.Path, r.Header.Get("Authorization"), r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"total_processed":2,"requirements_created":1,"trace_links_created":3,"errors":[]}`)
	}))
	defer srv.Close()

	c, err := New(srv.URL+"/", "tok-abc")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p, st := fixture()
	payload, err := BuildPayload(p, st, "tracera")
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	res, err := c.Send(context.Background(), payload)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.TotalProcessed != 2 || res.RequirementsCreated != 1 || res.TraceLinksCreated != 3 {
		t.Fatalf("result = %+v", res)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s", gotMethod)
	}
	if gotPath != IngestPath {
		t.Fatalf("path = %s, want %s", gotPath, IngestPath)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if !strings.Contains(string(gotBody), `"items"`) {
		t.Fatalf("body is not an items payload: %s", gotBody)
	}
}

func TestSend422IsAnErrorNamingStatusAndMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"errors":[{"message":"item 0 has no kind"}]}`)
	}))
	defer srv.Close()

	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Send(context.Background(), Payload{Items: []Item{{ID: "x", Kind: ""}}})
	if err == nil {
		t.Fatal("Send reported success on HTTP 422")
	}
	msg := err.Error()
	if !strings.Contains(msg, "422") {
		t.Fatalf("error does not name the status: %v", err)
	}
	if !strings.Contains(msg, "item 0 has no kind") {
		t.Fatalf("error does not quote the server message: %v", err)
	}
}

func TestSend200WithErrorsIsStillAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"total_processed":0,"requirements_created":0,"trace_links_created":0,"errors":["node 1.1 unknown","edge dangling"]}`)
	}))
	defer srv.Close()

	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Send(context.Background(), Payload{}); err == nil {
		t.Fatal("Send reported success on a 2xx body carrying errors")
	} else {
		if !strings.Contains(err.Error(), "node 1.1 unknown") || !strings.Contains(err.Error(), "edge dangling") {
			t.Fatalf("error does not report every entry: %v", err)
		}
	}
}

func TestSendNonJSON200IsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>ok</html>")
	}))
	defer srv.Close()

	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Send(context.Background(), Payload{}); err == nil {
		t.Fatal("Send accepted a non-JSON body")
	}
}

func TestSendNetworkFailureIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening any more

	c, err := New(url, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Send(context.Background(), Payload{}); err == nil {
		t.Fatal("Send reported success with no server")
	}
}

func TestSendNeverLeaksToken(t *testing.T) {
	const secret = "sk-live-DO-NOT-PRINT"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A hostile/echoing server: it reflects the credential back in the body.
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"errors":["bad token %s"]}`, r.Header.Get("Authorization"))
	}))
	defer srv.Close()

	c, err := New(srv.URL, secret)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Send(context.Background(), Payload{})
	if err == nil {
		t.Fatal("Send reported success on HTTP 401")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("token leaked into the error: %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error does not name the status: %v", err)
	}
}

func TestNewRejectsEmptyAndSchemalessURL(t *testing.T) {
	if _, err := New("  ", ""); err == nil {
		t.Fatal("New accepted an empty URL")
	}
	if _, err := New("localhost:3180", ""); err == nil {
		t.Fatal("New accepted a URL with no scheme")
	}
}
