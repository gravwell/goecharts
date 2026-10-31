// Copyright (c) 2026, Gravwell, Inc.
// All rights reserved.
//
// Use of this source code is governed by a BSD 2-Clause license that can be
// found in the LICENSE file.

// pre.js runs before the ECharts bundle is evaluated.
//
// ECharts (through zrender) detects its environment at load time. With no
// `window` global it takes the Node.js path, which supports server-side SVG
// rendering with no DOM at all. This file supplies the few host functions the
// bundle expects to exist at load time: timers. `console` and the text
// measurer are installed from Go, and the browser-like globals that example
// scripts use are added by post.js once the bundle has made its choice.
(function (global) {
  'use strict';

  // Timers are queued rather than run. A render drains the queue once the
  // script has returned (see post.js), so that a script which builds its
  // option inside setTimeout, or updates the chart a moment later, renders
  // the state a browser would show after those timers had elapsed. Each
  // timer fires at most once, intervals included; nothing waits for real
  // time to pass.
  var seq = 0;
  var now = 0;
  var queue = [];

  function schedule(fn, delay, args) {
    var id = ++seq;
    if (typeof fn === 'function') {
      var d = Number(delay);
      if (!(d >= 0)) { d = 0; }
      queue.push({ id: id, fn: fn, args: args, due: now + d, seq: id });
    }
    return id;
  }
  function cancel(id) {
    for (var i = 0; i < queue.length; i++) {
      if (queue[i].id === id) { queue.splice(i, 1); return; }
    }
  }
  function noop() {}

  global.setTimeout = function (fn, delay) { return schedule(fn, delay, Array.prototype.slice.call(arguments, 2)); };
  global.setInterval = function (fn, delay) { return schedule(fn, delay, Array.prototype.slice.call(arguments, 2)); };
  global.clearTimeout = cancel;
  global.clearInterval = cancel;
  // Animation frames never fire: zrender drives animations and progressive
  // rendering from them, and a static render wants neither.
  global.requestAnimationFrame = function () { return ++seq; };
  global.cancelAnimationFrame = noop;

  global.__goecharts_timers = {
    // run fires queued timers in due order, including ones they schedule,
    // until the queue is empty or limit callbacks have run.
    run: function (limit) {
      var count = 0;
      while (queue.length > 0 && count < limit) {
        var best = 0;
        for (var i = 1; i < queue.length; i++) {
          var t = queue[i], b = queue[best];
          if (t.due < b.due || (t.due === b.due && t.seq < b.seq)) { best = i; }
        }
        var timer = queue.splice(best, 1)[0];
        if (timer.due > now) { now = timer.due; }
        count++;
        timer.fn.apply(null, timer.args);
      }
      return count;
    },
    clear: function () { queue.length = 0; now = 0; }
  };
})(globalThis);
