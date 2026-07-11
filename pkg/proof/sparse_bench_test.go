package proof

import (
	"fmt"
	"testing"
)

func BenchmarkSparseTreeSingleUpdate(b *testing.B) {
	for _, size := range []int{1_000, 4_096} {
		b.Run(fmt.Sprintf("leaves-%d", size), func(b *testing.B) {
			entries := make(map[string][]byte, size)
			for index := 0; index < size; index++ {
				entries[fmt.Sprintf("account:%08d", index)] = []byte(fmt.Sprintf("value-%08d", index))
			}
			tree, err := BuildSparseTree(DomainSparseState, entries)
			if err != nil {
				b.Fatal(err)
			}
			key := fmt.Sprintf("account:%08d", size/2)
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				candidate := tree.Clone()
				if err := candidate.Set(key, []byte(fmt.Sprintf("changed-%d", index))); err != nil {
					b.Fatal(err)
				}
				tree = candidate.Publish()
			}
		})
	}
}
