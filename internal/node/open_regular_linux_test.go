//go:build linux

package node

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadBoundedRegularFileRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chain.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := readBoundedRegularFile(path, "persisted snapshot", MaxDiskSnapshotBytes)
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("FIFO error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO metadata read blocked")
	}
}
