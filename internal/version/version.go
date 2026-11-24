// Package version holds build identity injected by the release tool.
package version

// These variables are overridden with -ldflags at release time.
var (
	Version   = "0.4.2-dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// String returns a compact human-readable build identifier.
func String() string {
	if Commit == "" || Commit == "unknown" {
		return Version
	}
	return Version + "+" + Commit
}
