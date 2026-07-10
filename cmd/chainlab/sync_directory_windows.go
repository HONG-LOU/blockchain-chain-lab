//go:build windows

package main

// Windows is a development target. Production validator artifacts use the
// Unix path, which fsyncs their containing directory before reporting success.
func syncArtifactDirectory(string) error {
	return nil
}
