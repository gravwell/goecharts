// Copyright (c) 2026, Gravwell, Inc.
// All rights reserved.
//
// Use of this source code is governed by a BSD 2-Clause license that can be
// found in the LICENSE file.

package goecharts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	testRendererOnce sync.Once
	testRenderer     *Renderer
	testRendererErr  error
)

func sharedRenderer(t *testing.T) *Renderer {
	t.Helper()
	testRendererOnce.Do(func() {
		testRenderer, testRendererErr = New()
	})
	if testRendererErr != nil {
		t.Fatalf("New: %v", testRendererErr)
	}
	return testRenderer
}

const barOption = `{
  "title": {"text": "Sales"},
  "xAxis": {"type": "category"},
  "yAxis": {},
  "series": [{"type": "bar", "encode": {"x": "product", "y": "amount"}}]
}`

func salesData() map[string]Dataset {
	return map[string]Dataset{
		"sales": NewDataset(
			Col("product", []string{"Matcha", "Milk Tea", "Cheese Cocoa", "Walnut Brownie"}),
			Col("amount", []float64{43.3, 83.1, 86.4, 72.4}),
		),
	}
}

func TestRenderBar(t *testing.T) {
	r := sharedRenderer(t)
	svg, err := r.RenderSVG(context.Background(), []byte(barOption), salesData())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(svg)
	for _, want := range []string{"<svg", `width="600"`, `height="400"`, "Sales", "Walnut Brownie", "<path"} {
		if !strings.Contains(s, want) {
			t.Errorf("SVG missing %q", want)
		}
	}
	if strings.Contains(s, "@keyframes") {
		t.Errorf("SVG contains animation keyframes; animation should be disabled by default")
	}
	writeTestOutput(t, "bar.svg", svg)
}

func TestRenderInlineData(t *testing.T) {
	r := sharedRenderer(t)
	option := `{
	  "xAxis": {"type": "category", "data": ["Mon", "Tue", "Wed"]},
	  "yAxis": {"type": "value"},
	  "series": [{"type": "line", "data": [150, 230, 224], "smooth": true}]
	}`
	svg, err := r.Render(context.Background(), Request{Option: []byte(option), Width: 320, Height: 200})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(svg)
	for _, want := range []string{`width="320"`, `height="200"`, "Tue"} {
		if !strings.Contains(s, want) {
			t.Errorf("SVG missing %q", want)
		}
	}
}

func TestDatasetMatchesExistingID(t *testing.T) {
	r := sharedRenderer(t)
	option := `{
	  "dataset": [{"id": "raw", "source": [["x", "y"], ["old", 1]]},
	              {"id": "sorted", "fromDatasetId": "raw", "transform": {"type": "sort", "config": {"dimension": "y", "order": "desc"}}}],
	  "xAxis": {"type": "category"},
	  "yAxis": {},
	  "series": [{"type": "bar", "datasetId": "sorted"}]
	}`
	data := map[string]Dataset{"raw": NewDataset(Col("x", []string{"first", "second"}), Col("y", []int{2, 9}))}
	svg, err := r.RenderSVG(context.Background(), []byte(option), data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(svg)
	if strings.Contains(s, ">old<") {
		t.Errorf("original source was not replaced")
	}
	if !strings.Contains(s, "second") {
		t.Errorf("injected rows missing from output")
	}
}

func TestTimeColumn(t *testing.T) {
	r := sharedRenderer(t)
	option := `{
	  "xAxis": {"type": "time"},
	  "yAxis": {},
	  "series": [{"type": "line", "encode": {"x": "t", "y": "v"}}]
	}`
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	ts := []time.Time{base, base.Add(24 * time.Hour), base.Add(48 * time.Hour)}
	data := map[string]Dataset{"series": NewDataset(Col("t", ts), Col("v", []float64{1, 3, 2}))}
	svg, err := r.RenderSVG(context.Background(), []byte(option), data)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(svg), "Mar") && !strings.Contains(string(svg), "03") {
		t.Errorf("time axis labels missing")
	}
}

func TestTheme(t *testing.T) {
	r := sharedRenderer(t)
	theme := `{"backgroundColor": "#123456", "color": ["#abcdef"]}`
	svg, err := r.Render(context.Background(), Request{Option: []byte(barOption), Data: salesData(), Theme: []byte(theme)})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(svg)
	if !strings.Contains(s, "#123456") || !strings.Contains(s, "#abcdef") {
		t.Errorf("theme colors not applied")
	}
}

func TestUseViewBox(t *testing.T) {
	r := sharedRenderer(t)
	svg, err := r.Render(context.Background(), Request{Option: []byte(barOption), Data: salesData(), UseViewBox: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(svg), "viewBox=") {
		t.Errorf("viewBox missing")
	}
}

func TestBadOption(t *testing.T) {
	r := sharedRenderer(t)
	for _, opt := range []string{"", "   ", "{not json", "[1,2]"} {
		if _, err := r.RenderSVG(context.Background(), []byte(opt), nil); err == nil {
			t.Errorf("option %q: expected error", opt)
		}
	}
	var e *Error
	if _, err := r.RenderSVG(context.Background(), []byte("{not json"), nil); !errors.As(err, &e) {
		t.Errorf("malformed option: expected *Error, got %T %v", err, err)
	}
	// The production ECharts build ignores an unknown series type without
	// logging, as a browser would, so the render succeeds with an empty chart.
	if _, err := r.RenderSVG(context.Background(), []byte(`{"series":[{"type":"nosuchseries","data":[1]}]}`), nil); err != nil {
		t.Errorf("unknown series type: %v", err)
	}
}

func TestContextCancel(t *testing.T) {
	r := sharedRenderer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.RenderSVG(ctx, []byte(barOption), salesData()); !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestConcurrent(t *testing.T) {
	r := sharedRenderer(t)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.RenderSVG(context.Background(), []byte(barOption), salesData()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestVersion(t *testing.T) {
	if Version() == "" {
		t.Fatal("empty version")
	}
}

func writeTestOutput(t *testing.T, name string, b []byte) {
	t.Helper()
	dir := os.Getenv("GOECHARTS_TEST_OUT")
	if dir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}
