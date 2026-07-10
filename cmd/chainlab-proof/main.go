package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	chainproof "chainlab/pkg/proof"
)

const maxProofFileBytes = 16 * 1024 * 1024

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("chainlab-proof", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	proofPath := flags.String("proof", "", "canonical ChainLab proof envelope")
	trustedRoot := flags.String("root", "", "trusted root from a verified commitment")
	trustedHeight := flags.Int64("height", -1, "trusted committed height")
	expectedKind := flags.String("kind", "", "expected proof kind: transaction, receipt, or state")
	expectedKey := flags.String("key", "", "expected transaction index, receipt index, or flat-state key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("chainlab-proof does not accept positional arguments")
	}
	if strings.TrimSpace(*proofPath) == "" || strings.TrimSpace(*trustedRoot) == "" ||
		*trustedHeight < 0 || strings.TrimSpace(*expectedKind) == "" || strings.TrimSpace(*expectedKey) == "" {
		return errors.New("chainlab-proof requires --proof, --root, --height, --kind, and --key")
	}
	envelope, err := loadProofEnvelope(*proofPath)
	if err != nil {
		return err
	}
	leaf, err := chainproof.VerifyEnvelope(
		*trustedRoot, *trustedHeight, *expectedKind, *expectedKey, envelope,
	)
	if err != nil {
		return fmt.Errorf("verify inclusion proof: %w", err)
	}
	_, err = fmt.Fprintf(
		out, "verified kind=%s height=%d key=%s leaf_bytes=%d\n",
		envelope.Kind, envelope.Height, envelope.Key, len(leaf),
	)
	return err
}

func loadProofEnvelope(path string) (chainproof.Envelope, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return chainproof.Envelope{}, fmt.Errorf("inspect proof file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return chainproof.Envelope{}, errors.New("proof path must be a regular non-symlink file")
	}
	if info.Size() <= 0 || info.Size() > maxProofFileBytes {
		return chainproof.Envelope{}, fmt.Errorf("proof file must contain between 1 and %d bytes", maxProofFileBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return chainproof.Envelope{}, fmt.Errorf("read proof file: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope chainproof.Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return chainproof.Envelope{}, fmt.Errorf("decode proof envelope: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return chainproof.Envelope{}, err
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return chainproof.Envelope{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return chainproof.Envelope{}, errors.New("proof envelope is not canonically encoded")
	}
	return envelope, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("proof file contains multiple JSON values")
		}
		return fmt.Errorf("decode trailing proof data: %w", err)
	}
	return nil
}
