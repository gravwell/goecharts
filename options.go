// Copyright (c) 2026, Gravwell, Inc.
// All rights reserved.
//
// Use of this source code is governed by a BSD 2-Clause license that can be
// found in the LICENSE file.

package goecharts

import (
	"context"
	"time"
)

// Loader resolves URLs that a script (Request.Script) loads through `$.get`,
// `$.getJSON`, `$.ajax` or `fetch`. Loads are synchronous from the script's
// point of view and happen on the rendering goroutine; a Loader must be safe
// for concurrent use because several renders may run at once.
type Loader interface {
	Load(ctx context.Context, url string) ([]byte, error)
}

// LoaderFunc adapts a function to the Loader interface.
type LoaderFunc func(ctx context.Context, url string) ([]byte, error)

// Load calls f.
func (f LoaderFunc) Load(ctx context.Context, url string) ([]byte, error) { return f(ctx, url) }

// LogFunc receives console output emitted by the ECharts runtime, such as
// warnings about unknown series types or deprecated options. level is the
// console method used: "error", "warn", "info", "log" or "debug".
type LogFunc func(level string, msg string)

type options struct {
	poolSize int
	timeout  time.Duration
	log      LogFunc
	measurer TextMeasurer
	loader   Loader

	failOnError bool
}

// Option configures a Renderer.
type Option func(*options)

// WithPoolSize sets the maximum number of idle JavaScript runtimes kept for
// reuse. Each runtime holds a loaded copy of ECharts and serves one render at
// a time; when the pool is empty a new runtime is created on demand and
// discarded again if the pool is full. The default is runtime.NumCPU().
func WithPoolSize(n int) Option {
	return func(o *options) {
		if n > 0 {
			o.poolSize = n
		}
	}
}

// WithTimeout bounds the wall-clock time of a single render, independent of
// the caller's context. The default is 30 seconds; a negative value disables
// the built-in limit.
func WithTimeout(d time.Duration) Option {
	return func(o *options) { o.timeout = d }
}

// WithLoader enables scripts to load data by URL through l. Without a
// loader every load fails, because fetching arbitrary URLs named in an
// untrusted script is a request-forgery risk.
func WithLoader(l Loader) Option {
	return func(o *options) { o.loader = l }
}

// WithLogger forwards ECharts console output to fn. Without a logger all
// output is discarded.
func WithLogger(fn LogFunc) Option {
	return func(o *options) { o.log = fn }
}

// WithTextMeasurer replaces the text width function used for layout. See
// DefaultTextMeasurer.
func WithTextMeasurer(m TextMeasurer) Option {
	return func(o *options) {
		if m != nil {
			o.measurer = m
		}
	}
}

// WithFailOnError controls what happens when ECharts logs at error level
// while processing an option (for example a series that refers to a
// component that does not exist). ECharts itself only logs such problems and
// draws what it can, often an empty chart. By default this package turns
// them into a render failure; pass false to get the lenient behavior and
// receive the messages through WithLogger instead.
func WithFailOnError(fail bool) Option {
	return func(o *options) { o.failOnError = fail }
}
