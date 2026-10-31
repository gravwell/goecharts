# goecharts

Render [Apache ECharts](https://echarts.apache.org/) charts to SVG from pure
Go. No cgo, no browser, no Node.js, and `CGO_ENABLED=0` builds a fully static
binary.

Hand the library an ECharts option plus your data, in columns or rows, and get
back an SVG document. It is meant for servers that produce charts for reports,
alerts, emails and dashboards without a browser in the loop.

## How it works

The package embeds the upstream ECharts 6 JavaScript bundle and runs it with
[goja](https://github.com/dop251/goja), a JavaScript interpreter written in Go,
in ECharts' own server-side rendering mode, which lays out and paints a chart
with no DOM and serializes it with `renderToSVGString`. Go supplies what the
headless runtime would otherwise take from a browser: text measurement, from
width tables derived from the Liberation fonts, so axis, legend and label
layout matches what a viewer will draw.

Because the real ECharts runtime does the work, the whole option language is
supported: every series type in the main bundle (bar, line, pie, scatter,
radar, heatmap, gauge, funnel, sankey, graph, tree, treemap, sunburst,
candlestick, boxplot, parallel, themeRiver, pictorialBar, calendar and polar
coordinates), datasets and dataset transforms, visual maps, mark points, lines
and areas, data zoom sliders, rich text, gradients and themes. The options
under [testdata/examples](testdata/examples) exercise each of them in the
tests.

## Install

```bash
go get github.com/kris-watts-gravwell/goecharts
```

## A simple example

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/kris-watts-gravwell/goecharts"
)

const option = `{
  "title": {"text": "Sales by product"},
  "xAxis": {"type": "category"},
  "yAxis": {},
  "series": [{"type": "bar", "encode": {"x": "product", "y": "amount"}}]
}`

func main() {
	r, err := goecharts.New()
	if err != nil {
		log.Fatal(err)
	}
	svg, err := r.RenderSVG(context.Background(), []byte(option), map[string]goecharts.Dataset{
		"sales": goecharts.NewDataset(
			goecharts.Col("product", []string{"Matcha", "Milk Tea", "Cheese Cocoa", "Walnut Brownie"}),
			goecharts.Col("amount", []float64{43.3, 83.1, 86.4, 72.4}),
		),
	})
	if err != nil {
		log.Fatal(err)
	}
	os.WriteFile("bar.svg", svg, 0o644)
}
```

Create one `Renderer` per process and share it. It is safe for concurrent
use: it compiles the JavaScript bundle once and keeps a pool of interpreter
instances, each serving one render at a time.

## Providing data

Datasets are column-oriented. A `Column` holds a slice of any element type:
integers, floats, bools, strings, `time.Time`, pointers and `any` all work.
`time.Time` is sent as epoch milliseconds, which ECharts time axes accept
directly; `NaN`, `±Inf` and the zero time become `null`. `goecharts.FromRows`
converts row-oriented `[]map[string]any` data.

Datasets are matched to the option's `dataset` entries by `id`:

- A matching entry has its `source` replaced by the rows. Its `dimensions`
  list is kept; `transform` and `fromDataset*` references are dropped, since
  the host data is now the source. Other datasets that transform *from* it
  keep working.
- An unmatched name is appended as a new entry `{"id": name, "source": rows}`.
- When the option has no `dataset` at all and one dataset is supplied, it
  becomes dataset 0, which every series reads by default, as in the example
  above. Series pick columns with `encode`, or take them in order.

Rows are handed to ECharts as objects keyed by column name, in the column
order given, so dimension names and order follow the Go data.

## Render options

`Renderer.Render` takes a `Request`:

| Field | Purpose |
|---|---|
| `Option` | The ECharts option as JSON. One of `Option` and `Script` is required. |
| `Script` | A JavaScript program that produces the option; see below. |
| `RootPath` | The `ROOT_PATH` value a script sees. |
| `CDNPath` | The `CDN_PATH` value a script sees; scripts load third-party libraries from it with `$.getScript`. |
| `Data` | Datasets by id. |
| `Width`, `Height` | Output size in CSS pixels (default 600 × 400). |
| `Theme` | A theme object (JSON), as produced by the ECharts theme builder. |
| `Locale` | `EN` (default) or `ZH` for built-in component text. |
| `UseViewBox` | Emit a `viewBox` instead of fixed `width`/`height` attributes. |
| `Animation` | Keep entry animations as CSS keyframes. Off by default, so the SVG shows the chart's final state. |

Renderer options:

| Option | Default | Purpose |
|---|---|---|
| `WithTimeout` | 30s | Per-render wall clock limit; the interpreter is interrupted. |
| `WithPoolSize` | `NumCPU` | Idle interpreter instances kept for reuse. |
| `WithLoader` | loading disabled | Resolve URLs that scripts load. |
| `WithLogger` | discard | Receive ECharts console output. |
| `WithFailOnError` | true | Fail the render when ECharts logs at error level. |
| `WithTextMeasurer` | built-in metrics | Replace the text width function used for layout. |

### Behavior differences from a browser

- **Animation** is disabled unless the option sets `animation` itself or
  `Request.Animation` is true.
- **Force-directed graphs** run their simulation to convergence inside the
  render (`force.layoutAnimation` defaults to false), since no animation
  frames run on a server. Set it explicitly to keep ECharts' default.
- **Interactive components** such as tooltips, the toolbox and data zoom
  render in their initial state; nothing responds to events in a static SVG.
- **Images** are not embedded. An `image` graphic, symbol or pattern keeps
  its URL in the SVG for the viewer to load; the Loader is consulted only
  for the image's size, and a `graphic` image still needs explicit `width`
  and `height`, as in ECharts' own SVG renderer.
- **Maps** need their GeoJSON or SVG registered with `echarts.registerMap`
  from a script (see below); the JSON `Option` path has no way to do so.
- **Unknown series or component types** are ignored without error, as the
  production ECharts build does in a browser.
- **Large series** are drawn in full. In a browser ECharts paints series
  above the `progressiveThreshold` in chunks across animation frames; the
  server-side renderer emits every element at once, so a scatter of
  100,000 points yields an SVG with 100,000 paths and takes seconds. Thin
  the data, or use `sampling` on line series, before rendering.

### Errors

A malformed option, or an exception thrown by ECharts while processing it,
fails the render with a `*goecharts.Error`. The production ECharts bundle
strips most error messages, so the message often names only the position in
the bundle; `WithLogger` shows any warnings ECharts printed first.

## Command-line tool

`echarts2svg` renders one option:

```bash
go run ./cmd/echarts2svg -data sales=values.csv -w 800 -h 500 -o chart.svg option.json
```

`-data id=file.csv` injects CSV files (header row required; numbers and
RFC 3339 timestamps are inferred), `-theme file.json` applies a theme,
`-viewbox` emits a scalable root element and `-v` prints ECharts console
output.

## Rendering scripts instead of JSON

Many real options cannot be expressed as JSON: label and tooltip formatters
are functions, gradients are `echarts.graphic.LinearGradient` objects, maps
must be registered with `echarts.registerMap`, and the examples on the
ECharts site are programs that build `option` and often load their data
first. `Request.Script` renders such a program directly:

```go
svg, err := r.Render(ctx, goecharts.Request{
	Script: []byte(`
	  $.get(ROOT_PATH + '/data/asset/data/les-miserables.json', function (graph) {
	    option = {series: [{type: 'graph', layout: 'circular', data: graph.nodes, links: graph.links,
	      label: {formatter: function (p) { return p.name.toUpperCase(); }}}]};
	  });`),
	RootPath: "https://echarts.apache.org/examples",
})
```

The script runs inside the rendering VM with the globals the examples site
provides: `echarts`, `myChart` (the chart being rendered), `app`, `$` (the
`get`, `getJSON`, `getScript`, `ajax` and `when` subset of jQuery, with
jQuery's `done` semantics), `fetch`, `ROOT_PATH` and `CDN_PATH`. It may
assign `option` or call `myChart.setOption`. Data loads go through the
Renderer's `Loader`, set with `WithLoader`; without one every load fails,
since fetching URLs named by an untrusted script would be a request-forgery
risk. Loads complete synchronously from the script's point of view.

Scripts also see a small browser-like environment, enough for the
patterns the examples use and nothing more:

- `window` (the global object, with `innerWidth` and `innerHeight` set to
  the output size), `navigator`, `devicePixelRatio`, `addEventListener`
  and `matchMedia`.
- `document`, whose `createElement` returns inert elements; a canvas has a
  2D context that accepts any call and measures text, so a script that
  paints a watermark runs, though the pattern it paints is not produced.
- `DOMParser`, which parses XML in Go and returns the subset of the DOM
  that ECharts' SVG map parser walks, so `echarts.registerMap(name, {svg})`
  works.
- `Image`, whose source is resolved through the Loader to learn the pixel
  size (PNG, JPEG, GIF and SVG); the SVG keeps the URL and the viewer loads
  the pixels. Images named in an option take the same path.
- `XMLHttpRequest`, `structuredClone` and `$.getScript`, which evaluates a
  loaded script in the global scope so that UMD libraries such as d3 or
  ecStat define their globals.
- Timers. `setTimeout` and `setInterval` callbacks are queued and run, in
  due order, once the script has returned and again before the chart is
  serialized, each at most once (intervals included), up to 1000 callbacks
  per render. A script that builds its option inside `setTimeout`, or
  updates the chart a moment later, renders the state a browser would show
  after those timers; a script that animates forever renders a late frame.
  Animation frames never run.

## Evaluating against the ECharts examples gallery

The `demo` tool renders the whole official examples gallery and serves a
page that puts each result beside the screenshot from the examples site:

```bash
go tool demo
```

It downloads the example list, every example's source, the data files the
sources refer to and the official thumbnails into a cache directory (under
`os.UserCacheDir()`, so later runs start at once), then serves
http://localhost:8080/ and renders the examples in the background. The top
of the page is a table with every example's name, description (with the
reason for a failure, once it has failed), render time (`waiting` until it
is done), the amount of data fed to the chart (source plus loaded files)
and a link to the SVG; below it, each example shows the
goecharts SVG next to the official image at the same 600 × 450 size.

Useful flags: `-limit 40` renders only the first 40 examples, `-match line`
only those whose id contains `line`, `-port` changes the port, `-workers`
the number of concurrent renders, `-timeout` the per-render limit (2 minutes)
and `-refresh` discards the cache.
The tool supplies what the examples site gives every example before its
script runs: `CDN_PATH` (jsdelivr), the `ecStat` plugin, and the `world`
and `china` maps, each fetched only for the examples that use them. With
ECharts 6.1.0, 370 of the 377 examples render. The rest need Baidu Maps,
a real canvas (the heatmap on a geo map blurs pixels) or a DOM element to
drag; the largest charts can also exceed `-timeout` when every worker is
busy.

## Text measurement

ECharts lays out axes, legends and truncated labels from measured text
widths. Without a canvas it falls back to a crude per-character estimate.
This package measures text with character widths and kerning pairs taken
from the Liberation Sans (`sans-serif` and unknown families) and Liberation
Serif (`serif`) fonts, regular and bold, and a fixed 0.6 em per character for
`monospace`. The tables cover ASCII, Latin-1, Latin Extended-A and common
punctuation; other characters are approximated. The Liberation fonts are
metrically compatible with Arial and Times New Roman, which match Helvetica
and Times, so layout closely tracks what a viewer will draw.
`WithTextMeasurer` plugs in exact metrics for a specific font.

## Time zones

ECharts formats time axis labels in the JavaScript runtime's local time zone,
which goja takes from Go's `time.Local`. Set `TZ` or assign `time.Local` to
control it, or set `useUTC: true` in the option.

## Performance

goja is an interpreter, roughly 20 to 50 times slower than V8. The first
`New` compiles the bundle (about 200 ms, once per process); each interpreter
instance in the pool then costs about 20 ms to initialize. After that the
gallery examples under `testdata/examples` render as follows. Each was
rendered at 800 × 500 on an 8-core laptop with a warm pool; the time is the
median of seven renders, and the data column counts the data items in the
option (points, rows, nodes and links).

| Example | Description | Data | SVG size | Time |
|---|---|---|---|---|
| `bar` | Vertical bars from an injected dataset with `encode` (the README example) | 4 rows | 4 KB | 22 ms |
| `bar-stack-horizontal` | Three stacked horizontal bar series with value labels and legend | 21 points | 13 KB | 44 ms |
| `boxplot` | Boxplot built by the `boxplot` dataset transform, with outlier scatter | 5 rows × 20 values | 7 KB | 29 ms |
| `calendar` | Heatmap on a one-month calendar coordinate system | 7 cells | 6 KB | 16 ms |
| `candlestick` | OHLC candlesticks on a category axis | 4 candles | 4 KB | 11 ms |
| `dataset-encode` | Three bar series read from one array-of-arrays dataset | 4 rows × 3 series | 7 KB | 19 ms |
| `datazoom-markline` | Line with mark points, average mark line, mark area, data zoom slider and toolbox | 7 points | 13 KB | 36 ms |
| `effect-rich-text` | Rich text title, rotated axis labels, log axis and a linear gradient fill | 3 points | 4 KB | 19 ms |
| `funnel` | Descending funnel with inside labels | 5 stages | 7 KB | 23 ms |
| `gauge` | Gauge with progress arc, pointer and detail label | 1 value | 11 KB | 13 ms |
| `graph-circular` | Graph with circular layout, rotated labels and curved edges | 6 nodes, 6 links | 5 KB | 14 ms |
| `graph-force` | Graph with force-directed layout, run to convergence | 5 nodes, 5 links | 4 KB | 97 ms |
| `heatmap` | Punch-card heatmap with cell labels and a continuous visual map | 84 cells | 47 KB | 88 ms |
| `line-smooth` | Three stacked, smoothed area lines with legend and labels | 21 points | 17 KB | 28 ms |
| `parallel` | Parallel coordinates with a category axis | 3 lines × 4 axes | 9 KB | 25 ms |
| `pictorialbar` | Pictorial bars built from repeated rectangle symbols | 4 bars | 29 KB | 35 ms |
| `pie-doughnut` | Doughnut with rounded borders, percentage labels and legend | 5 slices | 9 KB | 25 ms |
| `polar-bar` | Tangential bars on polar coordinates with labels | 4 bars | 7 KB | 22 ms |
| `radar` | Radar with six indicators and two series | 2 series × 6 values | 8 KB | 28 ms |
| `sankey` | Sankey diagram | 6 nodes, 6 links | 5 KB | 18 ms |
| `scatter` | Scatter with a continuous visual map on the y dimension | 22 points | 24 KB | 38 ms |
| `sunburst` | Three-level sunburst with radial labels | 13 nodes | 6 KB | 25 ms |
| `themeriver` | Theme river on a time single axis | 15 points, 3 streams | 4 KB | 15 ms |
| `tree` | Orthogonal tree with leaf labels | 6 nodes | 4 KB | 14 ms |
| `treemap` | Two-level treemap with breadcrumb | 6 nodes | 4 KB | 15 ms |

Render time grows with the number of SVG elements rather than with the raw
data: the heatmap draws a rectangle and a label per cell, and the force graph
spends its time in the layout simulation.

## Upgrading the bundle

Replace `internal/jsdist/echarts.min.js` with a newer `dist/echarts.min.js`
from the `echarts` npm package, update the license files beside it, bump the
version constant in `internal/jsdist/embed.go`, and run the tests.

## Projects this builds on

- [Apache ECharts](https://github.com/apache/echarts) and
  [zrender](https://github.com/ecomfe/zrender), its rendering engine.
  Everything a chart does happens in their code.
- [goja](https://github.com/dop251/goja), the pure-Go JavaScript engine that
  runs them.
- The [Liberation fonts](https://github.com/liberationfonts/liberation-fonts),
  whose metrics drive text layout.

## Licenses

This project's own code is under the BSD 2-Clause license; see
[LICENSE](LICENSE). It embeds and links third-party code under permissive
licenses (Apache-2.0, BSD, MIT). The one exception is `fontwidths.go`, which
is derived from the Liberation fonts and stays under the SIL Open Font License
1.1. [LICENSES](LICENSES) lists every component, where it came from and its
license. No GPL or other copyleft code is included.
