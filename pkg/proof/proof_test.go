package proof

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestProofRoundTripAcrossTreeShapes(t *testing.T) {
	for total := 1; total <= 17; total++ {
		leaves := make([][]byte, total)
		for index := range leaves {
			leaves[index] = []byte(strings.Repeat(string(rune('a'+index)), index+1))
		}
		root, err := Root(DomainTx, leaves)
		if err != nil {
			t.Fatal(err)
		}
		for index := range leaves {
			merkleProof, err := Build(DomainTx, leaves, uint64(index))
			if err != nil {
				t.Fatal(err)
			}
			verified, err := Verify(root, merkleProof)
			if err != nil {
				t.Fatalf("total=%d index=%d: %v", total, index, err)
			}
			if !bytes.Equal(verified, leaves[index]) {
				t.Fatalf("total=%d index=%d leaf=%q", total, index, verified)
			}
		}
	}
}

func TestProofRejectsTamperingAndNonCanonicalEncoding(t *testing.T) {
	leaves := [][]byte{[]byte("alpha"), []byte("beta"), []byte("gamma")}
	root, err := Root(DomainReceipt, leaves)
	if err != nil {
		t.Fatal(err)
	}
	original, err := Build(DomainReceipt, leaves, 1)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		root   string
		mutate func(*Proof)
	}{
		{name: "root", root: "0x" + strings.Repeat("00", 32)},
		{name: "protocol", root: root, mutate: func(value *Proof) { value.Protocol = "other" }},
		{name: "domain", root: root, mutate: func(value *Proof) { value.Domain = DomainState }},
		{name: "index", root: root, mutate: func(value *Proof) { value.Index = 2 }},
		{name: "total", root: root, mutate: func(value *Proof) { value.Total = 4 }},
		{name: "leaf", root: root, mutate: func(value *Proof) { value.Leaf = "dGFtcGVyZWQ" }},
		{name: "padded base64", root: root, mutate: func(value *Proof) { value.Leaf += "=" }},
		{name: "missing sibling", root: root, mutate: func(value *Proof) { value.Siblings = value.Siblings[:1] }},
		{name: "uppercase sibling", root: root, mutate: func(value *Proof) { value.Siblings[0] = strings.ToUpper(value.Siblings[0]) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneProof(original)
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			if _, err := Verify(test.root, candidate); err == nil {
				t.Fatal("tampered proof verified")
			}
		})
	}
}

func TestEmptyRootsAreDomainSeparatedAndCannotBuildProof(t *testing.T) {
	txRoot, err := Root(DomainTx, nil)
	if err != nil {
		t.Fatal(err)
	}
	receiptRoot, err := Root(DomainReceipt, nil)
	if err != nil {
		t.Fatal(err)
	}
	if txRoot == receiptRoot {
		t.Fatal("empty roots are not domain separated")
	}
	if _, err := Build(DomainTx, nil, 0); err == nil {
		t.Fatal("empty proof was built")
	}
}

func TestEnvelopeRequiresTrustedRootAndBindsStateKey(t *testing.T) {
	stateLeaf, err := json.Marshal(StateLeaf{Kind: "account", Key: "YWNjb3VudA", Value: "dmFsdWU"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := Root(DomainState, [][]byte{stateLeaf})
	if err != nil {
		t.Fatal(err)
	}
	merkleProof, err := Build(DomainState, [][]byte{stateLeaf}, 0)
	if err != nil {
		t.Fatal(err)
	}
	envelope := Envelope{
		Protocol: EnvelopeProtocol, Kind: KindState, Height: 7, Root: root,
		Key: "account:YWNjb3VudA", Proof: merkleProof,
	}
	if _, err := VerifyEnvelope(root, 7, KindState, "account:YWNjb3VudA", envelope); err != nil {
		t.Fatal(err)
	}
	tampered := envelope
	tampered.Key = "account:b3RoZXI"
	if _, err := VerifyEnvelope(root, 7, KindState, "account:YWNjb3VudA", tampered); err == nil {
		t.Fatal("state envelope accepted a mismatched key")
	}
	if _, err := VerifyEnvelope(
		"0x"+strings.Repeat("00", 32), 7, KindState, "account:YWNjb3VudA", envelope,
	); err == nil {
		t.Fatal("envelope trusted its embedded root")
	}
	if _, err := VerifyEnvelope(root, 8, KindState, "account:YWNjb3VudA", envelope); err == nil {
		t.Fatal("envelope trusted its embedded height")
	}
}

func TestMerkleRootVector(t *testing.T) {
	root, err := Root(DomainTx, [][]byte{[]byte("alpha"), []byte("beta"), []byte("gamma")})
	if err != nil {
		t.Fatal(err)
	}
	const want = "0xb3547e5ca53a8a9c54a74de0947d22ec8193b5570a70736bd1cc227fa0346a8b"
	if root != want {
		t.Fatalf("root = %s", root)
	}
}

func cloneProof(input Proof) Proof {
	input.Siblings = append([]string(nil), input.Siblings...)
	return input
}
