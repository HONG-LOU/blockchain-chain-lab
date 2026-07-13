package buildinfo

import "testing"

func TestCurrentIncludesRuntimeTarget(t *testing.T) {
	info := Current()
	if info.Version == "" || info.Commit == "" || info.BuildTime == "" || info.GoVersion == "" || info.OS == "" || info.Arch == "" {
		t.Fatalf("build info = %+v", info)
	}
}
