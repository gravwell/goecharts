// Copyright (c) 2026, Gravwell, Inc.
// All rights reserved.
//
// Use of this source code is governed by a BSD 2-Clause license that can be
// found in the LICENSE file.

// Package goecharts renders Apache ECharts options to SVG in pure Go.
//
// The package embeds the upstream ECharts JavaScript bundle and executes it
// with the goja interpreter in ECharts' server-side rendering mode, so it
// has no cgo dependency and does not require a browser or Node.js. Go
// supplies text measurement from embedded font metric tables so that layout
// matches what a viewer will draw.
//
// Typical use:
//
//	r, err := goecharts.New()
//	...
//	svg, err := r.RenderSVG(ctx, optionJSON, map[string]goecharts.Dataset{
//		"sales": goecharts.NewDataset(
//			goecharts.Col("product", []string{"A", "B", "C"}),
//			goecharts.Col("amount", []float64{28, 55, 43}),
//		),
//	})
//
// Datasets are matched to the option's `dataset` entries by id: a matching
// entry has its source replaced, and an unmatched name is appended as a new
// dataset with that id. A single dataset handed to an option with no
// `dataset` becomes dataset 0, which series read by default.
//
// A Renderer is safe for concurrent use. Construct one and share it: it
// compiles the ECharts bundle once and keeps a pool of JavaScript runtimes.
package goecharts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	_ "embed"

	"github.com/dop251/goja"
	"github.com/kris-watts-gravwell/goecharts/internal/jsdist"
)

//go:embed pre.js
var preJS string

//go:embed post.js
var postJS string

// Version returns the embedded ECharts version.
func Version() string { return jsdist.Version }

// Error is an error raised by the ECharts runtime, for example an option it
// cannot parse or a series type it does not know.
type Error struct {
	Msg string
}

func (e *Error) Error() string { return "echarts: " + e.Msg }

// Default output dimensions in CSS pixels, used when a Request leaves
// Width or Height zero.
const (
	DefaultWidth  = 600
	DefaultHeight = 400
)

// Request describes one render.
type Request struct {
	// Option is the ECharts option as JSON. Exactly one of Option and Script
	// must be set.
	Option []byte
	// Script is a JavaScript program that produces the option, written the
	// way the examples on the ECharts site are: it assigns `option` or calls
	// `myChart.setOption(...)`. It runs inside the rendering VM with the
	// globals the examples site provides (`echarts`, `myChart`, `app`, `$`,
	// `fetch` and `ROOT_PATH`), so options may contain formatter functions,
	// gradient objects, registered maps and anything else JSON cannot carry.
	// Data loads go through the Renderer's Loader; see WithLoader and
	// RootPath. Timers never fire, so a script that updates the chart later
	// renders its first state.
	Script []byte
	// RootPath is the value of the ROOT_PATH global a Script sees; the
	// examples build data URLs from it. It defaults to "".
	RootPath string
	// CDNPath is the value of the CDN_PATH global a Script sees; the
	// examples load third-party scripts such as d3 from it with
	// `$.getScript`. It defaults to "".
	CDNPath string
	// Data supplies datasets by id; see the package documentation.
	Data map[string]Dataset
	// Width and Height are the output size in CSS pixels; zero means
	// DefaultWidth and DefaultHeight.
	Width, Height int
	// Theme is an optional ECharts theme object (JSON), as produced by the
	// theme builder, applied beneath the option.
	Theme []byte
	// Locale selects the built-in ECharts locale for component text such as
	// the data zoom and toolbox titles: "EN" (the default) or "ZH".
	Locale string
	// UseViewBox emits a viewBox attribute instead of fixed width and height
	// attributes on the root <svg>, so the image scales to its container.
	UseViewBox bool
	// Animation keeps the option's entry animations. ECharts writes them as
	// CSS keyframes inside the SVG; by default they are disabled so that the
	// document shows the chart's final state. An explicit `animation`
	// property in the option always wins.
	Animation bool
}

// Renderer renders ECharts options. Create one with New.
type Renderer struct {
	opts options

	pre, bundle, post *goja.Program

	pool chan *jsRuntime
}

var (
	sharedOnce   sync.Once
	sharedPre    *goja.Program
	sharedBundle *goja.Program
	sharedPost   *goja.Program
	sharedErr    error
)

