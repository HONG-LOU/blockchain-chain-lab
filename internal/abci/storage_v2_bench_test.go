package abci

import (
	"fmt"
	"strings"
	"testing"

	"chainlab/internal/state"
)

func BenchmarkStorageDeltaSingleMutation(b *testing.B) {
	for _, entries := range []int{1_000, 4_096} {
		b.Run(fmt.Sprintf("entries-%d", entries), func(b *testing.B) {
			account := "0x1111111111111111111111111111111111111111"
			store := state.NewStore()
			if err := store.SetValidators([]string{account}); err != nil {
				b.Fatal(err)
			}
			for index := 0; index < entries; index++ {
				if err := store.SetStorage(account, storageTestKey(index), strings.Repeat("x", 128)); err != nil {
					b.Fatal(err)
				}
			}
			baseline, err := flattenStateSnapshot(store.Snapshot())
			if err != nil {
				b.Fatal(err)
			}
			store.ResetMutations()
			if err := store.SetStorage(account, storageTestKey(entries/2), "changed"); err != nil {
				b.Fatal(err)
			}

			b.Run("mutation-journal", func(b *testing.B) {
				for range b.N {
					sets, deletes, err := flatDeltaFromStoreMutations(baseline, store)
					if err != nil || len(sets) != 1 || len(deletes) != 0 {
						b.Fatalf("sets=%d deletes=%d err=%v", len(sets), len(deletes), err)
					}
				}
			})

			b.Run("full-flatten-diff", func(b *testing.B) {
				for range b.N {
					next, err := flattenStateSnapshot(store.Snapshot())
					if err != nil {
						b.Fatal(err)
					}
					sets, deletes := diffFlatState(baseline, next)
					if len(sets) != 1 || len(deletes) != 0 {
						b.Fatalf("sets=%d deletes=%d", len(sets), len(deletes))
					}
				}
			})
		})
	}
}
