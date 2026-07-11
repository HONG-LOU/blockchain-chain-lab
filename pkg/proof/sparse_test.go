package proof

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestSparseTreeMembershipNonMembershipAndOrderIndependence(t *testing.T) {
	entries := map[string][]byte{
		"account:YQ": []byte(`{"kind":"account","key":"YQ","value":"MQ"}`),
		"account:Yg": []byte(`{"kind":"account","key":"Yg","value":"Mg"}`),
		"param:Yw":   []byte(`{"kind":"param","key":"Yw","value":"Mw"}`),
	}
	first, err := BuildSparseTree(DomainSparseState, entries)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSparseTree(DomainSparseState)
	if err != nil {
		t.Fatal(err)
	}
	keys := slices.Collect(maps.Keys(entries))
	slices.Sort(keys)
	slices.Reverse(keys)
	for _, key := range keys {
		if err := second.Set(key, entries[key]); err != nil {
			t.Fatal(err)
		}
	}
	if first.Root() != second.Root() || first.EntryCount() != len(entries) {
		t.Fatalf("sparse roots first=%s second=%s count=%d", first.Root(), second.Root(), first.EntryCount())
	}
	for key, want := range entries {
		proof, err := first.Prove(key)
		if err != nil {
			t.Fatal(err)
		}
		got, exists, err := VerifySparse(first.Root(), proof)
		if err != nil || !exists || !bytes.Equal(got, want) {
			t.Fatalf("key=%s exists=%t value=%q err=%v", key, exists, got, err)
		}
	}
	missing := "account:Yw"
	proof, err := first.Prove(missing)
	if err != nil {
		t.Fatal(err)
	}
	value, exists, err := VerifySparse(first.Root(), proof)
	if err != nil || exists || value != nil {
		t.Fatalf("missing exists=%t value=%q err=%v", exists, value, err)
	}
}

func TestSparseTreeUpdateDeleteCloneAndCompressedProof(t *testing.T) {
	tree, err := NewSparseTree(DomainSparseState)
	if err != nil {
		t.Fatal(err)
	}
	emptyRoot := tree.Root()
	if err := tree.Set("account:YQ", []byte("first")); err != nil {
		t.Fatal(err)
	}
	firstRoot := tree.Root()
	clone := tree.Clone()
	if err := clone.Set("account:YQ", []byte("second")); err != nil {
		t.Fatal(err)
	}
	if clone.Root() == firstRoot || tree.Root() != firstRoot {
		t.Fatal("sparse clone update mutated the original tree")
	}
	if err := clone.Delete("account:YQ"); err != nil {
		t.Fatal(err)
	}
	if clone.Root() != emptyRoot || clone.EntryCount() != 0 {
		t.Fatalf("deleted root=%s empty=%s count=%d", clone.Root(), emptyRoot, clone.EntryCount())
	}
	published := clone.Publish()
	if published != tree || tree.Root() != emptyRoot || tree.EntryCount() != 0 {
		t.Fatal("published sparse overlay did not merge into its parent")
	}
	proofTree, err := NewSparseTree(DomainSparseState)
	if err != nil {
		t.Fatal(err)
	}
	if err := proofTree.Set("account:YQ", []byte("first")); err != nil {
		t.Fatal(err)
	}
	proof, err := proofTree.Prove("account:YQ")
	if err != nil {
		t.Fatal(err)
	}
	if len(proof.Bitmap) != 43 || len(proof.Siblings) != 0 {
		t.Fatalf("single-leaf compressed proof bitmap=%q siblings=%d", proof.Bitmap, len(proof.Siblings))
	}
}

func TestSparseProofRejectsTamperingAndNonCanonicalFields(t *testing.T) {
	tree, err := NewSparseTree(DomainSparseState)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Set("account:YQ", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := tree.Set("account:Yg", []byte("second")); err != nil {
		t.Fatal(err)
	}
	original, err := tree.Prove("account:YQ")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		root   string
		mutate func(*SparseProof)
	}{
		{name: "root", root: "0x" + strings.Repeat("00", 32)},
		{name: "protocol", root: tree.Root(), mutate: func(proof *SparseProof) { proof.Protocol = "other" }},
		{name: "domain", root: tree.Root(), mutate: func(proof *SparseProof) { proof.Domain = DomainState }},
		{name: "key", root: tree.Root(), mutate: func(proof *SparseProof) { proof.Key = "account:Yw" }},
		{name: "value", root: tree.Root(), mutate: func(proof *SparseProof) { proof.Value = base64.RawURLEncoding.EncodeToString([]byte("changed")) }},
		{name: "bitmap padding", root: tree.Root(), mutate: func(proof *SparseProof) { proof.Bitmap += "=" }},
		{name: "bitmap", root: tree.Root(), mutate: func(proof *SparseProof) { proof.Bitmap = base64.RawURLEncoding.EncodeToString(make([]byte, 32)) }},
		{name: "sibling", root: tree.Root(), mutate: func(proof *SparseProof) { proof.Siblings[0] = "0x" + strings.Repeat("00", 32) }},
		{name: "extra sibling", root: tree.Root(), mutate: func(proof *SparseProof) { proof.Siblings = append(proof.Siblings, "0x"+strings.Repeat("00", 32)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := original
			candidate.Siblings = append([]string(nil), original.Siblings...)
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			if _, _, err := VerifySparse(test.root, candidate); err == nil {
				t.Fatal("tampered sparse proof verified")
			}
		})
	}
}

func TestSparseEnvelopeBindsTrustedClaimsAndStateLeaf(t *testing.T) {
	leaf, err := json.Marshal(StateLeaf{Kind: "account", Key: "YQ", Value: "MQ"})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := BuildSparseTree(DomainSparseState, map[string][]byte{"account:YQ": leaf})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := tree.Prove("account:YQ")
	if err != nil {
		t.Fatal(err)
	}
	envelope := SparseEnvelope{
		Protocol: SparseEnvelopeProtocol, Kind: KindState, Height: 9,
		Root: tree.Root(), Key: "account:YQ", Proof: proof,
	}
	verified, exists, err := VerifySparseEnvelope(tree.Root(), 9, KindState, "account:YQ", envelope)
	if err != nil || !exists || !bytes.Equal(verified, leaf) {
		t.Fatalf("exists=%t leaf=%q err=%v", exists, verified, err)
	}
	missingProof, err := tree.Prove("account:Yg")
	if err != nil {
		t.Fatal(err)
	}
	missing := SparseEnvelope{
		Protocol: SparseEnvelopeProtocol, Kind: KindState, Height: 9,
		Root: tree.Root(), Key: "account:Yg", Proof: missingProof,
	}
	if leaf, exists, err := VerifySparseEnvelope(tree.Root(), 9, KindState, "account:Yg", missing); err != nil || exists || leaf != nil {
		t.Fatalf("missing exists=%t leaf=%q err=%v", exists, leaf, err)
	}
	if _, _, err := VerifySparseEnvelope(tree.Root(), 10, KindState, "account:YQ", envelope); err == nil {
		t.Fatal("sparse envelope trusted an unbound height")
	}
}

func TestSparseRootVector(t *testing.T) {
	tree, err := BuildSparseTree(DomainSparseState, map[string][]byte{
		"account:YQ": []byte(`{"kind":"account","key":"YQ","value":"MQ"}`),
		"account:Yg": []byte(`{"kind":"account","key":"Yg","value":"Mg"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = "0xba229820b828609379d1ca06df9eccbf5e5de1e23e31e66849006959337cf6bd"
	if tree.Root() != want {
		t.Fatalf("root = %s", tree.Root())
	}
}
