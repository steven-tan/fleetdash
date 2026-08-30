// fleetdash — a tiny fleet status dashboard.
//
// The same binary runs on every node. Each instance:
//   - GET /api/status  -> JSON about THIS node (env, cloud, version, uptime, load)
//   - GET /            -> HTML table aggregating this node + every configured peer
//   - GET /healthz     -> liveness probe used by the deploy script
//
// Everything is plain HTTP over Tailscale — no TLS, no public exposure. v1 is
// deliberately stateless: the page fetches peers live on each load.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// version is injected at build time:  -ldflags "-X main.version=<git sha>"
var version = "dev"

// startedAt is used for process uptime.
var startedAt = time.Now()

// Status is what /api/status returns and what peers are parsed into.
type Status struct {
	Node          string    `json:"node"`
	Env           string    `json:"env"`
	Cloud         string    `json:"cloud"`
	Region        string    `json:"region"`
	Version       string    `json:"version"`
	Hostname      string    `json:"hostname"`
	GoOS          string    `json:"go_os"`
	GoArch        string    `json:"go_arch"`
	Now           time.Time `json:"now"`
	ProcUptimeSec int64     `json:"proc_uptime_sec"`
	HostUptimeSec int64     `json:"host_uptime_sec,omitempty"`
	Load1         float64   `json:"load1,omitempty"`
}

type config struct {
	listen string
	node   string
	env    string
	cloud  string
	region string
	peers  []string
}

func getenv(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func loadConfig() config {
	host, _ := os.Hostname()
	return config{
		listen: getenv("FLEETDASH_LISTEN", ":8080"),
		node:   getenv("FLEETDASH_NODE", host),
		env:    getenv("FLEETDASH_ENV", "unknown"),
		cloud:  getenv("FLEETDASH_CLOUD", "unknown"),
		region: getenv("FLEETDASH_REGION", ""),
		peers:  parsePeers(os.Getenv("FLEETDASH_PEERS")),
	}
}

// parsePeers turns "http://a:8080, http://b:8080/ ," into a clean slice.
func parsePeers(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, strings.TrimRight(p, "/"))
		}
	}
	return out
}

func (c config) localStatus() Status {
	host, _ := os.Hostname()
	s := Status{
		Node:          c.node,
		Env:           c.env,
		Cloud:         c.cloud,
		Region:        c.region,
		Version:       version,
		Hostname:      host,
		GoOS:          runtime.GOOS,
		GoArch:        runtime.GOARCH,
		Now:           time.Now().UTC(),
		ProcUptimeSec: int64(time.Since(startedAt).Seconds()),
	}
	if up, err := readFirstFloat("/proc/uptime"); err == nil {
		s.HostUptimeSec = int64(up)
	}
	if l, err := readFirstFloat("/proc/loadavg"); err == nil {
		s.Load1 = l
	}
	return s
}

// readFirstFloat reads the first whitespace-delimited field of a file as a float.
// Used for /proc/uptime and /proc/loadavg; absent (non-Linux) -> error, ignored.
func readFirstFloat(path string) (float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0, errors.New("empty file: " + path)
	}
	var f float64
	if _, err := fmt.Sscanf(fields[0], "%f", &f); err != nil {
		return 0, err
	}
	return f, nil
}

type peerResult struct {
	URL    string
	Status *Status
	Err    string
}

