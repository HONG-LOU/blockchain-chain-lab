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
	raw, protocol, err := loadProofDocument(*proofPath)
	if err != nil {
		return err
	}
	var leaf []byte
	exists := true
	switch protocol {
	case chainproof.EnvelopeProtocol:
		var envelope chainproof.Envelope
		if err := decodeCanonicalProof(raw, &envelope); err != nil {
			return err
		}
		leaf, err = chainproof.VerifyEnvelope(
			*trustedRoot, *trustedHeight, *expectedKind, *expectedKey, envelope,
		)
	case chainproof.SparseEnvelopeProtocol:
		var envelope chainproof.SparseEnvelope
		if err := decodeCanonicalProof(raw, &envelope); err != nil {
			return err
		}
		leaf, exists, err = chainproof.VerifySparseEnvelope(
			*trustedRoot, *trustedHeight, *expectedKind, *expectedKey, envelope,
		)
	default:
		return fmt.Errorf("unsupported proof envelope protocol %q", protocol)
	}
	if err != nil {
		return fmt.Errorf("verify proof: %w", err)
	}
	_, err = fmt.Fprintf(
		out, "verified kind=%s height=%d key=%s exists=%t leaf_bytes=%d\n",
		*expectedKind, *trustedHeight, *expectedKey, exists, len(leaf),
	)
	return err
}

func loadProofEnvelope(path string) (chainproof.Envelope, error) {
	raw, protocol, err := loadProofDocument(path)
	if err != nil {
		return chainproof.Envelope{}, err
	}
	if protocol != chainproof.EnvelopeProtocol {
		return chainproof.Envelope{}, fmt.Errorf("unsupported inclusion proof protocol %q", protocol)
	}
	var envelope chainproof.Envelope
	if err := decodeCanonicalProof(raw, &envelope); err != nil {
		return chainproof.Envelope{}, err
	}
	return envelope, nil
}

func loadProofDocument(path string) ([]byte, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", fmt.Errorf("inspect proof file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, "", errors.New("proof path must be a regular non-symlink file")
	}
	if info.Size() <= 0 || info.Size() > maxProofFileBytes {
		return nil, "", fmt.Errorf("proof file must contain between 1 and %d bytes", maxProofFileBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read proof file: %w", err)
	}
	var header struct {
		Protocol string `json:"protocol"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return nil, "", fmt.Errorf("decode proof envelope header: %w", err)
	}
	return raw, header.Protocol, nil
}

func decodeCanonicalProof(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode proof envelope: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	canonical, err := json.Marshal(destination)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, canonical) {
		return errors.New("proof envelope is not canonically encoded")
	}
	return nil
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
