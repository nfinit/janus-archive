// Package version defines the JANUS version, shared by server and client.
//
// JANUS uses "phase" versioning: the server presents itself as
// "JANUS Phase major.minor.patch.build"; clients use the same numbers
// without the "Phase" prefix. The build number increments with each
// testable change.
package version

import "fmt"

const (
	Major = 0
	Minor = 0
	Patch = 0
	Build = 5
)

// Number returns the bare version number, e.g. "0.0.0.1".
func Number() string {
	return fmt.Sprintf("%d.%d.%d.%d", Major, Minor, Patch, Build)
}

// Server returns the server version string, e.g. "JANUS Phase 0.0.0.1".
func Server() string {
	return "JANUS Phase " + Number()
}

// Client returns the client version string, e.g. "JANUS Client 0.0.0.1".
func Client() string {
	return "JANUS Client " + Number()
}
