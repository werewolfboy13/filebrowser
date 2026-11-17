//go:build !unix

package fileutils

import (
	"os"

	"github.com/spf13/afero"
)

// keepIdentity reports whether the file at tmp can take the place of the file
// described by old without changing who owns it. Ownership is not exposed
// through this API on these platforms, so there is nothing to carry over.
func keepIdentity(_ afero.Fs, _ string, _ os.FileInfo) bool {
	return true
}
