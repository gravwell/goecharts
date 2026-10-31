// Copyright (c) 2026, Gravwell, Inc.
// All rights reserved.
//
// Use of this source code is governed by a BSD 2-Clause license that can be
// found in the LICENSE file.

// Command basic renders a bar chart from Go slices and writes bar.svg.
package main

import (
	"context"
	"log"
	"os"

	"github.com/kris-watts-gravwell/goecharts"
)

const option = `{
  "title": {"text": "Sales by product"},
  "tooltip": {},
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
	if err := os.WriteFile("bar.svg", svg, 0o644); err != nil {
		log.Fatal(err)
	}
}
