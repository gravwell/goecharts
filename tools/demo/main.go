// Copyright (c) 2026, Gravwell, Inc.
// All rights reserved.
//
// Use of this source code is governed by a BSD 2-Clause license that can be
// found in the LICENSE file.

// Command demo renders the official ECharts examples gallery with goecharts
// and serves a page that puts each result beside the screenshot from the
// examples site, so the library's fidelity can be judged at a glance.
//
// It first downloads the example list, every example's source, the data
// files the sources refer to and the official thumbnails into a cache
// directory, then starts a web server and renders the examples in the
// background. The page shows a table of every example with its render time
// ("waiting" until it is done), the amount of data fed to the chart and a
// link to the SVG, followed by the side-by-side comparisons.
//
//	go tool demo [-port 8080] [-limit 50] [-match line] [-refresh]
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kris-watts-gravwell/goecharts"
)

const (
	siteRoot  = "https://echarts.apache.org/examples"
	listURL   = "https://raw.githubusercontent.com/apache/echarts-examples/gh-pages/src/data/chart-list-data.js"
	editorFmt = "https://echarts.apache.org/examples/en/editor.html?c=%s"
	// cdnRoot is what the examples site sets CDN_PATH to.
	cdnRoot = "https://fastly.jsdelivr.net/npm/"
)

// example is one entry of the gallery and the state of its render.
type example struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Category   []string `json:"category"`
	Difficulty int      `json:"difficulty"`
	Since      string   `json:"since,omitempty"`

	script    []byte
	thumbName string // cached thumbnail file name, "" if none

	mu        sync.Mutex
	status    string // waiting, ok, failed
	elapsed   time.Duration
	dataBytes int64 // script plus everything the script loaded
	svg       []byte
	errText   string
}

// HasThumb reports whether an official screenshot was downloaded.
func (e *example) HasThumb() bool { return e.thumbName != "" }

// Description is the text shown in the table's description column.
func (e *example) Description() string {
	var parts []string
	if len(e.Category) > 0 {
		parts = append(parts, strings.Join(e.Category, ", "))
	}
	if e.Difficulty > 0 {
		parts = append(parts, fmt.Sprintf("difficulty %d", e.Difficulty))
	}
	if e.Since != "" {
		parts = append(parts, "since ECharts "+e.Since)
	}
	return strings.Join(parts, "; ")
}

type status struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Time      string `json:"time"`
	Data      string `json:"data"`
	DataBytes int64  `json:"dataBytes"`
	SVGBytes  int    `json:"svgBytes"`
	Error     string `json:"error,omitempty"`
	// Reason is a short explanation of a failure for the table.
	Reason string `json:"reason,omitempty"`
}

// failureReason classifies a render error into a short explanation of what
// the example needs that the renderer cannot provide. Errors it does not
// recognize are returned as they are.
func failureReason(script []byte, errText string) string {
	src := string(script)
	switch {
	case errText == "":
		return ""
	case strings.HasPrefix(errText, "fetching source"):
		return "could not download the example source"
	case strings.Contains(errText, "deadline exceeded"):
		return "render exceeded the -timeout limit; try fewer -workers"
	case strings.Contains(src, "'bmap'") || strings.Contains(src, "bmap:"):
		return "needs Baidu Maps (bmap), a browser SDK with online tiles"
	case strings.Contains(src, "_echarts_instance_"):
		return "needs a DOM element to drag (draggable.js)"
	case strings.Contains(src, "type: 'heatmap'") && strings.Contains(src, "coordinateSystem: 'geo'"):
		return "heatmap on a map paints blurred pixels into a canvas, which has no equivalent in SVG"
	case strings.Contains(errText, "DOMParser") || strings.Contains(errText, "parsing XML"):
		return "SVG map could not be parsed: " + errText
	case strings.Contains(errText, "is not defined"):
		return "uses a browser global the renderer does not provide: " + errText
	}
	return strings.TrimPrefix(errText, "echarts: ")
}

