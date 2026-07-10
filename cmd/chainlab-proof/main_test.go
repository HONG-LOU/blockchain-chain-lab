package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
