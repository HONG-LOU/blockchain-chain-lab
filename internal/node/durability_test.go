package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWriteReportsCommitUncertainAfterRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chain.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	syncFailure := errors.New("injected directory sync failure")
	err := writeFileAtomicallyWithDirectorySync(path, []byte("new"), 0o600, func(string) error {
		return syncFailure
	})
	if !errors.Is(err, ErrAtomicCommitUncertain) || !strings.Contains(err.Error(), syncFailure.Error()) {
		t.Fatalf("atomic write error = %v", err)
	}
	contents, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(contents) != "new" {
		t.Fatalf("renamed contents = %q", contents)
	}
}

func TestCommitUncertainErrorMakesNodeStickyHalt(t *testing.T) {
	n := &Node{chainID: "chainlab-local"}
	err := n.handlePersistenceErrorLocked(fmt.Errorf("write snapshot: %w", ErrAtomicCommitUncertain))
	if err == nil || !errors.Is(err, ErrAtomicCommitUncertain) {
		t.Fatalf("persistence error = %v", err)
	}
	if n.haltErr == nil || !strings.Contains(n.haltErr.Error(), "uncertain") {
		t.Fatalf("halt error = %v", n.haltErr)
	}
}

func TestReadBoundedRegularFileRejectsOversizeAndNonRegularPaths(t *testing.T) {
	t.Run("oversize", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "metadata.json")
		if err := os.WriteFile(path, []byte("123456789"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readBoundedRegularFile(path, "metadata", 8); err == nil || !strings.Contains(err.Error(), "exceeds 8 bytes") {
			t.Fatalf("oversize metadata error = %v", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		path := t.TempDir()
		if _, err := readBoundedRegularFile(path, "metadata", 8); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("directory metadata error = %v", err)
		}
	})
}

func TestPersistedMetadataReadersEnforceIndependentSizeLimits(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		maxBytes int64
		load     func(string) error
	}{
		{
			name:     "manifest",
			fileName: "manifest.json",
			maxBytes: maxDataManifestBytes,
			load: func(dataDir string) error {
				_, err := loadDataManifest(dataDir)
				return err
			},
		},
		{
			name:     "halt marker",
			fileName: "HALT.json",
			maxBytes: maxHaltRecordBytes,
			load: func(dataDir string) error {
				_, err := loadHaltRecord(dataDir)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			path := filepath.Join(dataDir, test.fileName)
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(test.maxBytes + 1); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if err := test.load(dataDir); err == nil || !strings.Contains(err.Error(), "exceeds") {
				t.Fatalf("oversize %s error = %v", test.name, err)
			}
		})
	}
}
