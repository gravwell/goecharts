// Copyright (c) 2026, Gravwell, Inc.
// All rights reserved.
//
// Use of this source code is governed by a BSD 2-Clause license that can be
// found in the LICENSE file.

// post.js runs after the ECharts bundle has been evaluated and defines the
// __goecharts API that the Go side calls into.
(function (global) {
  'use strict';
  var echarts = global.echarts;
  var host = global.__goecharts_host;

  if (!echarts || typeof echarts.init !== 'function') {
    throw new Error('goecharts: the ECharts bundle did not define echarts.init');
  }

  // Text measurement. zrender measures text through a platform hook, which
  // on Node.js falls back to a crude per-character estimate when no canvas
  // exists. Route it to the Go host's font metric tables instead so that
  // axis labels, legends and overflow handling are laid out with widths that
  // match what a viewer will draw.
  echarts.setPlatformAPI({
    createCanvas: function () { return null; },
    measureText: function (text, font) {
      return { width: host.measureText(String(text), font || '12px sans-serif') };
    }
  });

  function errorText(e) {
    if (e == null) { return 'unknown error'; }
    if (e instanceof Error) { return e.message || String(e); }
    return String(e);
  }

  // toRows transposes a column-oriented dataset {col: [v0, v1, ...]} into an
  // array of row objects. ECharts accepts a column-keyed object as a source
  // too, but dataset transforms (sort, filter, ...) only operate on row
  // formats, so rows are the safe choice. Dimension names and their order
  // come from the keys of the first row, which keep the order Go sent.
  function toRows(columns) {
    var names = Object.keys(columns);
    if (names.length === 0) { return []; }
    var n = columns[names[0]].length;
    var rows = new Array(n);
    for (var i = 0; i < n; i++) {
      var row = {};
      for (var c = 0; c < names.length; c++) {
        row[names[c]] = columns[names[c]][i];
      }
      rows[i] = row;
    }
    return rows;
  }

  // inject makes the host datasets available to the option. Each dataset is
  // matched to an entry of option.dataset by id; a match has its source
  // replaced (its dimensions and other properties are kept), and an
  // unmatched name is appended as a new dataset with that id. When the
  // option declares no dataset at all and exactly one dataset is supplied,
  // it becomes the first (index 0) dataset, which series use by default.
  function inject(option, datasets) {
    var names = Object.keys(datasets);
    if (names.length === 0) { return; }
    var list = option.dataset;
    if (list == null) { list = []; }
    else if (!Array.isArray(list)) { list = [list]; }
    for (var i = 0; i < names.length; i++) {
      var name = names[i];
      var src = toRows(datasets[name]);
      var found = null;
      for (var j = 0; j < list.length; j++) {
        if (list[j] && String(list[j].id) === name) { found = list[j]; break; }
      }
      if (found) {
        found.source = src;
        // The host data replaces whatever produced this dataset before,
        // but a dimensions list still names the columns.
        delete found.fromDatasetId;
        delete found.fromDatasetIndex;
        delete found.transform;
        continue;
      }
      list.push({ id: name, source: src });
    }
    option.dataset = list;
  }

  // settleForceLayouts makes graph series with a force layout run their
  // simulation to convergence inside setOption. ECharts normally advances the
  // simulation one step per animation frame, and no frames run on a server,
  // so the nodes would otherwise stay at their random starting positions.
  // An explicit layoutAnimation setting is respected.
  function settleForceLayouts(option) {
    var series = option.series;
    if (series == null) { return; }
    if (!Array.isArray(series)) { series = [series]; }
    for (var i = 0; i < series.length; i++) {
      var s = series[i];
      if (!s || s.type !== 'graph' || s.layout !== 'force') { continue; }
      if (!s.force || typeof s.force !== 'object') { s.force = {}; }
      if (s.force.layoutAnimation === undefined) { s.force.layoutAnimation = false; }
    }
  }

  // prepareOption applies the server-side defaults to an option about to be
  // set on a chart. Entry animations would otherwise be emitted as CSS
  // keyframes in the SVG, which an offline consumer cannot play; the chart's
  // final state is what we want. An explicit option property still wins.
  function prepareOption(option, opts) {
    if (!option || typeof option !== 'object') { return; }
    if (opts.animation !== true && option.animation === undefined) {
      option.animation = false;
    }
    settleForceLayouts(option);
  }

  // newChart creates a server-side chart instance for one render.
  function newChart(opts) {
    var initOpts = {
      renderer: 'svg',
      ssr: true,
      width: opts.width || 600,
      height: opts.height || 400
    };
    if (opts.locale) { initOpts.locale = opts.locale; }
    if (opts.devicePixelRatio) { initOpts.devicePixelRatio = opts.devicePixelRatio; }
    return echarts.init(null, opts.theme || null, initOpts);
  }

  // render renders one JSON option to an SVG string. Unlike a browser render
  // this is synchronous: in server-side rendering mode ECharts lays out and
  // paints the whole scene inside setOption, and renderToSVGString
  // serializes it.
  function render(optionText, datasetsText, optsText) {
    var result = { svg: null, error: null };
    var chart = null;
    try {
      var option = JSON.parse(optionText);
      var opts = optsText ? JSON.parse(optsText) : {};
      var datasets = datasetsText ? JSON.parse(datasetsText) : {};
      if (!option || typeof option !== 'object' || Array.isArray(option)) {
        throw new Error('option must be a JSON object');
      }
      inject(option, datasets);
      prepareOption(option, opts);
      chart = newChart(opts);
      chart.setOption(option, { notMerge: true, lazyUpdate: false, silent: true });
      result.svg = chart.renderToSVGString({ useViewBox: !!opts.useViewBox });
    } catch (e) {
      result.error = errorText(e);
    } finally {
      if (chart) { try { chart.dispose(); } catch (e2) { /* ignore */ } }
    }
    return result;
  }

  // --- script rendering ------------------------------------------------------
  //
  // A script is a JavaScript program written the way the ECharts examples
  // are: it assigns `option`, or calls `myChart.setOption(...)`, possibly
  // after loading data through `$.get` or `fetch`. It runs against the same
  // environment the examples site provides: `echarts`, `myChart`, `app`,
  // `$`, `ROOT_PATH`, `CDN_PATH` and `fetch`, plus the browser globals below.
  // Loads go through the Go host's Loader and complete synchronously, so a
  // callback has run by the time the script returns; promise reactions that
  // have not yet run do so when control returns to Go, which is why
  // rendering a script takes two calls (prepareScript, then finishScript).

  var timers = global.__goecharts_timers;
  var timerLimit = 1000;

  // load fetches url through the host and returns {text, json} where json
  // is the parsed body when it parses, as jQuery's content-type sniffing
  // would deliver it.
  function load(url) {
    var text = host.load(String(url));
    var parsed;
    try { parsed = JSON.parse(text); } catch (e) { parsed = undefined; }
    return { text: text, json: parsed };
  }

  // runScript evaluates source as a script in the global scope, the way a
  // <script> tag would, so that UMD bundles such as d3 or ecStat define
  // their globals.
  function runScript(source) {
    (0, eval)(String(source));
  }

  function microtask(fn) { Promise.resolve().then(fn); }

  // --- browser globals -------------------------------------------------------
  //
  // These exist for scripts, not for ECharts, which chose its server-side
  // code paths when the bundle loaded and does not look again.

  function noop() {}

  // DOMParser parses XML in Go and wraps the tree in the subset of the DOM
  // that ECharts' SVG map parser walks: nodeType, nodeName, attributes,
  // getAttribute, child and sibling links and textContent.
  function makeNode(raw, parent, doc) {
    var node = {
      nodeType: raw.type === 'element' ? 1 : raw.type === 'text' ? 3 : 9,
      nodeName: raw.type === 'element' ? raw.name : raw.type === 'text' ? '#text' : '#document',
      parentNode: parent,
      ownerDocument: doc,
      childNodes: [],
      children: [],
      firstChild: null, lastChild: null, nextSibling: null, previousSibling: null
    };
    if (node.nodeType === 3) {
      node.nodeValue = raw.text;
      node.data = raw.text;
      node.textContent = raw.text;
      return node;
    }
    if (node.nodeType === 1) {
      node.tagName = raw.name;
      node.localName = raw.name;
      var map = {};
      node.attributes = [];
      for (var a = 0; a < raw.attrs.length; a++) {
        var name = raw.attrs[a][0], value = raw.attrs[a][1];
        map[name] = value;
        node.attributes.push({ name: name, value: value, nodeName: name, nodeValue: value });
      }
      node.attributes.length = raw.attrs.length;
      node.getAttribute = function (n) {
        n = String(n);
        if (Object.prototype.hasOwnProperty.call(map, n)) { return map[n]; }
        var colon = n.indexOf(':');
        if (colon >= 0 && Object.prototype.hasOwnProperty.call(map, n.slice(colon + 1))) { return map[n.slice(colon + 1)]; }
        return null;
      };
      node.getAttributeNS = function (ns, n) { return node.getAttribute(n); };
      node.hasAttribute = function (n) { return node.getAttribute(n) !== null; };
      node.id = map.id || '';
      node.className = map['class'] || '';
    }
    var prev = null;
    for (var i = 0; i < raw.children.length; i++) {
      var child = makeNode(raw.children[i], node, doc || node);
      node.childNodes.push(child);
      if (child.nodeType === 1) { node.children.push(child); }
      if (prev) { prev.nextSibling = child; child.previousSibling = prev; }
      prev = child;
    }
    node.firstChild = node.childNodes[0] || null;
    node.lastChild = prev;
    Object.defineProperty(node, 'textContent', {
      get: function () {
        var out = '';
        for (var k = 0; k < node.childNodes.length; k++) { out += node.childNodes[k].textContent; }
        return out;
      }
    });
    node.getElementsByTagName = function (tag) {
      tag = String(tag).toLowerCase();
      var out = [];
      (function walk(n) {
        for (var k = 0; k < n.children.length; k++) {
          var c = n.children[k];
          if (tag === '*' || c.nodeName.toLowerCase() === tag) { out.push(c); }
          walk(c);
        }
      })(node);
      return out;
    };
    node.querySelector = function () { return null; };
    node.querySelectorAll = function () { return []; };
    if (node.nodeType === 9) { node.documentElement = node.children[0] || null; }
    return node;
  }

  function DOMParser() {}
  DOMParser.prototype.parseFromString = function (text) {
    return makeNode(host.parseXML(String(text)), null, null);
  };

  // Image resolves its source through the host loader to learn the image's
  // size, then reports load (or error) asynchronously as a browser would.
  // The SVG keeps the source URL; the viewer loads the pixels.
  function Image(width, height) {
    // zrender keeps DOM nodes by reference when it clones an option and
    // deep-copies everything else, which would lose the src accessor.
    this.nodeType = 1;
    this.ownerDocument = global.document || null;
    this.width = width || 0;
    this.height = height || 0;
    this.naturalWidth = 0;
    this.naturalHeight = 0;
    this.complete = false;
    this.onload = null;
    this.onerror = null;
    this.crossOrigin = null;
    this._src = '';
    this._fixedWidth = !!width;
    this._fixedHeight = !!height;
  }
  Object.defineProperty(Image.prototype, 'src', {
    enumerable: true,
    get: function () { return this._src; },
    set: function (v) { loadInto(this, String(v), false); }
  });
  // loadInto sets an image's source and size. Synchronous loads, used for
  // images named in an option, must not fire callbacks: zrender wires its
  // cache up after loadImage returns, and serializes right after setOption.
  function loadInto(img, src, sync) {
    img._src = src;
    var size = null, err = null;
    try { size = host.imageSize(src); } catch (e) { err = e; }
    function apply() {
      if (err) {
        if (!sync && typeof img.onerror === 'function') { img.onerror(err); }
        return;
      }
      img.naturalWidth = size.width;
      img.naturalHeight = size.height;
      if (!img._fixedWidth) { img.width = size.width; }
      if (!img._fixedHeight) { img.height = size.height; }
      img.complete = true;
      if (!sync && typeof img.onload === 'function') { img.onload({ target: img }); }
    }
    if (sync) { apply(); } else { microtask(apply); }
  }
  Image.prototype.nodeName = 'IMG';
  Image.prototype.addEventListener = function (type, fn) {
    if (type === 'load') { this.onload = fn; }
    if (type === 'error') { this.onerror = fn; }
  };
  Image.prototype.removeEventListener = noop;

  // XMLHttpRequest completes synchronously through the loader and delivers
  // its events on a microtask, so handlers set after send() still run.
  function XMLHttpRequest() {
    this.readyState = 0;
    this.status = 0;
    this.statusText = '';
    this.responseType = '';
    this.responseText = '';
    this.response = null;
    this.responseURL = '';
    this.onload = null;
    this.onerror = null;
    this.onloadend = null;
    this.onreadystatechange = null;
    this._url = '';
  }
  XMLHttpRequest.prototype.open = function (method, url) { this._url = String(url); this.readyState = 1; };
  XMLHttpRequest.prototype.setRequestHeader = noop;
  XMLHttpRequest.prototype.overrideMimeType = noop;
  XMLHttpRequest.prototype.getResponseHeader = function () { return null; };
  XMLHttpRequest.prototype.getAllResponseHeaders = function () { return ''; };
  XMLHttpRequest.prototype.abort = noop;
  XMLHttpRequest.prototype.addEventListener = function (type, fn) { this['on' + type] = fn; };
  XMLHttpRequest.prototype.removeEventListener = noop;
  XMLHttpRequest.prototype.send = function () {
    var self = this;
    var text = null, err = null;
    try { text = host.load(self._url); } catch (e) { err = e; }
    microtask(function () {
      self.readyState = 4;
      self.responseURL = self._url;
      if (err) {
        self.status = 0;
        if (typeof self.onreadystatechange === 'function') { self.onreadystatechange(); }
        if (typeof self.onerror === 'function') { self.onerror(err); }
      } else {
        self.status = 200;
        self.statusText = 'OK';
        self.responseText = text;
        if (self.responseType === 'json') {
          try { self.response = JSON.parse(text); } catch (e) { self.response = null; }
        } else {
          self.response = text;
        }
        if (typeof self.onreadystatechange === 'function') { self.onreadystatechange(); }
        if (typeof self.onload === 'function') { self.onload({ target: self }); }
      }
      if (typeof self.onloadend === 'function') { self.onloadend({ target: self }); }
    });
  };

  // A canvas element whose 2D context accepts any call and measures text
  // through the host, so a script that paints a pattern or watermark runs
  // through; the pattern itself is not produced.
  function makeCanvas() {
    var ctx = new Proxy({
      canvas: null,
      measureText: function (text) { return { width: host.measureText(String(text), this.font || '10px sans-serif') }; },
      getImageData: function (x, y, w, h) { return { width: w, height: h, data: new Uint8ClampedArray(w * h * 4) }; },
      createLinearGradient: function () { return { addColorStop: noop }; },
      createRadialGradient: function () { return { addColorStop: noop }; },
      createPattern: function () { return null; }
    }, {
      get: function (target, key) { return key in target ? target[key] : (typeof key === 'string' ? noop : undefined); }
    });
    var canvas = makeElement('canvas');
    canvas.width = 300;
    canvas.height = 150;
    canvas.getContext = function () { return ctx; };
    canvas.toDataURL = function () { return ''; };
    ctx.canvas = canvas;
    return canvas;
  }

  function makeElement(tag) {
    var el = {
      nodeType: 1, nodeName: String(tag).toUpperCase(), tagName: String(tag).toUpperCase(),
      ownerDocument: null,
      style: {}, childNodes: [], children: [], attributes: [],
      clientWidth: 0, clientHeight: 0, offsetWidth: 0, offsetHeight: 0,
      innerHTML: '', textContent: '',
      appendChild: function (c) { el.childNodes.push(c); return c; },
      removeChild: function (c) { return c; },
      insertBefore: function (c) { return c; },
      setAttribute: noop, removeAttribute: noop,
      getAttribute: function () { return null; },
      addEventListener: noop, removeEventListener: noop, dispatchEvent: noop,
      getBoundingClientRect: function () { return { left: 0, top: 0, right: 0, bottom: 0, width: 0, height: 0, x: 0, y: 0 }; },
      querySelector: function () { return null; },
      querySelectorAll: function () { return []; },
      getElementsByTagName: function () { return []; },
      getContext: function () { return null; },
      focus: noop, blur: noop, click: noop, remove: noop
    };
    return el;
  }

  function makeDocument() {
    var doc = makeElement('#document');
    doc.nodeType = 9;
    doc.nodeName = '#document';
    doc.createElement = function (tag) {
      return String(tag).toLowerCase() === 'canvas' ? makeCanvas() : makeElement(tag);
    };
    doc.createElementNS = function (ns, tag) { return doc.createElement(tag); };
    doc.createTextNode = function (text) { return { nodeType: 3, nodeName: '#text', textContent: String(text), nodeValue: String(text) }; };
    doc.createDocumentFragment = function () { return makeElement('#fragment'); };
    doc.getElementById = function () { return null; };
    doc.getElementsByClassName = function () { return []; };
    doc.body = makeElement('body');
    doc.head = makeElement('head');
    doc.documentElement = makeElement('html');
    doc.documentElement.style = {};
    doc.readyState = 'complete';
    doc.title = '';
    doc.cookie = '';
    doc.location = null;
    return doc;
  }

  function installBrowserGlobals() {
    if (typeof global.window === 'undefined') { global.window = global; }
    if (typeof global.self === 'undefined') { global.self = global; }
    if (typeof global.document === 'undefined') { global.document = makeDocument(); }
    if (typeof global.navigator === 'undefined') {
      global.navigator = { userAgent: 'goecharts', language: 'en-US', languages: ['en-US'], platform: 'server' };
    }
    if (typeof global.devicePixelRatio === 'undefined') { global.devicePixelRatio = 1; }
    if (typeof global.innerWidth === 'undefined') { global.innerWidth = 600; }
    if (typeof global.innerHeight === 'undefined') { global.innerHeight = 400; }
    if (typeof global.addEventListener !== 'function') {
      global.addEventListener = noop;
      global.removeEventListener = noop;
      global.dispatchEvent = noop;
    }
    if (typeof global.getComputedStyle !== 'function') { global.getComputedStyle = function () { return {}; }; }
    if (typeof global.matchMedia !== 'function') {
      global.matchMedia = function (q) { return { matches: false, media: String(q), addEventListener: noop, removeEventListener: noop, addListener: noop, removeListener: noop }; };
    }
    if (typeof global.structuredClone !== 'function') {
      global.structuredClone = function (v) { return v === undefined ? undefined : JSON.parse(JSON.stringify(v)); };
    }
    if (typeof global.DOMParser === 'undefined') { global.DOMParser = DOMParser; }
    if (typeof global.Image === 'undefined') { global.Image = Image; }
    if (typeof global.XMLHttpRequest === 'undefined') { global.XMLHttpRequest = XMLHttpRequest; }
    if (typeof global.alert !== 'function') { global.alert = noop; }
  }
  installBrowserGlobals();

  // Image elements in an option (`image` graphics, symbol images, patterns)
  // go through the same Image so that they carry their natural size.
  echarts.setPlatformAPI({
    loadImage: function (src) {
      var img = new Image();
      loadInto(img, String(src), true);
      return img;
    }
  });

  // --- jQuery subset ---------------------------------------------------------

  // deferred wraps the arguments jQuery would resolve a request with
  // ([data, textStatus, jqXHR]) in a promise that also has jQuery's done,
  // fail and always methods.
  function deferred(args) {
    var p = Promise.resolve(args[0]);
    p.args = args;
    p.done = function (fn) { if (typeof fn === 'function') { fn.apply(null, args); } return p; };
    p.fail = function () { return p; };
    p.always = function (fn) { if (typeof fn === 'function') { fn.apply(null, args); } return p; };
    return p;
  }

  // makeJQuery builds the subset of jQuery the examples use for data access.
  function makeJQuery() {
    function get(url, data, success, dataType) {
      if (typeof data === 'function') { dataType = success; success = data; data = undefined; }
      var r = load(url);
      var value = dataType === 'text' ? r.text : (r.json !== undefined ? r.json : r.text);
      if (typeof success === 'function') { success(value, 'success'); }
      return deferred([value, 'success', { responseText: r.text }]);
    }
    function getJSON(url, data, success) {
      if (typeof data === 'function') { success = data; data = undefined; }
      return get(url, undefined, success, 'json');
    }
    function getScript(url, success) {
      var r = load(url);
      runScript(r.text);
      if (typeof success === 'function') { success(r.text, 'success'); }
      return deferred([r.text, 'success', { responseText: r.text }]);
    }
    function ajax(url, settings) {
      if (typeof url === 'object' && url !== null) { settings = url; url = settings.url; }
      settings = settings || {};
      if (settings.dataType === 'script') { return getScript(url, settings.success); }
      return get(url, undefined, settings.success, settings.dataType);
    }
    function when() {
      var ds = Array.prototype.slice.call(arguments);
      if (ds.length === 1) {
        return ds[0] && typeof ds[0].done === 'function' ? ds[0] : deferred([ds[0]]);
      }
      // With several deferreds, each done() argument is that request's
      // argument list, as in jQuery.
      var args = ds.map(function (d) { return d && d.args ? d.args : [d]; });
      var p = Promise.all(ds).then(function () { return args; });
      p.args = args;
      p.done = function (fn) { if (typeof fn === 'function') { fn.apply(null, args); } return p; };
      p.fail = function () { return p; };
      p.always = function (fn) { if (typeof fn === 'function') { fn.apply(null, args); } return p; };
      return p;
    }
    // $(selector) finds nothing: it returns an empty, chainable wrapper so
    // that a resize handler or a style tweak on the page is a no-op.
    function wrapper() {
      var w = { length: 0 };
      var chain = function () { return w; };
      ['on', 'off', 'one', 'resize', 'ready', 'css', 'attr', 'addClass', 'removeClass', 'html', 'text', 'show', 'hide', 'append', 'remove', 'each', 'bind', 'click', 'trigger'].forEach(function (m) { w[m] = chain; });
      w.ready = function (fn) { if (typeof fn === 'function') { fn(); } return w; };
      w.find = w.eq = w.first = w.parent = w.children = chain;
      w.width = function () { return global.innerWidth; };
      w.height = function () { return global.innerHeight; };
      w.get = function () { return undefined; };
      return w;
    }
    var $ = function (arg) {
      if (typeof arg === 'function') { arg(); }
      return wrapper();
    };
    $.get = get;
    $.getJSON = getJSON;
    $.getScript = getScript;
    $.ajax = ajax;
    $.when = when;
    $.extend = Object.assign;
    $.each = function (obj, fn) {
      if (Array.isArray(obj)) { obj.forEach(function (v, i) { fn.call(v, i, v); }); }
      else { Object.keys(obj).forEach(function (k) { fn.call(obj[k], k, obj[k]); }); }
      return obj;
    };
    $.isArray = Array.isArray;
    return $;
  }

  // makeFetch builds a fetch() that resolves through the host loader.
  function makeFetch() {
    return function fetch(url) {
      try {
        var r = load(url);
        var response = {
          ok: true, status: 200, url: String(url),
          text: function () { return Promise.resolve(r.text); },
          json: function () {
            return r.json !== undefined ? Promise.resolve(r.json) : Promise.reject(new Error('fetch: response is not JSON'));
          }
        };
        return Promise.resolve(response);
      } catch (e) {
        return Promise.reject(e);
      }
    };
  }

  var pending = null;

  // applyOption sets a script's `option` on the chart, once, unless the
  // script called setOption itself. The datasets are injected first.
  function applyOption(state) {
    if (state.setCalled) { return; }
    var option = state.option !== undefined ? state.option : global.option;
    if (!option || typeof option !== 'object') { return; }
    state.option = option;
    inject(option, state.datasets);
    state.chart.setOption(option, { notMerge: true, lazyUpdate: false, silent: true });
  }

  // prepareScript creates the chart and runs the script against it.
  function prepareScript(scriptText, datasetsText, optsText) {
    var state = { chart: null, opts: null, datasets: {}, setCalled: false, option: undefined, error: null };
    pending = state;
    timers.clear();
    try {
      state.opts = optsText ? JSON.parse(optsText) : {};
      state.datasets = datasetsText ? JSON.parse(datasetsText) : {};
      global.innerWidth = state.opts.width || 600;
      global.innerHeight = state.opts.height || 400;
      var chart = newChart(state.opts);
      state.chart = chart;
      // Loading indicators need a DOM; make them no-ops.
      chart.showLoading = function () {};
      chart.hideLoading = function () {};
      // Apply the server-side defaults to every option the script sets, and
      // remember that it did so.
      var setOption = chart.setOption;
      chart.setOption = function (option, a, b) {
        state.setCalled = true;
        prepareOption(option, state.opts);
        return setOption.call(chart, option, a, b);
      };
      var app = {};
      var jq = makeJQuery();
      var fetchFn = makeFetch();
      delete global.option;
      // A trailing return picks up `option` whether the script declared it
      // (const/let/var inside this function) or assigned it as a global.
      var body = String(scriptText) + '\n;return (typeof option !== "undefined") ? option : undefined;';
      var fn = new Function('echarts', 'myChart', 'app', '$', 'ROOT_PATH', 'CDN_PATH', 'fetch', body);
      var ret = fn(echarts, chart, app, jq, state.opts.rootPath || '', state.opts.cdnPath || '', fetchFn);
      state.option = ret !== undefined ? ret : global.option;
      // The examples site applies `option` as soon as the script returns,
      // then the timers it set fire; keep that order so that an update
      // from a timer merges into a configured chart.
      applyOption(state);
      timers.run(timerLimit);
    } catch (e) {
      state.error = errorText(e);
    }
    return state;
  }

  // finishScript applies an option assigned by a promise reaction, runs the
  // timers those set, and serializes the chart.
  function finishScript() {
    var state = pending;
    pending = null;
    var result = { svg: null, error: null };
    try {
      if (!state) { throw new Error('finishScript called without prepareScript'); }
      if (state.error) { throw new Error(state.error); }
      applyOption(state);
      timers.run(timerLimit);
      applyOption(state);
      if (!state.setCalled) {
        throw new Error('script did not assign option or call myChart.setOption');
      }
      result.svg = state.chart.renderToSVGString({ useViewBox: !!state.opts.useViewBox });
    } catch (e) {
      result.error = errorText(e);
    } finally {
      delete global.option;
      timers.clear();
      if (state && state.chart) { try { state.chart.dispose(); } catch (e2) { /* ignore */ } }
    }
    return result;
  }

  global.__goecharts = {
    render: render,
    prepareScript: prepareScript,
    finishScript: finishScript,
    version: echarts.version
  };
})(globalThis);
