// Package installer embeds install.sh so the writer bootstraps new nodes with the
// installer that shipped with its own build (no unpinned download of the script).
package installer

import _ "embed"

//go:embed install.sh
var Script []byte
