package web

import "embed"

// Files contains the production UI bundled into the server binary.
//
//go:embed index.html app.js output.css
var Files embed.FS
