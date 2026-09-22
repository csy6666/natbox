package web

import _ "embed"

// IndexHTML is the standalone, dependency-free management interface.
//
//go:embed index.html
var IndexHTML []byte
