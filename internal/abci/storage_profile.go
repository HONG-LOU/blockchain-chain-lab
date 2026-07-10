package abci

import (
	"errors"
	"fmt"
)

type StorageMode string

const (
	StorageModePruned  StorageMode = "pruned"
	StorageModeFull    StorageMode = "full"
	StorageModeArchive StorageMode = "archive"

	DefaultFullRetainHeights     uint64 = 10_000
	DefaultCheckpointInterval    uint64 = 100
	maxStorageRetainHeights      uint64 = 10_000_000
	maxStorageCheckpointInterval uint64 = 100_000
)

type StorageProfile struct {
	Mode               StorageMode `json:"mode"`
	RetainHeights      uint64      `json:"retain_heights"`
	CheckpointInterval uint64      `json:"checkpoint_interval"`
}

type StorageInfo struct {
	Protocol           string      `json:"protocol"`
	Mode               StorageMode `json:"mode"`
	RetainHeights      uint64      `json:"retain_heights"`
	CheckpointInterval uint64      `json:"checkpoint_interval"`
	MinimumHeight      int64       `json:"minimum_height"`
	CurrentHeight      int64       `json:"current_height"`
}

type historicalApplicationPersistence interface {
	LoadHeight(int64) (persistedApplicationState, error)
	HistoryRange() StorageInfo
	Backup(string) error
	Compact() error
}

func DefaultStorageProfile() StorageProfile {
	return StorageProfile{
		Mode: StorageModeFull, RetainHeights: DefaultFullRetainHeights,
		CheckpointInterval: DefaultCheckpointInterval,
	}
}

func normalizeStorageProfile(profile StorageProfile) (StorageProfile, error) {
	if profile == (StorageProfile{}) {
		profile = DefaultStorageProfile()
	}
	switch profile.Mode {
	case StorageModePruned:
		if profile.RetainHeights != 1 {
			return StorageProfile{}, errors.New("pruned storage requires retain_heights=1")
		}
		if profile.CheckpointInterval != 0 {
			return StorageProfile{}, errors.New("pruned storage requires checkpoint_interval=0")
		}
	case StorageModeFull:
		if profile.RetainHeights < 2 || profile.RetainHeights > maxStorageRetainHeights {
			return StorageProfile{}, fmt.Errorf("full storage retain_heights must be between 2 and %d", maxStorageRetainHeights)
		}
		if profile.CheckpointInterval == 0 || profile.CheckpointInterval > maxStorageCheckpointInterval ||
			profile.CheckpointInterval > profile.RetainHeights {
			return StorageProfile{}, errors.New("full storage checkpoint_interval must be positive and not exceed retain_heights")
		}
	case StorageModeArchive:
		if profile.RetainHeights != 0 {
			return StorageProfile{}, errors.New("archive storage requires retain_heights=0")
		}
		if profile.CheckpointInterval == 0 || profile.CheckpointInterval > maxStorageCheckpointInterval {
			return StorageProfile{}, fmt.Errorf("archive storage checkpoint_interval must be between 1 and %d", maxStorageCheckpointInterval)
		}
	default:
		return StorageProfile{}, fmt.Errorf("unsupported storage mode %q", profile.Mode)
	}
	return profile, nil
}

func PrunedStorageProfile() StorageProfile {
	return StorageProfile{Mode: StorageModePruned, RetainHeights: 1}
}

func ArchiveStorageProfile(checkpointInterval uint64) StorageProfile {
	return StorageProfile{Mode: StorageModeArchive, CheckpointInterval: checkpointInterval}
}
