// Copyright (c) 2026, Gravwell, Inc.
// All rights reserved.
//
// Use of this source code is governed by a BSD 2-Clause license that can be
// found in the LICENSE file.

package goecharts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func scriptRenderer(t *testing.T) *Renderer {
	t.Helper()
	loader := LoaderFunc(func(ctx context.Context, url string) ([]byte, error) {
		switch url {
		case "/data/points.json":
			return []byte(`[{"name":"a","value":3},{"name":"b","value":5}]`), nil
		case "/data/words.txt":
			return []byte("x,y\n1,2\n"), nil
		}
		return nil, fmt.Errorf("not found")
	})
	r, err := New(WithLoader(loader), WithPoolSize(2))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestScriptAssignsOption(t *testing.T) {
	r := scriptRenderer(t)
	script := `
	  const days = ['Mon', 'Tue', 'Wed'];
	  option = {
	    xAxis: {type: 'category', data: days},
	    yAxis: {axisLabel: {formatter: function (v) { return v + ' units'; }}},
	    series: [{type: 'bar', data: [1, 2, 3], itemStyle: {color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [{offset: 0, color: '#f00'}, {offset: 1, color: '#00f'}])}}]
	  };`
	svg, err := r.Render(context.Background(), Request{Script: []byte(script)})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	s := string(svg)
	for _, want := range []string{"units", "linearGradient", "Wed"} {
		if !strings.Contains(s, want) {
			t.Errorf("SVG missing %q", want)
		}
	}
}

func TestScriptLoadsData(t *testing.T) {
	r := scriptRenderer(t)
	script := `
	  myChart.showLoading();
	  $.get(ROOT_PATH + '/points.json', function (data) {
	    myChart.hideLoading();
	    myChart.setOption((option = {
	      series: [{type: 'pie', data: data, label: {formatter: '{b}: {c}'}}]
	    }));
	  });`
	svg, err := r.Render(context.Background(), Request{Script: []byte(script), RootPath: "/data"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(svg), "b: 5") {
		t.Errorf("loaded data missing from output")
	}
}

func TestScriptFetchPromise(t *testing.T) {
	r := scriptRenderer(t)
	script := `
	  fetch('/data/points.json').then(function (r) { return r.json(); }).then(function (data) {
	    myChart.setOption({series: [{type: 'funnel', data: data}]});
	  });`
	svg, err := r.Render(context.Background(), Request{Script: []byte(script)})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(svg), ">b<") && !strings.Contains(string(svg), "b</text>") {
		t.Errorf("fetched data missing from output")
	}
}

func TestScriptErrors(t *testing.T) {
	r := scriptRenderer(t)
	cases := map[string]string{
		"syntax":    `option = {`,
		"throws":    `throw new Error('boom')`,
		"no option": `var x = 1;`,
		"missing":   `$.get('/nope.json', function () {})`,
	}
	for name, script := range cases {
		_, err := r.Render(context.Background(), Request{Script: []byte(script)})
		if _, ok := errors.AsType[*Error](err); !ok {
			t.Errorf("%s: expected *Error, got %v", name, err)
		}
	}
	if _, err := r.Render(context.Background(), Request{Script: []byte("option={}"), Option: []byte("{}")}); err == nil {
		t.Error("both Option and Script: expected error")
	}
	noLoader := sharedRenderer(t)
	if _, err := noLoader.Render(context.Background(), Request{Script: []byte(`$.get('/x', function(){})`)}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("without loader: expected loading-disabled error, got %v", err)
	}
	// A runtime that failed a script must still render afterwards.
	if _, err := r.Render(context.Background(), Request{Option: []byte(barOption), Data: salesData()}); err != nil {
		t.Errorf("render after failures: %v", err)
	}
}
