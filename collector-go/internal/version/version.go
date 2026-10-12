// Package version holds the build's version, set at build time with -ldflags "-X .../version.Version=1.2.3".
package version

var Version = "dev"

// UserAgent identifies the collector to the API and to M-Lab.
func UserAgent() string { return "speedtest-collector/" + Version }
