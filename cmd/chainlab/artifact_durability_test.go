package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExclusiveArtifactDirectorySyncFailureIsCommitUncertain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "validator-key.json")
	syncFailure := errors.New("injected directory sync failure")
	artifact, err := createExclusiveArtifactWithDirectorySync(path, []byte("secret"), 0o600, func(directory string) error {
		if directory != filepath.Dir(path) {
			t.Fatalf("directory sync path = %q", directory)
		}
		return syncFailure
	})
	if !errors.Is(err, errArtifactCommitUncertain) || !strings.Contains(err.Error(), syncFailure.Error()) {
		t.Fatalf("artifact commit error = %v", err)
	}
	if artifact.path != path || artifact.info == nil {
		t.Fatalf("uncertain artifact identity = %+v", artifact)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != "secret" {
		t.Fatalf("uncertain artifact contents = %q", raw)
	}
}

func TestExclusiveArtifactPublishesCompleteFileWithoutStagingResidue(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "validator-key.json")
	raw := []byte("complete secret")
	artifact, err := createExclusiveArtifact(path, raw, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.path != path || artifact.info == nil {
		t.Fatalf("artifact identity = %+v", artifact)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("artifact contents = %q", got)
	}
	assertNoArtifactStagingFiles(t, directory)

	if _, err := createExclusiveArtifact(path, []byte("replacement"), 0o600); !errors.Is(err, os.ErrExist) {
		t.Fatalf("replacement error = %v", err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("existing artifact changed to %q", got)
	}
	assertNoArtifactStagingFiles(t, directory)
}

func assertNoArtifactStagingFiles(t *testing.T, directory string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, ".*.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("staging artifacts remain: %#v", matches)
	}
}

func TestGenesisPairDoesNotPublishGenesisWhenKeyCommitIsUncertain(t *testing.T) {
	directory := t.TempDir()
	genesisPath := filepath.Join(directory, "genesis.json")
	keyPath := filepath.Join(directory, "validator-key.json")
	syncFailure := errors.New("injected key directory sync failure")
	createArtifact := func(path string, raw []byte, mode os.FileMode) (createdArtifact, error) {
		return createExclusiveArtifactWithDirectorySync(path, raw, mode, func(string) error {
			if path == keyPath {
				return syncFailure
			}
			return nil
		})
	}

	_, _, err := createGenesisFilesWithArtifactCreator(genesisPath, keyPath, createArtifact)
	if !errors.Is(err, errArtifactCommitUncertain) || !strings.Contains(err.Error(), syncFailure.Error()) {
		t.Fatalf("genesis pair commit error = %v", err)
	}
	if info, statErr := os.Stat(keyPath); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("uncertain key artifact = %v, %v", info, statErr)
	}
	if _, statErr := os.Stat(genesisPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("genesis published after uncertain key commit: %v", statErr)
	}
}

func TestGenesisPairPreservesKeyWhenGenesisCommitIsUncertain(t *testing.T) {
	directory := t.TempDir()
	genesisPath := filepath.Join(directory, "genesis.json")
	keyPath := filepath.Join(directory, "validator-key.json")
	syncFailure := errors.New("injected genesis directory sync failure")
	createArtifact := func(path string, raw []byte, mode os.FileMode) (createdArtifact, error) {
		return createExclusiveArtifactWithDirectorySync(path, raw, mode, func(string) error {
			if path == genesisPath {
				return syncFailure
			}
			return nil
		})
	}

	_, _, err := createGenesisFilesWithArtifactCreator(genesisPath, keyPath, createArtifact)
	if !errors.Is(err, errArtifactCommitUncertain) || !strings.Contains(err.Error(), syncFailure.Error()) {
		t.Fatalf("genesis commit error = %v", err)
	}
	for _, path := range []string{keyPath, genesisPath} {
		if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() {
			t.Fatalf("uncertain artifact %s = %v, %v", path, info, statErr)
		}
	}
}

func TestArtifactCommandsRequirePrecreatedOutputDirectories(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "not-created")
	keyPath := filepath.Join(missing, "validator-key.json")
	if err := runKeygenCommand([]string{"--out", keyPath}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "must already exist") {
		t.Fatalf("keygen missing-directory error = %v", err)
	}
	if _, statErr := os.Stat(missing); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("keygen created output directory: %v", statErr)
	}

	genesisPath := filepath.Join(missing, "genesis.json")
	if _, _, err := createGenesisFiles(genesisPath, keyPath); err == nil || !strings.Contains(err.Error(), "must already exist") {
		t.Fatalf("init missing-directory error = %v", err)
	}
	if _, statErr := os.Stat(missing); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("init created output directory: %v", statErr)
	}
}
