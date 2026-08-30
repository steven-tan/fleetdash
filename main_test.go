package main

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
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
