package abci

import "testing"

func TestNormalizeStorageProfile(t *testing.T) {
	tests := []struct {
		name    string
		profile StorageProfile
		want    StorageProfile
		valid   bool
	}{
		{name: "default", want: DefaultStorageProfile(), valid: true},
		{name: "pruned", profile: PrunedStorageProfile(), want: PrunedStorageProfile(), valid: true},
		{name: "pruned retains history", profile: StorageProfile{Mode: StorageModePruned, RetainHeights: 2}, valid: false},
		{name: "pruned checkpoints", profile: StorageProfile{Mode: StorageModePruned, RetainHeights: 1, CheckpointInterval: 1}, valid: false},
		{name: "full", profile: StorageProfile{Mode: StorageModeFull, RetainHeights: 4, CheckpointInterval: 2}, want: StorageProfile{Mode: StorageModeFull, RetainHeights: 4, CheckpointInterval: 2}, valid: true},
		{name: "full one height", profile: StorageProfile{Mode: StorageModeFull, RetainHeights: 1, CheckpointInterval: 1}, valid: false},
		{name: "full checkpoint exceeds retention", profile: StorageProfile{Mode: StorageModeFull, RetainHeights: 4, CheckpointInterval: 5}, valid: false},
		{name: "archive", profile: ArchiveStorageProfile(10), want: ArchiveStorageProfile(10), valid: true},
		{name: "archive retention", profile: StorageProfile{Mode: StorageModeArchive, RetainHeights: 1, CheckpointInterval: 10}, valid: false},
		{name: "archive no checkpoints", profile: StorageProfile{Mode: StorageModeArchive}, valid: false},
		{name: "unknown", profile: StorageProfile{Mode: "unknown"}, valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeStorageProfile(test.profile)
			if test.valid {
				if err != nil || got != test.want {
					t.Fatalf("normalize profile = %+v err=%v, want %+v", got, err, test.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("invalid profile normalized to %+v", got)
			}
		})
	}
}
