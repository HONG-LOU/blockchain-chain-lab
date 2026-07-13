package desktop

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	chainabci "chainlab/internal/abci"
	"chainlab/internal/cometnode"
	"chainlab/internal/hash"
)

const (
	BackupProtocol     = "chainlab-desktop-backup-v1"
	backupManifestPath = "backup-manifest.json"
	backupStatusPath   = "backup-status.json"
	maxBackupFiles     = 100_000
	maxBackupBytes     = int64(100 * 1024 * 1024 * 1024)
)

type BackupManifest struct {
	Protocol  string       `json:"protocol"`
	ChainID   string       `json:"chain_id"`
	Profile   Profile      `json:"profile"`
	CreatedAt string       `json:"created_at"`
	Files     []BackupFile `json:"files"`
}

type BackupFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	SHA256 string `json:"sha256"`
}

type BackupStatus struct {
	Protocol      string `json:"protocol"`
	CreatedAt     string `json:"created_at"`
	Archive       string `json:"archive"`
	ArchiveBytes  int64  `json:"archive_bytes"`
	ArchiveSHA256 string `json:"archive_sha256"`
}

func Backup(root string, destination string) (returnErr error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	config, err := Load(absoluteRoot)
	if err != nil {
		return err
	}
	lock, err := acquireRuntimeLock(absoluteRoot)
	if err != nil {
		return fmt.Errorf("desktop must be stopped before backup: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	absoluteDestination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if insidePath(absoluteRoot, absoluteDestination) {
		return errors.New("desktop backup destination must be outside the live data directory")
	}
	if _, err := os.Lstat(absoluteDestination); err == nil {
		return fmt.Errorf("desktop backup destination already exists: %s", absoluteDestination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if info, err := os.Stat(filepath.Dir(absoluteDestination)); err != nil || !info.IsDir() {
		return errors.New("desktop backup destination parent must exist")
	}
	files, err := collectBackupFiles(absoluteRoot)
	if err != nil {
		return err
	}
	manifest := BackupManifest{
		Protocol: BackupProtocol, ChainID: config.ChainID, Profile: config.Profile,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Files: files,
	}
	manifestRaw, err := hash.CanonicalBytes(manifest)
	if err != nil {
		return err
	}
	archive, err := os.OpenFile(absoluteDestination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create desktop backup: %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = archive.Close()
			_ = os.Remove(absoluteDestination)
		}
	}()
	writer := zip.NewWriter(archive)
	if err := writeZipBytes(writer, backupManifestPath, manifestRaw, 0o600); err != nil {
		return err
	}
	for _, file := range files {
		if err := writeZipFile(writer, absoluteRoot, file); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close desktop backup archive: %w", err)
	}
	if err := archive.Sync(); err != nil {
		return fmt.Errorf("sync desktop backup archive: %w", err)
	}
	if err := archive.Close(); err != nil {
		return err
	}
	info, err := os.Stat(absoluteDestination)
	if err != nil {
		return err
	}
	archiveSHA256, err := fileSHA256(absoluteDestination)
	if err != nil {
		return err
	}
	backupStatus := BackupStatus{
		Protocol: BackupProtocol, CreatedAt: manifest.CreatedAt, Archive: absoluteDestination,
		ArchiveBytes: info.Size(), ArchiveSHA256: archiveSHA256,
	}
	statusRaw, err := hash.CanonicalBytes(backupStatus)
	if err != nil {
		return err
	}
	if err := replaceLocalFile(filepath.Join(absoluteRoot, backupStatusPath), statusRaw, 0o600); err != nil {
		return err
	}
	success = true
	return nil
}

func Restore(backupPath string, destination string, expectedChainID string) (Config, error) {
	if strings.TrimSpace(expectedChainID) == "" {
		return Config{}, errors.New("restore requires the expected chain id")
	}
	absoluteDestination, err := newAbsoluteDirectory(destination, "restore")
	if err != nil {
		return Config{}, err
	}
	archive, err := zip.OpenReader(backupPath)
	if err != nil {
		return Config{}, fmt.Errorf("open desktop backup: %w", err)
	}
	defer archive.Close()
	manifest, entries, err := validateBackupArchive(&archive.Reader)
	if err != nil {
		return Config{}, err
	}
	if manifest.ChainID != expectedChainID {
		return Config{}, fmt.Errorf("backup chain id %q does not match expected %q", manifest.ChainID, expectedChainID)
	}
	parent := filepath.Dir(absoluteDestination)
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(absoluteDestination)+"-restore-")
	if err != nil {
		return Config{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()
	for _, expected := range manifest.Files {
		entry := entries[expected.Path]
		if err := extractBackupFile(stage, entry, expected); err != nil {
			return Config{}, err
		}
	}
	config, err := Verify(stage, expectedChainID)
	if err != nil {
		return Config{}, fmt.Errorf("verify restored desktop data: %w", err)
	}
	if config.Profile != manifest.Profile {
		return Config{}, errors.New("backup profile does not match restored desktop config")
	}
	if err := os.Rename(stage, absoluteDestination); err != nil {
		return Config{}, fmt.Errorf("publish restored desktop data: %w", err)
	}
	published = true
	return config, nil
}

func Verify(root string, expectedChainID string) (Config, error) {
	config, err := Load(root)
	if err != nil {
		return Config{}, err
	}
	if expectedChainID != "" && config.ChainID != expectedChainID {
		return Config{}, fmt.Errorf("desktop chain id %q does not match expected %q", config.ChainID, expectedChainID)
	}
	home := filepath.Join(root, filepath.FromSlash(config.NodeHome))
	document, err := cometnode.LoadNodeDocument(home)
	if err != nil {
		return Config{}, err
	}
	if _, err := cometnode.BuildConfig(home, document); err != nil {
		return Config{}, err
	}
	genesis, err := chainabci.LoadGenesisDocument(filepath.Join(home, filepath.FromSlash(cometnode.AppGenesisPath)))
	if err != nil {
		return Config{}, err
	}
	applicationData := filepath.Join(root, filepath.FromSlash(config.ApplicationData))
	if _, err := os.Stat(applicationData); err == nil {
		application, err := chainabci.NewApplication(chainabci.Config{
			Genesis: genesis, DataDir: applicationData,
			Storage:            config.Contract.ApplicationStorage,
			CometRetainHeights: config.Contract.CometRetainBlocks,
		})
		if err != nil {
			return Config{}, err
		}
		if err := application.Close(); err != nil {
			return Config{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	return config, nil
}

func collectBackupFiles(root string) ([]BackupFile, error) {
	files := make([]BackupFile, 0, 256)
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		if relative == ".desktop.lock" || relative == RuntimePath {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("backup source must contain only real directories and regular files: %s", relative)
		}
		if info.IsDir() {
			return nil
		}
		if len(files) >= maxBackupFiles {
			return fmt.Errorf("backup exceeds %d files", maxBackupFiles)
		}
		if info.Size() > maxBackupBytes-total {
			return fmt.Errorf("backup exceeds %d bytes", maxBackupBytes)
		}
		total += info.Size()
		digest, err := fileSHA256(path)
		if err != nil {
			return err
		}
		files = append(files, BackupFile{Path: relative, Size: info.Size(), Mode: uint32(info.Mode().Perm()), SHA256: digest})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	if len(files) == 0 {
		return nil, errors.New("desktop backup has no files")
	}
	return files, nil
}

func loadBackupStatus(root string) *BackupStatus {
	raw, err := readBoundedFile(filepath.Join(root, backupStatusPath), maxConfigBytes, "backup status")
	if err != nil {
		return nil
	}
	var status BackupStatus
	if decodeStrictCanonical(raw, &status) != nil || status.Protocol != BackupProtocol || status.ArchiveBytes <= 0 || len(status.ArchiveSHA256) != 64 {
		return nil
	}
	return &status
}

func replaceLocalFile(path string, raw []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".backup-status-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func validateBackupArchive(reader *zip.Reader) (BackupManifest, map[string]*zip.File, error) {
	if len(reader.File) == 0 || len(reader.File) > maxBackupFiles+1 {
		return BackupManifest{}, nil, errors.New("desktop backup entry count is invalid")
	}
	entries := make(map[string]*zip.File, len(reader.File))
	var manifestRaw []byte
	var total uint64
	for _, entry := range reader.File {
		if !validArchivePath(entry.Name) || entry.FileInfo().IsDir() || entry.FileInfo().Mode()&os.ModeSymlink != 0 {
			return BackupManifest{}, nil, fmt.Errorf("desktop backup contains unsafe path %q", entry.Name)
		}
		if _, duplicate := entries[entry.Name]; duplicate {
			return BackupManifest{}, nil, fmt.Errorf("desktop backup contains duplicate path %q", entry.Name)
		}
		if entry.UncompressedSize64 > uint64(maxBackupBytes)-total {
			return BackupManifest{}, nil, errors.New("desktop backup uncompressed size exceeds limit")
		}
		total += entry.UncompressedSize64
		if entry.Name == backupManifestPath {
			if entry.UncompressedSize64 > maxConfigBytes {
				return BackupManifest{}, nil, errors.New("desktop backup manifest exceeds size limit")
			}
			file, err := entry.Open()
			if err != nil {
				return BackupManifest{}, nil, err
			}
			manifestRaw, err = io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				return BackupManifest{}, nil, errors.Join(err, closeErr)
			}
			continue
		}
		entries[entry.Name] = entry
	}
	if len(manifestRaw) == 0 {
		return BackupManifest{}, nil, errors.New("desktop backup manifest is missing")
	}
	var manifest BackupManifest
	if err := decodeStrictCanonical(manifestRaw, &manifest); err != nil {
		return BackupManifest{}, nil, err
	}
	if manifest.Protocol != BackupProtocol || manifest.ChainID == "" || len(manifest.Files) != len(entries) {
		return BackupManifest{}, nil, errors.New("desktop backup manifest contract is invalid")
	}
	previous := ""
	for _, file := range manifest.Files {
		if !validArchivePath(file.Path) || file.Path <= previous || file.Size < 0 || len(file.SHA256) != 64 {
			return BackupManifest{}, nil, errors.New("desktop backup file manifest is invalid or unsorted")
		}
		entry, exists := entries[file.Path]
		if !exists || entry.UncompressedSize64 != uint64(file.Size) {
			return BackupManifest{}, nil, fmt.Errorf("desktop backup entry does not match manifest: %s", file.Path)
		}
		previous = file.Path
	}
	return manifest, entries, nil
}

func extractBackupFile(stage string, entry *zip.File, expected BackupFile) error {
	target := filepath.Join(stage, filepath.FromSlash(expected.Path))
	if !insidePath(stage, target) {
		return errors.New("backup extraction path escapes restore directory")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	mode := os.FileMode(expected.Mode) & 0o777
	if mode == 0 {
		mode = 0o600
	}
	destination, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(destination, hasher), io.LimitReader(source, expected.Size+1))
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil {
		return errors.Join(copyErr, closeErr)
	}
	if written != expected.Size || hex.EncodeToString(hasher.Sum(nil)) != expected.SHA256 {
		return fmt.Errorf("backup file checksum does not match: %s", expected.Path)
	}
	return nil
}

func writeZipFile(writer *zip.Writer, root string, file BackupFile) error {
	source, err := os.Open(filepath.Join(root, filepath.FromSlash(file.Path)))
	if err != nil {
		return err
	}
	defer source.Close()
	header := &zip.FileHeader{Name: file.Path, Method: zip.Deflate}
	header.SetMode(os.FileMode(file.Mode))
	destination, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	written, err := io.Copy(destination, source)
	if err != nil {
		return err
	}
	if written != file.Size {
		return fmt.Errorf("backup source changed while reading: %s", file.Path)
	}
	return nil
}

func writeZipBytes(writer *zip.Writer, name string, raw []byte, mode os.FileMode) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(mode)
	destination, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(destination, bytes.NewReader(raw))
	return err
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func validArchivePath(path string) bool {
	if path == "" || strings.Contains(path, "\\") || filepath.IsAbs(path) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	return clean == path && path != "." && path != ".." && !strings.HasPrefix(path, "../")
}

func insidePath(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
