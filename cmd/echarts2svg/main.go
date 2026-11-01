// Copyright (c) 2026, Gravwell, Inc.
// All rights reserved.
//
// Use of this source code is governed by a BSD 2-Clause license that can be
// found in the LICENSE file.

// Command echarts2svg renders an ECharts option file to SVG.
//
//	echarts2svg [-o chart.svg] [-w 800 -h 500] [-data id=file.csv]... option.json
//
// Each -data flag injects a CSV file as the dataset with the given id. The
// first CSV row names the columns; numbers and RFC 3339 timestamps are
// recognised, everything else stays a string.
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kris-watts-gravwell/goecharts"
)

type dataFlags map[string]string

func (d dataFlags) String() string { return fmt.Sprint(map[string]string(d)) }
func (d dataFlags) Set(v string) error {
	id, file, ok := strings.Cut(v, "=")
	if !ok || id == "" || file == "" {
		return fmt.Errorf("want id=file.csv, got %q", v)
	}
	d[id] = file
	return nil
}

func main() {
	var (
		out      = flag.String("o", "", "output file (default stdout)")
		width    = flag.Int("w", goecharts.DefaultWidth, "width in pixels")
		height   = flag.Int("h", goecharts.DefaultHeight, "height in pixels")
		theme    = flag.String("theme", "", "theme JSON file")
		locale   = flag.String("locale", "", "component text locale: EN or ZH")
		viewBox  = flag.Bool("viewbox", false, "emit a viewBox instead of fixed width/height")
		animate  = flag.Bool("animation", false, "keep entry animations as CSS keyframes")
		verbose  = flag.Bool("v", false, "print ECharts console output to stderr")
		timeout  = flag.Duration("timeout", 30*time.Second, "render timeout")
		datasets = dataFlags{}
	)
	flag.Var(datasets, "data", "inject a CSV file as dataset `id=file.csv` (repeatable)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: echarts2svg [flags] option.json\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	option, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fatal(err)
	}
	req := goecharts.Request{
		Option:     option,
		Width:      *width,
		Height:     *height,
		Locale:     *locale,
		UseViewBox: *viewBox,
		Animation:  *animate,
		Data:       map[string]goecharts.Dataset{},
	}
	if *theme != "" {
		if req.Theme, err = os.ReadFile(*theme); err != nil {
			fatal(err)
		}
	}
	for id, file := range datasets {
		ds, err := readCSV(file)
		if err != nil {
			fatal(fmt.Errorf("%s: %w", file, err))
		}
		req.Data[id] = ds
	}

	var opts []goecharts.Option
	opts = append(opts, goecharts.WithTimeout(*timeout), goecharts.WithPoolSize(1))
	if *verbose {
		opts = append(opts, goecharts.WithLogger(func(level, msg string) {
			fmt.Fprintf(os.Stderr, "echarts %s: %s\n", level, msg)
		}))
	}
	r, err := goecharts.New(opts...)
	if err != nil {
		fatal(err)
	}
	svg, err := r.Render(context.Background(), req)
	if err != nil {
		fatal(err)
	}
	if *out == "" {
		os.Stdout.Write(svg)
		return
	}
	if err := os.WriteFile(*out, svg, 0o644); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "echarts2svg:", err)
	os.Exit(1)
}

// readCSV reads a CSV file with a header row into a Dataset. Each column is
// typed from its values: all numbers, all RFC 3339 timestamps, else strings.
// Empty cells become null in numeric and time columns.
func readCSV(path string) (goecharts.Dataset, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return goecharts.Dataset{}, err
	}
	rd := csv.NewReader(bytes.NewReader(b))
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true
	header, err := rd.Read()
	if err != nil {
		return goecharts.Dataset{}, fmt.Errorf("reading header: %w", err)
	}
	cols := make([][]string, len(header))
	for {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return goecharts.Dataset{}, err
		}
		for i := range header {
			v := ""
			if i < len(rec) {
				v = rec[i]
			}
			cols[i] = append(cols[i], v)
		}
	}
	ds := goecharts.Dataset{}
	for i, name := range header {
		ds.Columns = append(ds.Columns, goecharts.Col(name, typeColumn(cols[i])))
	}
	return ds, nil
}

func typeColumn(vals []string) any {
	nums := make([]any, len(vals))
	times := make([]any, len(vals))
	isNum, isTime := len(vals) > 0, len(vals) > 0
	for i, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			nums[i], times[i] = nil, nil
			continue
		}
		if isNum {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				nums[i] = f
			} else {
				isNum = false
			}
		}
		if isTime {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				times[i] = t
			} else {
				isTime = false
			}
		}
		if !isNum && !isTime {
			break
		}
	}
	switch {
	case isNum:
		return nums
	case isTime:
		return times
	default:
		return vals
	}
}
