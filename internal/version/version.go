package version

import (
	"fmt"
	"runtime"
)

var (
	// Version is the semantic version of texlite-share.
	Version = "0.1.15"
	// GitCommit is the git commit sha injected at build time.
	GitCommit = "dev"
	// BuildDate is the timestamp of the build.
	BuildDate = "unknown"
)

// Info returns a formatted human-readable version string.
func Info(component string) string {
	return fmt.Sprintf("%s v%s (commit: %s, built: %s, %s/%s)",
		component, Version, GitCommit, BuildDate, runtime.GOOS, runtime.GOARCH)
}

// Short returns the version string without metadata.
func Short() string {
	return Version
}
