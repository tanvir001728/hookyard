// Package version exposes build information injected at link time.
package version

import (
	"fmt"
	"runtime"
)

// These values are overridden at build time with -ldflags "-X ...".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a human-readable version line.
func String() string {
	return fmt.Sprintf("hookyard %s (commit %s, built %s, %s %s/%s)",
		Version, Commit, Date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
