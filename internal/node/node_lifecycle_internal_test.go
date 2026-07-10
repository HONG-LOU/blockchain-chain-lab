package node

import "testing"

func registerTestNodeClose(t testing.TB, n *Node) {
	t.Helper()
	t.Cleanup(func() {
		if err := n.Close(); err != nil {
			t.Errorf("close node: %v", err)
		}
	})
}

func closeTestNode(t testing.TB, n *Node) {
	t.Helper()
	if err := n.Close(); err != nil {
		t.Fatalf("close node: %v", err)
	}
}
