package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHealthz(t *testing.T) {
	rr := httptest.NewRecorder()
	newServer(config{}).ServeHTTP(rr, httptest.NewRequest("GET", "/healthz", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "ok") {
		t.Fatalf("healthz: code=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestAPIStatus(t *testing.T) {
	cfg := config{node: "n1", env: "dev", cloud: "aws", region: "us-x"}
	rr := httptest.NewRecorder()
	newServer(cfg).ServeHTTP(rr, httptest.NewRequest("GET", "/api/status", nil))
	if rr.Code != 200 {
		t.Fatalf("status code %d", rr.Code)
	}
	var s Status
	if err := json.Unmarshal(rr.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.Node != "n1" || s.Env != "dev" || s.Cloud != "aws" {
		t.Fatalf("unexpected: %+v", s)
	}
	if s.Version == "" {
		t.Fatal("version empty")
	}
	// load1 must always be serialized, even when 0 — no omitempty. A genuine
	// idle reading of 0.00 has to stay distinguishable from an absent field.
	if !strings.Contains(rr.Body.String(), `"load1"`) {
		t.Errorf("response missing load1 field: %s", rr.Body.String())
	}
}

func TestParsePeers(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"http://a:8080", []string{"http://a:8080"}},
		{" http://a:8080 , http://b:8080/ ,", []string{"http://a:8080", "http://b:8080"}},
	}
	for _, c := range cases {
		if got := parsePeers(c.in); !slices.Equal(got, c.want) {
			t.Errorf("parsePeers(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestHumanDur(t *testing.T) {
	cases := map[int64]string{0: "-", 90: "1m", 3720: "1h2m", 90000: "1d1h"}
	for in, want := range cases {
		if got := humanDur(in); got != want {
			t.Errorf("humanDur(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestIndexRendersWithoutPeers(t *testing.T) {
	rr := httptest.NewRecorder()
	newServer(config{node: "solo", env: "dev"}).ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "solo") {
		t.Fatalf("index: code=%d", rr.Code)
	}
}

func TestVersionDriftFlag(t *testing.T) {
	v := buildView(config{}, []peerResult{
		{URL: "http://a", Status: &Status{Version: "aaa"}},
		{URL: "http://b", Status: &Status{Version: "bbb"}},
	})
	if !v.VersionDrift {
		t.Fatalf("expected drift, versions=%v", v.AllVersions)
	}
}

// --- peer fetching: the network-facing code, previously untested ---

func TestFetchPeerOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Status{Node: "peer1", Version: "abc123def456", Env: "stage"})
	}))
	defer srv.Close()

	got := fetchPeer(context.Background(), srv.Client(), srv.URL)
	if got.Err != "" {
		t.Fatalf("unexpected error: %s", got.Err)
	}
	if got.Status == nil || got.Status.Node != "peer1" || got.Status.Version != "abc123def456" {
		t.Fatalf("bad status: %+v", got.Status)
	}
}

func TestFetchPeerHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	got := fetchPeer(context.Background(), srv.Client(), srv.URL)
	if got.Status != nil {
		t.Fatalf("expected no status, got %+v", got.Status)
	}
	if got.Err != "HTTP 500" {
		t.Errorf("err = %q, want %q", got.Err, "HTTP 500")
	}
}

func TestFetchPeerBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json{"))
	}))
	defer srv.Close()

	got := fetchPeer(context.Background(), srv.Client(), srv.URL)
	if got.Status != nil {
		t.Fatalf("expected no status, got %+v", got.Status)
	}
	if !strings.HasPrefix(got.Err, "bad JSON") {
		t.Errorf("err = %q, want it to start with 'bad JSON'", got.Err)
	}
}

func TestFetchPeerUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening on that port now

	got := fetchPeer(context.Background(), http.DefaultClient, url)
	if got.Status != nil {
		t.Fatalf("expected no status, got %+v", got.Status)
	}
	if got.Err == "" {
		t.Error("expected a connection error, got empty Err")
	}
}

// gatherPeers must fetch every peer, keep results in input order, isolate a
// down peer from the healthy ones, and do it concurrently rather than serially.
func TestGatherPeersConcurrentAndOrdered(t *testing.T) {
	slow := func(node string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(Status{Node: node, Version: "v1"})
		}))
	}
	a := slow("a")
	defer a.Close()
	b := slow("b")
	defer b.Close()
	c := slow("c")
	defer c.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	peers := []string{a.URL, deadURL, b.URL, c.URL}
	start := time.Now()
	got := gatherPeers(context.Background(), peers)
	elapsed := time.Since(start)

	if len(got) != 4 {
		t.Fatalf("want 4 results, got %d", len(got))
	}
	if got[0].URL != a.URL || got[1].URL != deadURL || got[2].URL != b.URL || got[3].URL != c.URL {
		t.Fatalf("results not in input order: %+v", got)
	}
	if got[0].Status == nil || got[0].Status.Node != "a" ||
		got[2].Status == nil || got[2].Status.Node != "b" ||
		got[3].Status == nil || got[3].Status.Node != "c" {
		t.Errorf("healthy peers not populated: %+v", got)
	}
	if got[1].Err == "" {
		t.Errorf("down peer should carry an error: %+v", got[1])
	}
	// Three independent 200ms fetches in parallel finish in ~200ms; run back to
	// back they would take ~600ms. 450ms cleanly separates the two.
	if elapsed > 450*time.Millisecond {
		t.Errorf("gatherPeers took %v — looks serial, not concurrent", elapsed)
	}
}

// buildView must render a down peer as an unhealthy row carrying its error,
// alongside healthy rows, and only flag drift when versions actually differ.
func TestBuildViewMixedHealth(t *testing.T) {
	v := buildView(config{}, []peerResult{
		{URL: "http://good", Status: &Status{Node: "good", Version: "dev"}},
		{URL: "http://bad", Err: "connection refused"},
	})
	if len(v.Peers) != 2 {
		t.Fatalf("want 2 peer rows, got %d", len(v.Peers))
	}
	if !v.Peers[0].Healthy || v.Peers[0].Detail != "ok" {
		t.Errorf("row 0 should be healthy/ok: %+v", v.Peers[0])
	}
	if v.Peers[1].Healthy {
		t.Errorf("row 1 should be unhealthy: %+v", v.Peers[1])
	}
	if v.Peers[1].Detail != "connection refused" {
		t.Errorf("row 1 detail = %q, want the error text", v.Peers[1].Detail)
	}
	// self version defaults to "dev"; the healthy peer is also "dev" → no drift.
	if v.VersionDrift {
		t.Errorf("no drift expected, versions = %v", v.AllVersions)
	}
}