func fetchPeer(ctx context.Context, client *http.Client, base string) peerResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/status", nil)
	if err != nil {
		return peerResult{URL: base, Err: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return peerResult{URL: base, Err: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return peerResult{URL: base, Err: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	var s Status
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return peerResult{URL: base, Err: "bad JSON: " + err.Error()}
	}
	return peerResult{URL: base, Status: &s}
}

// gatherPeers fetches every peer concurrently; a slow/down peer never blocks the rest.
func gatherPeers(ctx context.Context, peers []string) []peerResult {
	client := &http.Client{Timeout: 3 * time.Second}
	results := make([]peerResult, len(peers))
	var wg sync.WaitGroup
	for i, p := range peers {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			results[i] = fetchPeer(ctx, client, p)
		}(i, p)
	}
	wg.Wait()
	return results
}

type row struct {
	Label   string
	Node    string
	Env     string
	Cloud   string
	Region  string
	Version string
	Arch    string
	Uptime  string
	Load1   string
	Healthy bool
	Detail  string
	Self    bool
}

type view struct {
	Self         row
	Peers        []row
	Generated    string
	AllVersions  []string
	VersionDrift bool
}

func humanDur(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	d := sec / 86400
	h := (sec % 86400) / 3600
	m := (sec % 3600) / 60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd%dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

func statusToRow(label string, s Status, self bool) row {
	return row{
		Label: label, Node: s.Node, Env: s.Env, Cloud: s.Cloud, Region: s.Region,
		Version: s.Version, Arch: s.GoArch, Uptime: humanDur(s.HostUptimeSec),
		Load1: fmt.Sprintf("%.2f", s.Load1), Healthy: true, Detail: "ok", Self: self,
	}
}

func buildView(cfg config, results []peerResult) view {
	self := cfg.localStatus()
	v := view{
		Generated: time.Now().UTC().Format(time.RFC3339),
		Self:      statusToRow("(self)", self, true),
	}
	versions := map[string]bool{self.Version: true}
	for _, r := range results {
		if r.Status != nil {
			v.Peers = append(v.Peers, statusToRow(r.URL, *r.Status, false))
			versions[r.Status.Version] = true
			continue
		}
		v.Peers = append(v.Peers, row{Label: r.URL, Healthy: false, Detail: r.Err})
	}
	for ver := range versions {
		v.AllVersions = append(v.AllVersions, ver)
	}
	sort.Strings(v.AllVersions)
	v.VersionDrift = len(v.AllVersions) > 1
	return v
}

var pageTmpl = template.Must(template.New("page").Parse(pageHTML))

func newServer(cfg config) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(cfg.localStatus())
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		v := buildView(cfg, gatherPeers(ctx, cfg.peers))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := pageTmpl.Execute(w, v); err != nil {
			log.Printf("template: %v", err)
		}
	})

	return mux
}

func main() {
	cfg := loadConfig()
	srv := &http.Server{
		Addr:         cfg.listen,
		Handler:      newServer(cfg),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("fleetdash %s on %s (node=%s env=%s cloud=%s peers=%d)",
			version, cfg.listen, cfg.node, cfg.env, cfg.cloud, len(cfg.peers))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Println("bye")
}

const pageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="10">
<title>fleetdash</title>
<style>
  body { font: 14px/1.45 system-ui, sans-serif; margin: 2rem; color: #1b1b1b; }
  h1 { font-size: 1.1rem; margin-bottom: .2rem; }
  .muted { color: #777; }
  table { border-collapse: collapse; margin-top: 1rem; }
  th, td { border: 1px solid #ccc; padding: 4px 10px; text-align: left; white-space: nowrap; }
  th { background: #f2f2f2; }
  tr.self td { background: #f7fbff; }
  .dot { display: inline-block; width: 9px; height: 9px; border-radius: 50%; }
  .up { background: #1a9850; }
  .down { background: #d73027; }
  .drift { background: #fff3cd; border: 1px solid #ffe08a; padding: 6px 10px; margin-top: 1rem; }
  code { background: #eee; padding: 0 4px; border-radius: 3px; }
</style>
</head>
<body>
<h1>fleetdash <span class="muted">&mdash; {{.Self.Node}} ({{.Self.Env}})</span></h1>
<div class="muted">generated {{.Generated}} &middot; auto-refresh 10s</div>
{{if .VersionDrift}}<div class="drift">&#9888; version drift across fleet: {{range .AllVersions}}<code>{{.}}</code> {{end}}</div>{{end}}
<table>
  <tr>
    <th></th><th>target</th><th>node</th><th>env</th><th>cloud</th><th>region</th>
    <th>version</th><th>arch</th><th>host uptime</th><th>load1</th><th>detail</th>
  </tr>
  <tr class="self">
    <td><span class="dot up"></span></td>
    <td>{{.Self.Label}}</td><td>{{.Self.Node}}</td><td>{{.Self.Env}}</td>
    <td>{{.Self.Cloud}}</td><td>{{.Self.Region}}</td><td>{{.Self.Version}}</td>
    <td>{{.Self.Arch}}</td><td>{{.Self.Uptime}}</td><td>{{.Self.Load1}}</td><td>{{.Self.Detail}}</td>
  </tr>
  {{range .Peers}}
  <tr>
    <td><span class="dot {{if .Healthy}}up{{else}}down{{end}}"></span></td>
    <td>{{.Label}}</td><td>{{.Node}}</td><td>{{.Env}}</td><td>{{.Cloud}}</td>
    <td>{{.Region}}</td><td>{{.Version}}</td><td>{{.Arch}}</td>
    <td>{{.Uptime}}</td><td>{{.Load1}}</td><td>{{.Detail}}</td>
  </tr>
  {{end}}
</table>
</body>
</html>
`
