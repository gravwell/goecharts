// Copyright (c) 2026, Gravwell, Inc.
// All rights reserved.
//
// Use of this source code is governed by a BSD 2-Clause license that can be
// found in the LICENSE file.

package goecharts

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

const testSVGMap = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="200" height="100" viewBox="0 0 200 100">
  <defs>
    <rect id="unit" width="80" height="80"/>
  </defs>
  <style>.room { stroke: #333; }</style>
  <g>
    <rect name="left" class="room" x="10" y="10" width="80" height="80" fill="#eee"/>
    <path name="right" d="M110 10h80v80h-80z" fill="#ddd"/>
    <text x="50" y="55">L&amp;R</text>
    <image xlink:href="#unit" x="0" y="0" width="1" height="1"/>
  </g>
</svg>`

func shimRenderer(t *testing.T) *Renderer {
	t.Helper()
	var pngBytes bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 24, 16))
	img.Set(0, 0, color.White)
	if err := png.Encode(&pngBytes, img); err != nil {
		t.Fatal(err)
	}
	loader := LoaderFunc(func(ctx context.Context, url string) ([]byte, error) {
		switch url {
		case "/geo/plan.svg":
			return []byte(testSVGMap), nil
		case "/img/tile.png":
			return pngBytes.Bytes(), nil
		case "/data/rows.json":
			return []byte(`[["a",1],["b",4],["c",2]]`), nil
		case "https://cdn.test/lib.js":
			return []byte(`(function (global) { global.lib = { double: function (v) { return v.map(function (x) { return x * 2; }); } }; })(this);`), nil
		}
		return nil, fmt.Errorf("not found")
	})
	r, err := New(WithLoader(loader), WithPoolSize(2))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func renderScript(t *testing.T, r *Renderer, script string) string {
	t.Helper()
	svg, err := r.Render(context.Background(), Request{Script: []byte(script), CDNPath: "https://cdn.test/"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return string(svg)
}

func TestShimSVGMap(t *testing.T) {
	r := shimRenderer(t)
	s := renderScript(t, r, `
	  $.get(ROOT_PATH + '/geo/plan.svg', function (svg) {
	    echarts.registerMap('plan', { svg: svg });
	    option = {
	      geo: { map: 'plan', label: { show: true } },
	      series: [{ type: 'map', geoIndex: 0, map: 'plan', data: [{ name: 'left', value: 1 }, { name: 'right', value: 2 }] }]
	    };
	  }, 'text');`)
	for _, want := range []string{"left", "right", "L&amp;R"} {
		if !strings.Contains(s, want) {
			t.Errorf("SVG missing %q", want)
		}
	}
}

func TestParseXMLAttributes(t *testing.T) {
	root, err := parseXML(testSVGMap)
	if err != nil {
		t.Fatal(err)
	}
	var use *xmlNode
	var walk func(n *xmlNode)
	walk = func(n *xmlNode) {
		if n.typ == "element" && n.name == "image" {
			use = n
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(root)
	if use == nil {
		t.Fatal("no <image> element")
	}
	got := map[string]string{}
	for _, a := range use.attrs {
		got[a[0]] = a[1]
	}
	if got["xlink:href"] != "#unit" {
		t.Errorf("xlink:href = %q, want #unit; attrs %v", got["xlink:href"], use.attrs)
	}
	if _, err := parseXML("<svg><g></svg>"); err == nil {
		t.Error("mismatched tags parsed without error")
	}
	root, err = parseXML(`<!DOCTYPE svg [<!ENTITY ns_ai "http://ns.adobe.com/AdobeIllustrator/10.0/">]><svg xmlns:i="&ns_ai;"><g i:extraneous="self"/></svg>`)
	if err != nil {
		t.Errorf("DOCTYPE entity: %v", err)
	} else if a := root.children[0].attrs; len(a) != 1 || a[0][1] != "http://ns.adobe.com/AdobeIllustrator/10.0/" {
		t.Errorf("entity not expanded: %v", a)
	}
}

func TestShimWindowAndTimers(t *testing.T) {
	r := shimRenderer(t)
	svg, err := r.Render(context.Background(), Request{Width: 640, Height: 320, Script: []byte(`
	  window.addEventListener('resize', function () {});
	  const w = window.innerWidth, h = window.innerHeight;
	  let data = structuredClone([1, 2, 3]);
	  setTimeout(function () {
	    data.push(w);
	    setTimeout(function () { data.push(h); }, 5);
	  }, 10);
	  setInterval(function () { data.push(99); }, 1);
	  setTimeout(function () {
	    myChart.setOption({ title: { text: 'sizes ' + data.join('/') }, xAxis: {}, yAxis: {}, series: [{ type: 'bar', data: data }] });
	  }, 20);`)})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(svg), "sizes 1/2/3/99/640/320") {
		t.Errorf("timers did not run in due order; SVG title: %s", titleOf(svg))
	}
}

func TestShimTimerLimit(t *testing.T) {
	r := shimRenderer(t)
	svg, err := r.Render(context.Background(), Request{Script: []byte(`
	  let n = 0;
	  function tick() {
	    n++;
	    myChart.setOption({ title: { text: 'ticks ' + n }, series: [] });
	    setTimeout(tick, 1);
	  }
	  tick();`)})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var n int
	if _, err := fmt.Sscanf(titleOf(svg)[strings.LastIndex(titleOf(svg), "ticks "):], "ticks %d", &n); err != nil || n < 100 || n > 2000 {
		t.Errorf("endless timer chain ran %d times (err %v); SVG title: %s", n, err, titleOf(svg))
	}
}

func TestShimImage(t *testing.T) {
	r := shimRenderer(t)
	s := renderScript(t, r, `
	  const img = new Image();
	  img.onload = function () {
	    option = {
	      title: { text: 'img ' + img.width + 'x' + img.height },
	      series: [{ type: 'pie', data: [{ value: 1, name: 'a' }], itemStyle: { color: { image: img, repeat: 'repeat' } } }]
	    };
	  };
	  img.src = ROOT_PATH + '/img/tile.png';`)
	if !strings.Contains(s, "img 24x16") {
		t.Errorf("image size not reported; SVG title: %s", titleOf([]byte(s)))
	}
	if !strings.Contains(s, "/img/tile.png") {
		t.Error("pattern image source not in SVG")
	}
	s = renderScript(t, r, `
	  option = { graphic: [{ type: 'image', style: { image: 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAADCAYAAAC56t6BAAAADUlEQVR4nGNgYGD4DwABBAEAX+XDSgAAAABJRU5ErkJggg==', x: 10, y: 10, width: 20, height: 30 } }], series: [] };`)
	if !strings.Contains(s, `<image href="data:image/png;base64,`) || !strings.Contains(s, `width="20"`) {
		t.Errorf("graphic image not drawn:\n%s", s)
	}
}

func TestShimXHRAndGetScript(t *testing.T) {
	r := shimRenderer(t)
	s := renderScript(t, r, `
	  $.getScript(CDN_PATH + 'lib.js').done(function () {
	    const xhr = new XMLHttpRequest();
	    xhr.open('GET', ROOT_PATH + '/data/rows.json');
	    xhr.responseType = 'json';
	    xhr.onload = function () {
	      const rows = xhr.response;
	      myChart.setOption({
	        title: { text: 'status ' + xhr.status + ' doubled ' + lib.double(rows.map(function (r) { return r[1]; })).join(',') },
	        xAxis: { type: 'category' }, yAxis: {},
	        series: [{ type: 'bar', data: rows.map(function (r) { return r[1]; }) }]
	      });
	    };
	    xhr.send();
	  });`)
	if !strings.Contains(s, "status 200 doubled 2,8,4") {
		t.Errorf("SVG title: %s", titleOf([]byte(s)))
	}
}

func TestShimWhenSemantics(t *testing.T) {
	r := shimRenderer(t)
	s := renderScript(t, r, `
	  $.when($.getJSON(ROOT_PATH + '/data/rows.json'), $.get(ROOT_PATH + '/data/rows.json', null, null, 'text')).done(function (res0, res1) {
	    option = { title: { text: 'n ' + res0[0].length + ' ' + res0[1] + ' ' + typeof res1[0] }, series: [] };
	  });
	  $.when($.getJSON(ROOT_PATH + '/data/rows.json')).done(function (rows) {
	    if (!Array.isArray(rows)) { throw new Error('single when should pass data'); }
	  });`)
	if !strings.Contains(s, "n 3 success string") {
		t.Errorf("SVG title: %s", titleOf([]byte(s)))
	}
}

func TestShimTimerAfterOption(t *testing.T) {
	r := shimRenderer(t)
	s := renderScript(t, r, `
	  option = { title: { text: 'initial' }, xAxis: { type: 'category', data: ['a', 'b'] }, yAxis: {}, series: [{ type: 'bar', data: [1, 2] }] };
	  setInterval(function () {
	    myChart.setOption({ title: { text: 'updated' }, series: [{ data: [3, 4] }] });
	  }, 1000);
	  $(window).on('resize', function () { myChart.resize(); });`)
	if !strings.Contains(s, "updated") {
		t.Errorf("update from a timer was not merged; SVG title: %s", titleOf([]byte(s)))
	}
}

func TestShimCanvasWatermark(t *testing.T) {
	r := shimRenderer(t)
	s := renderScript(t, r, `
	  const canvas = document.createElement('canvas');
	  const ctx = canvas.getContext('2d');
	  canvas.width = canvas.height = 100;
	  ctx.textAlign = 'center';
	  ctx.fillText('draft', 50, 50);
	  option = { backgroundColor: { image: canvas, repeat: 'repeat' }, title: { text: 'wm ' + ctx.measureText('draft').width.toFixed(0) }, series: [] };`)
	if !strings.Contains(s, "wm ") || strings.Contains(s, "wm 0") {
		t.Errorf("SVG title: %s", titleOf([]byte(s)))
	}
}

func titleOf(svg []byte) string {
	s := string(svg)
	i := strings.Index(s, "<text")
	if i < 0 {
		return "(no text)"
	}
	j := strings.Index(s[i:], "</text>")
	if j < 0 {
		return s[i:]
	}
	return s[i : i+j]
}
