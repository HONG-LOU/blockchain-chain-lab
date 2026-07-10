package state

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestStorageByteCacheFollowsStateLifecycle(t *testing.T) {
	address := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := NewStore()
	if err := store.SetValidators([]string{"0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}); err != nil {
		t.Fatal(err)
	}
	mustSetStorage(t, store, address, "profile:name", "alice")
	mustSetStorage(t, store, address, "session:key:limit", "100")
	mustSetStorage(t, store, address, "session:key:spent", "25")
	assertStorageByteCache(t, store, address)

	rootBeforeUpdate := store.Root()
	mustSetStorage(t, store, address, "profile:name", "alice-updated")
	if store.Root() == rootBeforeUpdate {
		t.Fatal("updating existing storage did not change the state root")
	}
	assertStorageByteCache(t, store, address)

	clone := store.Clone()
	if clone.Root() != store.Root() {
		t.Fatal("clone changed the state root")
	}
	assertStorageByteCache(t, clone, address)
	clone.DeleteStorage(address, "profile:name")
	assertStorageByteCache(t, clone, address)
	if clone.Root() == store.Root() {
		t.Fatal("deleting clone storage did not change its state root")
	}
	assertStorageByteCache(t, store, address)

	if deleted := clone.DeleteStoragePrefix(address, "session:"); deleted != 2 {
		t.Fatalf("prefix delete removed %d entries, want 2", deleted)
	}
	assertStorageByteCache(t, clone, address)

	restored, err := NewStoreFromSnapshot(store.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if restored.Root() != store.Root() {
		t.Fatal("snapshot restore changed the state root")
	}
	assertStorageByteCache(t, restored, address)

	replacement := NewStore()
	mustSetStorage(t, replacement, address, "replacement", "value")
	restored.ReplaceWith(replacement)
	if restored.Root() != replacement.Root() {
		t.Fatal("replacement changed the source state root")
	}
	assertStorageByteCache(t, restored, address)
}

func TestClearDelegationUpdatesStorageByteCache(t *testing.T) {
	address := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := NewStore()
	store.SetDelegatedCodeID(address, "account.v1")
	mustSetStorage(t, store, address, "owner", "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	mustSetStorage(t, store, address, "session:key:limit", "100")
	mustSetStorage(t, store, address, "recovery:guardian", "0xcccccccccccccccccccccccccccccccccccccccc")
	mustSetStorage(t, store, address, "profile:name", "alice")

	store.ClearDelegation(address)
	assertStorageByteCache(t, store, address)
	if got := store.GetStorage(address, "profile:name"); got != "alice" {
		t.Fatalf("unrelated storage = %q", got)
	}
}

func TestStorageByteCacheEnforcesExactLimitAcrossDerivedStateOperations(t *testing.T) {
	address := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := NewStore()
	if err := store.SetValidators([]string{"0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}); err != nil {
		t.Fatal(err)
	}
	largeValue := strings.Repeat("v", MaxStorageValueBytes)
	for index := 0; index < 127; index++ {
		mustSetStorage(t, store, address, fmt.Sprintf("bulk:%03d", index), largeValue)
	}
	tailKey := "tail"
	tailValue := strings.Repeat("t", MaxStorageBytesPerAccount-store.storageBytes[address]-len(tailKey))
	mustSetStorage(t, store, address, tailKey, tailValue)
	assertStorageByteCache(t, store, address)
	if got := store.storageBytes[address]; got != MaxStorageBytesPerAccount {
		t.Fatalf("storage byte cache at boundary = %d", got)
	}
	if err := store.SetStorage(address, tailKey, tailValue+"x"); !errors.Is(err, ErrStorageLimit) {
		t.Fatalf("boundary overflow error = %v", err)
	}

	mustSetStorage(t, store, address, tailKey, tailValue[:len(tailValue)-1])
	if got := store.storageBytes[address]; got != MaxStorageBytesPerAccount-1 {
		t.Fatalf("storage byte cache after shrink = %d", got)
	}
	mustSetStorage(t, store, address, tailKey, tailValue)
	assertStorageByteCache(t, store, address)

	clone := store.Clone()
	if deleted := clone.DeleteStoragePrefix(address, "bulk:"); deleted != 127 {
		t.Fatalf("prefix delete removed %d entries, want 127", deleted)
	}
	assertStorageByteCache(t, clone, address)
	for index := 0; index < 127; index++ {
		mustSetStorage(t, clone, address, fmt.Sprintf("bulk:%03d", index), largeValue)
	}
	assertStorageByteCache(t, clone, address)
	if clone.Root() != store.Root() {
		t.Fatal("prefix delete and refill did not restore the state root")
	}

	restored, err := NewStoreFromSnapshot(store.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	restored.DeleteStorage(address, tailKey)
	assertStorageByteCache(t, restored, address)
	mustSetStorage(t, restored, address, tailKey, tailValue)
	assertStorageByteCache(t, restored, address)
	if restored.Root() != store.Root() {
		t.Fatal("snapshot restore delete and refill did not restore the state root")
	}
}

func BenchmarkSetStorageUpdatesExistingKey(b *testing.B) {
	for _, entries := range []int{1, MaxStorageEntriesPerAccount} {
		b.Run(benchmarkName(entries), func(b *testing.B) {
			store := NewStore()
			address := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			for index := 0; index < entries; index++ {
				key := fixedStorageKey(index)
				if err := store.SetStorage(address, key, "value"); err != nil {
					b.Fatal(err)
				}
			}
			key := fixedStorageKey(0)
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				if err := store.SetStorage(address, key, "value"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func assertStorageByteCache(t *testing.T, store *Store, address string) {
	t.Helper()
	want := storageSize(store.accounts[address].Storage)
	if got := store.storageBytes[address]; got != want {
		t.Fatalf("storage byte cache = %d, want %d", got, want)
	}
}

func mustSetStorage(t testing.TB, store *Store, address string, key string, value string) {
	t.Helper()
	if err := store.SetStorage(address, key, value); err != nil {
		t.Fatal(err)
	}
}

func benchmarkName(entries int) string {
	if entries == 1 {
		return "one-entry"
	}
	return "max-entries"
}

func fixedStorageKey(index int) string {
	const digits = "0123456789"
	key := []byte("key:0000")
	for position := len(key) - 1; position >= len("key:"); position-- {
		key[position] = digits[index%10]
		index /= 10
	}
	return string(key)
}
