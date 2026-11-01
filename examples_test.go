// Copyright (c) 2026, Gravwell, Inc.
// All rights reserved.
//
// Use of this source code is governed by a BSD 2-Clause license that can be
// found in the LICENSE file.

package goecharts

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestExamples renders every option under testdata/examples and checks that
// the result is well-formed SVG with drawn content. Set GOECHARTS_TEST_OUT
// to a directory to keep the SVGs for inspection.
func TestExamples(t *testing.T) {
	files, err := filepath.Glob("testdata/examples/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no examples found")
	}
	r := sharedRenderer(t)
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			option, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			svg, err := r.Render(context.Background(), Request{Option: option, Width: 800, Height: 500})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			t.Logf("%d bytes in %v", len(svg), time.Since(start).Round(time.Millisecond))
			writeTestOutput(t, name+".svg", svg)
			checkSVG(t, svg)
		})
	}
}

// checkSVG parses the document and requires at least one drawn element.
func checkSVG(t *testing.T, svg []byte) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(string(svg)))
	drawn := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("SVG is not well-formed XML: %v", err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			switch se.Name.Local {
			case "path", "rect", "circle", "ellipse", "line", "polyline", "polygon", "text", "image":
				drawn++
			}
		}
	}
	if drawn < 3 {
		t.Errorf("SVG has only %d drawn elements", drawn)
	}
}
