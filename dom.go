// Copyright (c) 2026, Gravwell, Inc.
// All rights reserved.
//
// Use of this source code is governed by a BSD 2-Clause license that can be
// found in the LICENSE file.

package goecharts

import (
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/dop251/goja"
)

// Namespace URIs that SVG documents commonly use with a prefix. The parser
// hands attribute names back with the prefix the document would show, which
// is what ECharts looks up (`xlink:href`).
var xmlPrefixes = map[string]string{
	"http://www.w3.org/1999/xlink":         "xlink",
	"http://www.w3.org/XML/1998/namespace": "xml",
	"http://www.w3.org/2000/svg":           "",
}

// hostParseXML(text) parses an XML document and returns a plain tree that
// post.js wraps in a minimal DOM for ECharts' SVG map parser:
//
//	{type: "document", children: [...]}
//	{type: "element", name: "path", attrs: [["d", "..."], ...], children: [...]}
//	{type: "text", text: "..."}
//
// Comments, processing instructions and the DOCTYPE are dropped.
func (rt *jsRuntime) hostParseXML(call goja.FunctionCall) goja.Value {
	root, err := parseXML(call.Argument(0).String())
	if err != nil {
		panic(rt.vm.NewGoError(err))
	}
	return rt.xmlToJS(root)
}

type xmlNode struct {
	typ      string
	name     string
	attrs    [][2]string
	children []*xmlNode
	text     string
}

// entityDecl matches an internal-subset entity declaration, as drawing
// tools put in an SVG's DOCTYPE for their namespace URIs.
var entityDecl = regexp.MustCompile(`<!ENTITY\s+([A-Za-z_][\w.:-]*)\s+(?:"([^"]*)"|'([^']*)')\s*>`)

func parseXML(text string) (*xmlNode, error) {
	d := xml.NewDecoder(strings.NewReader(text))
	d.Entity = xml.HTMLEntity
	if decls := entityDecl.FindAllStringSubmatch(text, -1); len(decls) > 0 {
		entities := make(map[string]string, len(xml.HTMLEntity)+len(decls))
		for k, v := range xml.HTMLEntity {
			entities[k] = v
		}
		for _, m := range decls {
			entities[m[1]] = m[2] + m[3]
		}
		d.Entity = entities
	}
	root := &xmlNode{typ: "document"}
	stack := []*xmlNode{root}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing XML: %w", err)
		}
		cur := stack[len(stack)-1]
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xmlNode{typ: "element", name: t.Name.Local}
			for _, a := range t.Attr {
				n.attrs = append(n.attrs, [2]string{qualifiedName(a.Name), a.Value})
			}
			cur.children = append(cur.children, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			s := string(t)
			if k := len(cur.children); k > 0 && cur.children[k-1].typ == "text" {
				cur.children[k-1].text += s
			} else {
				cur.children = append(cur.children, &xmlNode{typ: "text", text: s})
			}
		}
	}
	return root, nil
}

func qualifiedName(n xml.Name) string {
	switch {
	case n.Space == "":
		return n.Local
	case n.Space == "xmlns":
		return "xmlns:" + n.Local
	}
	if p, ok := xmlPrefixes[n.Space]; ok {
		if p == "" {
			return n.Local
		}
		return p + ":" + n.Local
	}
	if strings.Contains(n.Space, "/") {
		// An unknown namespace URI; the prefix is lost, keep the local name.
		return n.Local
	}
	return n.Space + ":" + n.Local
}

func (rt *jsRuntime) xmlToJS(n *xmlNode) goja.Value {
	vm := rt.vm
	o := vm.NewObject()
	_ = o.Set("type", n.typ)
	switch n.typ {
	case "text":
		_ = o.Set("text", n.text)
		return o
	case "element":
		_ = o.Set("name", n.name)
		attrs := make([]interface{}, len(n.attrs))
		for i, a := range n.attrs {
			attrs[i] = []interface{}{a[0], a[1]}
		}
		_ = o.Set("attrs", vm.ToValue(attrs))
	}
	children := make([]interface{}, len(n.children))
	for i, c := range n.children {
		children[i] = rt.xmlToJS(c)
	}
	_ = o.Set("children", vm.NewArray(children...))
	return o
}