// New creates a Renderer. Compilation of the JavaScript bundle is shared
// between all Renderers in the process and happens once.
func New(opts ...Option) (*Renderer, error) {
	o := options{
		poolSize:    runtime.NumCPU(),
		timeout:     30 * time.Second,
		measurer:    DefaultTextMeasurer,
		failOnError: true,
	}
	for _, opt := range opts {
		opt(&o)
	}
	r := &Renderer{opts: o, pool: make(chan *jsRuntime, o.poolSize)}
	if err := r.compile(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Renderer) compile() error {
	sharedOnce.Do(func() {
		sharedPre, sharedErr = goja.Compile("goecharts-pre.js", preJS, true)
		if sharedErr != nil {
			return
		}
		sharedBundle, sharedErr = goja.Compile("echarts.min.js", jsdist.EChartsJS, true)
		if sharedErr != nil {
			return
		}
		sharedPost, sharedErr = goja.Compile("goecharts-post.js", postJS, true)
	})
	if sharedErr != nil {
		return fmt.Errorf("echarts: compiling runtime: %w", sharedErr)
	}
	r.pre, r.bundle, r.post = sharedPre, sharedBundle, sharedPost
	return nil
}

// RenderSVG renders option with the given datasets at the default size and
// returns the SVG document. data may be nil when the option carries its own
// data.
func (r *Renderer) RenderSVG(ctx context.Context, option []byte, data map[string]Dataset) ([]byte, error) {
	return r.Render(ctx, Request{Option: option, Data: data})
}

// Render renders a Request and returns the SVG document.
func (r *Renderer) Render(ctx context.Context, req Request) (svg []byte, err error) {
	hasOption := len(strings.TrimSpace(string(req.Option))) > 0
	hasScript := len(strings.TrimSpace(string(req.Script))) > 0
	switch {
	case hasOption && hasScript:
		return nil, errors.New("echarts: Request sets both Option and Script")
	case !hasOption && !hasScript:
		return nil, &Error{Msg: "empty option"}
	}
	dataJSON, err := encodeDatasets(req.Data)
	if err != nil {
		return nil, err
	}
	optsJSON, err := encodeRenderOptions(req)
	if err != nil {
		return nil, err
	}
	if r.opts.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.opts.timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rt, err := r.acquire()
	if err != nil {
		return nil, err
	}
	healthy := false
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("echarts: panic during render: %v", p)
			healthy = false
		}
		if healthy {
			r.release(rt)
		}
	}()

	if hasScript {
		svg, err = rt.renderScript(ctx, string(req.Script), string(dataJSON), string(optsJSON))
	} else {
		svg, err = rt.render(ctx, string(req.Option), string(dataJSON), string(optsJSON))
	}
	if err != nil {
		var ierr *goja.InterruptedError
		if errors.As(err, &ierr) || ctx.Err() != nil {
			// The runtime may be in an inconsistent state; do not reuse it.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		healthy = true
		return nil, err
	}
	healthy = true
	return svg, nil
}

// validObject reports whether b is a single, valid JSON object, so that it
// can be spliced into the options document without changing its structure.
func validObject(b []byte) bool {
	t := bytes.TrimSpace(b)
	return len(t) > 0 && t[0] == '{' && json.Valid(t)
}

func encodeRenderOptions(req Request) ([]byte, error) {
	if req.Width < 0 || req.Height < 0 {
		return nil, errors.New("echarts: negative output size")
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, `{"width":%d,"height":%d`, orDefault(req.Width, DefaultWidth), orDefault(req.Height, DefaultHeight))
	if req.UseViewBox {
		b.WriteString(`,"useViewBox":true`)
	}
	if req.Animation {
		b.WriteString(`,"animation":true`)
	}
	if req.RootPath != "" {
		b.WriteString(`,"rootPath":`)
		rp, _ := json.Marshal(req.RootPath)
		b.Write(rp)
	}
	if req.CDNPath != "" {
		b.WriteString(`,"cdnPath":`)
		cp, _ := json.Marshal(req.CDNPath)
		b.Write(cp)
	}
	switch strings.ToUpper(req.Locale) {
	case "":
	case "EN", "ZH":
		fmt.Fprintf(&b, `,"locale":%q`, strings.ToUpper(req.Locale))
	default:
		return nil, fmt.Errorf("echarts: unknown locale %q", req.Locale)
	}
	if len(req.Theme) > 0 {
		if !validObject(req.Theme) {
			return nil, errors.New("echarts: Request.Theme is not a valid JSON object")
		}
		b.WriteString(`,"theme":`)
		b.Write(req.Theme)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func orDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

func (r *Renderer) acquire() (*jsRuntime, error) {
	select {
	case rt := <-r.pool:
		return rt, nil
	default:
	}
	return newJSRuntime(r)
}

func (r *Renderer) release(rt *jsRuntime) {
	rt.reset()
	select {
	case r.pool <- rt:
	default:
		// Pool is full; let the runtime be garbage collected.
	}
}
