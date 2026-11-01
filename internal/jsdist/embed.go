// Copyright (c) 2026, Gravwell, Inc.
// All rights reserved.
//
// Use of this source code is governed by a BSD 2-Clause license that can be
// found in the LICENSE file.

// Package jsdist embeds the upstream Apache ECharts JavaScript bundle.
//
// The bundle is the unmodified UMD build published to npm as
// echarts@<Version>/dist/echarts.min.js. It is licensed under the Apache
// License 2.0 and bundles zrender, which is under the BSD 3-Clause license;
// see LICENSE.echarts, NOTICE.echarts and LICENSE.zrender in this directory
// and the LICENSES file at the repository root.
package jsdist

import _ "embed"

// Version is the upstream ECharts release embedded in this package.
const Version = "6.1.0"

// EChartsJS is the source text of the ECharts UMD bundle.
//
//go:embed echarts.min.js
var EChartsJS string
