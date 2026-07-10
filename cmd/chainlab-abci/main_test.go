package main

import (
	"testing"

	chainabci "chainlab/internal/abci"
)

func TestStorageProfileDefaultsFollowMode(t *testing.T) {
	tests := []struct {
		name string
		mode chainabci.StorageMode
		want chainabci.StorageProfile
	}{
		{
			name: "full",
			mode: chainabci.StorageModeFull,
			want: chainabci.DefaultStorageProfile(),
		},
		{
			name: "archive",
			mode: chainabci.StorageModeArchive,
			want: chainabci.ArchiveStorageProfile(chainabci.DefaultCheckpointInterval),
		},
		{
			name: "pruned",
			mode: chainabci.StorageModePruned,
			want: chainabci.PrunedStorageProfile(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := storageProfileFromFlags(
				test.mode,
				chainabci.DefaultFullRetainHeights,
				chainabci.DefaultCheckpointInterval,
				false,
				false,
			)
			if got != test.want {
				t.Fatalf("storage profile = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestStorageProfilePreservesExplicitValues(t *testing.T) {
	want := chainabci.StorageProfile{
		Mode: chainabci.StorageModeArchive, RetainHeights: 7, CheckpointInterval: 9,
	}
	got := storageProfileFromFlags(want.Mode, want.RetainHeights, want.CheckpointInterval, true, true)
	if got != want {
		t.Fatalf("storage profile = %+v, want %+v", got, want)
	}
}