func (e *example) snapshot() status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := status{ID: e.ID, Status: e.status, DataBytes: e.dataBytes, Data: humanBytes(e.dataBytes), SVGBytes: len(e.svg), Error: e.errText}
	switch e.status {
	case "ok":
		s.Time = fmt.Sprintf("%d ms", e.elapsed.Milliseconds())
	case "failed":
		s.Time = "failed"
		s.Reason = failureReason(e.script, e.errText)
	default:
		s.Time = "waiting"
	}
	return s
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func main() {
	defaultCache, _ := os.UserCacheDir()
	if defaultCache != "" {
		defaultCache = filepath.Join(defaultCache, "goecharts-demo")
	}
	var (
		port    = flag.Int("port", 8080, "port to serve on")
		cache   = flag.String("cache", defaultCache, "directory for downloaded examples, data and thumbnails")
		limit   = flag.Int("limit", 0, "render only the first N examples (0 = all)")
		match   = flag.String("match", "", "render only examples whose id contains this text")
		width   = flag.Int("w", 600, "render width; the official thumbnails are 600 × 450")
		height  = flag.Int("h", 450, "render height")
		workers = flag.Int("workers", runtime.NumCPU(), "concurrent renders")
		timeout = flag.Duration("timeout", 2*time.Minute, "per-render timeout")
		refresh = flag.Bool("refresh", false, "re-download everything instead of using the cache")
	)
	flag.Parse()
	if *cache == "" {
		log.Fatal("no cache directory; pass -cache")
	}
	if *refresh {
		os.RemoveAll(*cache)
	}
	for _, d := range []string{"scripts", "thumbs", "data"} {
		if err := os.MkdirAll(filepath.Join(*cache, d), 0o755); err != nil {
			log.Fatal(err)
		}
	}
	fetcher := &fetcher{dir: *cache, client: &http.Client{Timeout: 60 * time.Second}}

	// Phase 1: pull the gallery down.
	examples, err := fetcher.listing()
	if err != nil {
		log.Fatalf("fetching example list: %v", err)
	}
	if *match != "" {
		var kept []*example
		for _, e := range examples {
			if strings.Contains(e.ID, *match) {
				kept = append(kept, e)
			}
		}
		examples = kept
	}
	if *limit > 0 && len(examples) > *limit {
		examples = examples[:*limit]
	}
	log.Printf("fetching %d examples into %s", len(examples), *cache)
	fetcher.fetchAll(examples, 8)

	// Phase 2: serve, and render in the background.
	r, err := goecharts.New(
		goecharts.WithLoader(fetcher),
		goecharts.WithTimeout(*timeout),
		goecharts.WithPoolSize(*workers),
		goecharts.WithFailOnError(false),
	)
	if err != nil {
		log.Fatal(err)
	}
	srv := &server{examples: examples, fetcher: fetcher, width: *width, height: *height, started: time.Now()}
	go srv.renderAll(r, *workers)

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("serving on http://localhost:%d/", *port)
	log.Fatal(http.Serve(ln, srv.routes()))
}

// --- fetching -----------------------------------------------------------------

// fetcher downloads gallery files into a cache directory and doubles as the
// renderer's Loader for data the scripts load at render time.
type fetcher struct {
	dir      string
	client   *http.Client
	mu       sync.Mutex // serializes downloads of the same URL
	inFlight map[string]*sync.Mutex
}

// ctxKey carries the example being rendered so that loads count toward its
// data size.
type ctxKey struct{}

// Load implements goecharts.Loader. Relative URLs are resolved against the
// examples site, as ROOT_PATH would make them.
func (f *fetcher) Load(ctx context.Context, url string) ([]byte, error) {
	if strings.HasPrefix(url, "/") {
		url = siteRoot + url
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("unsupported URL")
	}
	b, err := f.get(ctx, url, filepath.Join("data", cacheName(url)))
	if err != nil {
		return nil, err
	}
	if e, ok := ctx.Value(ctxKey{}).(*example); ok {
		e.mu.Lock()
		e.dataBytes += int64(len(b))
		e.mu.Unlock()
	}
	return b, nil
}

// cacheName maps a URL to a file name that keeps the path readable.
func cacheName(url string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	s = strings.NewReplacer("/", "_", "?", "_", "&", "_", "=", "_", ":", "_").Replace(s)
	if len(s) > 200 {
		s = s[len(s)-200:]
	}
	return s
}

