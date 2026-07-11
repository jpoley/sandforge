package agents

import (
	"embed"
	"io/fs"
)

// webdistFS holds the built React/shadcn control UI (deploy/agents-ui → vite build → webdist).
// Committed so `go build` works without Node; regenerate with `mage webUI` after changing the UI.
//
//go:embed all:webdist
var webdistFS embed.FS

// uiFS returns the SPA's file tree rooted at its top (so "/" maps to index.html).
func uiFS() (fs.FS, error) { return fs.Sub(webdistFS, "webdist") }
