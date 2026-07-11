package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	chainproof "chainlab/pkg/proof"
)

func TestRunVerifiesCanonicalEnvelopeAgainstTrustedRoot(t *testing.T) {
	leaves := [][]byte{[]byte("first"), []byte("second")}
	root, err := chainproof.Root(chainproof.DomainTx, leaves)
	if err != nil {
		t.Fatal(err)
	}
	merkleProof, err := chainproof.Build(chainproof.DomainTx, leaves, 1)
	if err != nil {
		t.Fatal(err)
	}
	envelope := chainproof.Envelope{
		Protocol: chainproof.EnvelopeProtocol, Kind: chainproof.KindTransaction,
		Height: 12, Root: root, Key: "1", Proof: merkleProof,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "proof.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	args := []string{
		"--proof", path, "--root", root, "--height", "12", "--kind", "transaction", "--key", "1",
	}
	if err := run(args, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "verified kind=transaction height=12 key=1") {
		t.Fatalf("output = %q", out.String())
	}
	args[3] = "0x" + strings.Repeat("00", 32)
	if err := run(args, &out); err == nil {
		t.Fatal("untrusted root was accepted")
	}
}

func TestLoadProofEnvelopeRejectsUnknownOrNonCanonicalJSON(t *testing.T) {
	root := t.TempDir()
	for name, raw := range map[string]string{
		"unknown.json":  `{"protocol":"chainlab-inclusion-proof-v1","kind":"transaction","height":1,"root":"x","key":"0","proof":{},"extra":true}`,
		"newline.json":  "{}\n",
		"multiple.json": "{}{}",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name)
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadProofEnvelope(path); err == nil {
				t.Fatal("invalid proof JSON was accepted")
			}
		})
	}
}

func TestRunVerifiesSparseMembershipAndNonMembership(t *testing.T) {
	tree, err := chainproof.NewSparseTree(chainproof.DomainSparseState)
	if err != nil {
		t.Fatal(err)
	}
	key := "account:YQ"
	leaf := []byte(`{"kind":"account","key":"YQ","value":"MQ"}`)
	if err := tree.Set(key, leaf); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		key    string
		exists bool
	}{
		{name: "membership", key: key, exists: true},
		{name: "non-membership", key: "account:Yg", exists: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			proof, err := tree.Prove(test.key)
			if err != nil {
				t.Fatal(err)
			}
			envelope := chainproof.SparseEnvelope{
				Protocol: chainproof.SparseEnvelopeProtocol, Kind: chainproof.KindState,
				Height: 8, Root: tree.Root(), Key: test.key, Proof: proof,
			}
			raw, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "proof.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = run([]string{
				"--proof", path, "--root", tree.Root(), "--height", "8",
				"--kind", "state", "--key", test.key,
			}, &out)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "exists="+strconv.FormatBool(test.exists)) {
				t.Fatalf("output = %q", out.String())
			}
		})
	}
}