// get returns the cached file or downloads it. A 404 is cached as an empty
// marker so that it is not retried on every render.
func (f *fetcher) get(ctx context.Context, url, rel string) ([]byte, error) {
	full := filepath.Join(f.dir, rel)
	f.mu.Lock()
	if f.inFlight == nil {
		f.inFlight = map[string]*sync.Mutex{}
	}
	m := f.inFlight[rel]
	if m == nil {
		m = &sync.Mutex{}
		f.inFlight[rel] = m
	}
	f.mu.Unlock()
	m.Lock()
	defer m.Unlock()

	if b, err := os.ReadFile(full); err == nil {
		if _, err := os.Stat(full + ".missing"); err == nil {
			return nil, errors.New("404 not found (cached)")
		}
		return b, nil
	}
	if _, err := os.Stat(full + ".missing"); err == nil {
		return nil, errors.New("404 not found (cached)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		os.WriteFile(full+".missing", nil, 0o644)
		return nil, errors.New("404 not found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(full, b, 0o644); err != nil {
		return nil, err
	}
	return b, nil
}

// listing downloads the gallery index, a JavaScript module exporting a JSON
// array, and returns the examples in gallery order.
func (f *fetcher) listing() ([]*example, error) {
	b, err := f.get(context.Background(), listURL, "chart-list-data.js")
	if err != nil {
		return nil, err
	}
	start, end := bytes.IndexByte(b, '['), bytes.LastIndexByte(b, ']')
	if start < 0 || end < start {
		return nil, errors.New("listing does not contain a JSON array")
	}
	var list []*example
	if err := json.Unmarshal(b[start:end+1], &list); err != nil {
		return nil, fmt.Errorf("parsing listing: %w", err)
	}
	var out []*example
	for _, e := range list {
		if e.ID != "" {
			e.status = "waiting"
			out = append(out, e)
		}
	}
	return out, nil
}

var dataRefs = []*regexp.Regexp{
	regexp.MustCompile(`ROOT_PATH\s*\+\s*['"]([^'"]+)['"]`),
	regexp.MustCompile(`['"](/data(?:-gl)?/asset/[^'"]+)['"]`),
	regexp.MustCompile(`['"](https?://echarts\.apache\.org/examples/[^'"]+)['"]`),
}

// preloads are what the examples site makes available to every example
// before its script runs: the ecStat plugin and the world and china maps.
// Each entry is a line of script that loads the resource through the usual
// `$` helpers (so it is cached and counted like any other data), applied
// when the example's source matches the pattern and does not load it itself.
var preloads = []struct {
	uses    *regexp.Regexp
	defines *regexp.Regexp
	url     string
	script  string
}{
	{
		uses:   regexp.MustCompile(`\becStat\b`),
		url:    cdnRoot + "echarts-stat@1.2.0/dist/ecStat.min.js",
		script: `$.getScript(CDN_PATH + 'echarts-stat@1.2.0/dist/ecStat.min.js');`,
	},
	{
		uses:    regexp.MustCompile(`map(?:Type)?:\s*['"]world['"]`),
		defines: regexp.MustCompile(`registerMap\(\s*['"]world['"]`),
		url:     siteRoot + "/data/asset/geo/world.json",
		script:  `$.getJSON(ROOT_PATH + '/data/asset/geo/world.json', function (geo) { echarts.registerMap('world', geo); });`,
	},
	{
		uses:    regexp.MustCompile(`map(?:Type)?:\s*['"]china['"]`),
		defines: regexp.MustCompile(`registerMap\(\s*['"]china['"]`),
		url:     cdnRoot + "echarts@4.9.0/map/json/china.json",
		script:  `$.getJSON(CDN_PATH + 'echarts@4.9.0/map/json/china.json', function (geo) { echarts.registerMap('china', geo); });`,
	},
}

// preamble returns the preload script lines an example needs and the URLs
// they fetch.
func preamble(script []byte) (lines []string, urls []string) {
	for _, p := range preloads {
		if !p.uses.Match(script) || (p.defines != nil && p.defines.Match(script)) {
			continue
		}
		lines = append(lines, p.script)
		urls = append(urls, p.url)
	}
	return lines, urls
}

// fetchAll downloads each example's source, thumbnail and the data files its
// source names, with the given number of concurrent downloads.
func (f *fetcher) fetchAll(examples []*example, concurrency int) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	var done atomic.Int64
	for _, e := range examples {
		wg.Add(1)
		sem <- struct{}{}
		go func(e *example) {
			defer wg.Done()
			defer func() { <-sem }()
			ctx := context.Background()
			script, err := f.get(ctx, siteRoot+"/examples/js/"+e.ID+".js", filepath.Join("scripts", e.ID+".js"))
			if err != nil {
				e.mu.Lock()
				e.status, e.errText = "failed", "fetching source: "+err.Error()
				e.mu.Unlock()
			} else {
				e.script = script
				e.dataBytes = int64(len(script))
				var urls []string
				for _, re := range dataRefs {
					for _, m := range re.FindAllSubmatch(script, -1) {
						url := string(m[1])
						if strings.HasPrefix(url, "/") {
							url = siteRoot + url
						}
						urls = append(urls, url)
					}
				}
				lines, extra := preamble(script)
				if len(lines) > 0 {
					e.script = append([]byte(strings.Join(lines, "\n")+"\n"), script...)
				}
				for _, url := range append(urls, extra...) {
					if _, err := f.get(ctx, url, filepath.Join("data", cacheName(url))); err != nil {
						log.Printf("%s: data %s: %v", e.ID, url, err)
					}
				}
			}
			for _, ext := range []string{".webp", ".png"} {
				if _, err := f.get(ctx, siteRoot+"/data/thumb/"+e.ID+ext, filepath.Join("thumbs", e.ID+ext)); err == nil {
					e.thumbName = e.ID + ext
					break
				}
			}
			if n := done.Add(1); n%25 == 0 || int(n) == len(examples) {
				log.Printf("fetched %d/%d", n, len(examples))
			}
		}(e)
	}
	wg.Wait()
}

// --- rendering ----------------------------------------------------------------

type server struct {
	examples []*example
	fetcher  *fetcher
	width    int
	height   int
	started  time.Time
	finished atomic.Int64
}

func (s *server) renderAll(r *goecharts.Renderer, workers int) {
	var wg sync.WaitGroup
	queue := make(chan *example)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range queue {
				s.renderOne(r, e)
			}
		}()
	}
	for _, e := range s.examples {
		if e.script != nil {
			queue <- e
		}
	}
	close(queue)
	wg.Wait()
	ok := 0
	for _, e := range s.examples {
		if e.snapshot().Status == "ok" {
			ok++
		}
	}
	log.Printf("rendered %d examples: %d ok, %d failed, %v", len(s.examples), ok, len(s.examples)-ok, time.Since(s.started).Round(time.Millisecond))
}

func (s *server) renderOne(r *goecharts.Renderer, e *example) {
	ctx := context.WithValue(context.Background(), ctxKey{}, e)
	start := time.Now()
	svg, err := r.Render(ctx, goecharts.Request{
		Script:   e.script,
		RootPath: siteRoot,
		CDNPath:  cdnRoot,
		Width:    s.width,
		Height:   s.height,
	})
	elapsed := time.Since(start)
	e.mu.Lock()
	e.elapsed = elapsed
	if err != nil {
		e.status, e.errText = "failed", err.Error()
	} else {
		e.status, e.svg = "ok", svg
	}
	e.mu.Unlock()
	s.finished.Add(1)
}

// --- serving ------------------------------------------------------------------

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/status.json", s.statusJSON)
	mux.HandleFunc("/svg/", s.svg)
	mux.HandleFunc("/thumb/", s.thumb)
	mux.HandleFunc("/script/", s.script)
	return mux
}

func (s *server) find(id string) *example {
	for _, e := range s.examples {
		if e.ID == id {
			return e
		}
	}
	return nil
}

func (s *server) svg(w http.ResponseWriter, req *http.Request) {
	id := strings.TrimSuffix(path.Base(req.URL.Path), ".svg")
	e := s.find(id)
	if e == nil {
		http.NotFound(w, req)
		return
	}
	e.mu.Lock()
	svg := e.svg
	e.mu.Unlock()
	if svg == nil {
		http.Error(w, "not rendered", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write(svg)
}

func (s *server) thumb(w http.ResponseWriter, req *http.Request) {
	id := path.Base(req.URL.Path)
	e := s.find(id)
	if e == nil || e.thumbName == "" {
		http.NotFound(w, req)
		return
	}
	http.ServeFile(w, req, filepath.Join(s.fetcher.dir, "thumbs", e.thumbName))
}

func (s *server) script(w http.ResponseWriter, req *http.Request) {
	id := strings.TrimSuffix(path.Base(req.URL.Path), ".js")
	e := s.find(id)
	if e == nil || e.script == nil {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(e.script)
}

func (s *server) statusJSON(w http.ResponseWriter, req *http.Request) {
	list := make([]status, 0, len(s.examples))
	for _, e := range s.examples {
		list = append(list, e.snapshot())
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"total":    len(s.examples),
		"finished": s.finished.Load(),
		"examples": list,
	})
}

type row struct {
	N      int
	E      *example
	S      status
	Editor string
}

func (s *server) index(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/" {
		http.NotFound(w, req)
		return
	}
	rows := make([]row, 0, len(s.examples))
	var sorted []status
	for i, e := range s.examples {
		st := e.snapshot()
		rows = append(rows, row{N: i + 1, E: e, S: st, Editor: fmt.Sprintf(editorFmt, e.ID)})
		sorted = append(sorted, st)
	}
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	data := map[string]any{
		"Rows":     rows,
		"Total":    len(s.examples),
		"Finished": s.finished.Load(),
		"Width":    s.width,
		"Height":   s.height,
		"Version":  goecharts.Version(),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.Execute(w, data); err != nil {
		log.Printf("render page: %v", err)
	}
}

var page = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<title>goecharts demo</title>
<style>
  body { font-family: system-ui, sans-serif; margin: 0; padding: 16px 24px; background: #f4f5f7; color: #222; }
  h1 { font-size: 20px; margin: 0 0 4px; }
  .summary { color: #555; margin-bottom: 16px; }
  table { border-collapse: collapse; background: #fff; width: 100%; font-size: 13px; }
  th, td { text-align: left; padding: 4px 8px; border-bottom: 1px solid #e5e7eb; white-space: nowrap; }
  th { background: #eef0f3; position: sticky; top: 0; }
  td.desc { white-space: normal; color: #555; }
  td.num { text-align: right; font-variant-numeric: tabular-nums; }
  .waiting { color: #999; }
  .failed { color: #b00020; }
  .ok { color: #137333; }
  .tablewrap { max-height: 45vh; overflow: auto; border: 1px solid #e5e7eb; margin-bottom: 24px; }
  .card { background: #fff; border: 1px solid #e5e7eb; margin: 12px 0; padding: 12px; }
  .card h2 { font-size: 15px; margin: 0 0 8px; }
  .card h2 small { color: #777; font-weight: normal; margin-left: 8px; }
  .pair { display: flex; gap: 16px; flex-wrap: wrap; }
  .pane { flex: 0 0 auto; }
  .pane .label { font-size: 12px; color: #777; margin-bottom: 4px; }
  .pane img { display: block; width: {{.Width}}px; height: {{.Height}}px; background: #fff; border: 1px solid #ddd; object-fit: contain; }
  .pane .placeholder { width: {{.Width}}px; height: {{.Height}}px; border: 1px dashed #ccc; display: flex; align-items: center; justify-content: center; color: #999; box-sizing: border-box; padding: 16px; text-align: center; white-space: pre-wrap; font-size: 12px; }
  a { color: #1a56db; }
</style>
<h1>goecharts demo — ECharts {{.Version}} examples rendered to SVG</h1>
<div class="summary"><span id="progress">{{.Finished}} of {{.Total}} rendered</span>. Left: goecharts output at {{.Width}}×{{.Height}}. Right: the official screenshot from the examples site.</div>
<div class="tablewrap">
<table>
<thead><tr><th>#</th><th>Name</th><th>Description</th><th>Render time</th><th>Data size</th><th>SVG</th></tr></thead>
<tbody>
{{range .Rows}}<tr id="row-{{.E.ID}}">
  <td class="num">{{.N}}</td>
  <td><a href="#card-{{.E.ID}}">{{.E.Title}}</a> <small><a href="{{.Editor}}" target="_blank" rel="noopener">{{.E.ID}}</a></small></td>
  <td class="desc" id="desc-{{.E.ID}}">{{.E.Description}}{{if .S.Reason}}; <span class="failed" title="{{.S.Error}}">{{.S.Reason}}</span>{{end}}</td>
  <td class="num {{.S.Status}}" id="time-{{.E.ID}}">{{.S.Time}}</td>
  <td class="num" id="data-{{.E.ID}}">{{.S.Data}}</td>
  <td id="svg-{{.E.ID}}">{{if eq .S.Status "ok"}}<a href="/svg/{{.E.ID}}.svg" target="_blank">svg</a> ({{.S.SVGBytes}} B){{else if eq .S.Status "failed"}}<span class="failed">failed</span>{{else}}<span class="waiting">waiting</span>{{end}}</td>
</tr>
{{end}}</tbody>
</table>
</div>
{{range .Rows}}<div class="card" id="card-{{.E.ID}}">
  <h2>{{.N}}. {{.E.Title}} <small>{{.E.ID}} · {{.E.Description}} · <a href="/script/{{.E.ID}}.js" target="_blank">source</a> · <a href="{{.Editor}}" target="_blank" rel="noopener">on echarts.apache.org</a></small></h2>
  <div class="pair">
    <div class="pane"><div class="label">goecharts</div><div id="pane-{{.E.ID}}">{{if eq .S.Status "ok"}}<img src="/svg/{{.E.ID}}.svg" alt="goecharts render of {{.E.ID}}">{{else if eq .S.Status "failed"}}<div class="placeholder failed">{{.S.Error}}</div>{{else}}<div class="placeholder">waiting</div>{{end}}</div></div>
    <div class="pane"><div class="label">echarts.apache.org</div>{{if .E.HasThumb}}<img src="/thumb/{{.E.ID}}" alt="official screenshot of {{.E.ID}}" loading="lazy">{{else}}<div class="placeholder">no official screenshot</div>{{end}}</div>
  </div>
</div>
{{end}}
<script>
(function () {
  var total = {{.Total}};
  function esc(s) { var d = document.createElement('div'); d.textContent = s; return d.innerHTML; }
  function apply(st) {
    var t = document.getElementById('time-' + st.id);
    if (!t || t.textContent === st.time) { return; }
    t.textContent = st.time;
    t.className = 'num ' + st.status;
    document.getElementById('data-' + st.id).textContent = st.data;
    var svg = document.getElementById('svg-' + st.id);
    var pane = document.getElementById('pane-' + st.id);
    if (st.status === 'ok') {
      svg.innerHTML = '<a href="/svg/' + st.id + '.svg" target="_blank">svg</a> (' + st.svgBytes + ' B)';
      pane.innerHTML = '<img src="/svg/' + st.id + '.svg" alt="goecharts render of ' + st.id + '">';
    } else if (st.status === 'failed') {
      svg.innerHTML = '<span class="failed">failed</span>';
      var desc = document.getElementById('desc-' + st.id);
      if (desc && st.reason) {
        desc.insertAdjacentHTML('beforeend', '; <span class="failed" title="' + esc(st.error || '') + '">' + esc(st.reason) + '</span>');
      }
      pane.innerHTML = '<div class="placeholder failed">' + esc(st.error || 'failed') + '</div>';
    }
  }
  function poll() {
    fetch('/status.json').then(function (r) { return r.json(); }).then(function (data) {
      document.getElementById('progress').textContent = data.finished + ' of ' + data.total + ' rendered';
      data.examples.forEach(apply);
      if (data.finished < total) { setTimeout(poll, 1500); }
    }).catch(function () { setTimeout(poll, 3000); });
  }
  if ({{.Finished}} < total) { setTimeout(poll, 1000); }
})();
</script>
</html>
`))
