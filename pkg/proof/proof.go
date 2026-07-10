package proof

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"golang.org/x/crypto/sha3"
)

const (
	Protocol         = "chainlab-merkle-proof-v1"
	EnvelopeProtocol = "chainlab-inclusion-proof-v1"
	DomainTx         = "chainlab-tx-v1"
	DomainReceipt    = "chainlab-receipt-v1"
	DomainState      = "chainlab-state-v1"
	KindTransaction  = "transaction"
	KindReceipt      = "receipt"
	KindState        = "state"

	maxDomainBytes = 64
	maxProofDepth  = 63
)

type Proof struct {
	Protocol string   `json:"protocol"`
	Domain   string   `json:"domain"`
	Index    uint64   `json:"index"`
	Total    uint64   `json:"total"`
	Leaf     string   `json:"leaf"`
	Siblings []string `json:"siblings"`
}

type Envelope struct {
	Protocol string `json:"protocol"`
	Kind     string `json:"kind"`
	Height   int64  `json:"height"`
	Root     string `json:"root"`
	Key      string `json:"key"`
	Proof    Proof  `json:"proof"`
}

type StateLeaf struct {
	Kind  string `json:"kind"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

func Root(domain string, leaves [][]byte) (string, error) {
	if err := validateDomain(domain); err != nil {
		return "", err
	}
	if len(leaves) == 0 {
		return encodeDigest(emptyHash(domain)), nil
	}
	nodes, err := leafLevel(domain, leaves)
	if err != nil {
		return "", err
	}
	for len(nodes) > 1 {
		nodes = parentLevel(domain, uint64(len(leaves)), nodes)
	}
	return encodeDigest(nodes[0]), nil
}

func Build(domain string, leaves [][]byte, index uint64) (Proof, error) {
	if err := validateDomain(domain); err != nil {
		return Proof{}, err
	}
	if len(leaves) == 0 || index >= uint64(len(leaves)) {
		return Proof{}, errors.New("proof index is outside the leaf set")
	}
	nodes, err := leafLevel(domain, leaves)
	if err != nil {
		return Proof{}, err
	}
	proof := Proof{
		Protocol: Protocol,
		Domain:   domain,
		Index:    index,
		Total:    uint64(len(leaves)),
		Leaf:     base64.RawURLEncoding.EncodeToString(leaves[index]),
		Siblings: make([]string, 0, proofDepth(uint64(len(nodes)))),
	}
	position := index
	for len(nodes) > 1 {
		proof.Siblings = append(proof.Siblings, encodeDigest(nodes[position^1]))
		nodes = parentLevel(domain, proof.Total, nodes)
		position /= 2
	}
	return proof, nil
}

func Verify(expectedRoot string, proof Proof) ([]byte, error) {
	if proof.Protocol != Protocol {
		return nil, fmt.Errorf("unsupported proof protocol %q", proof.Protocol)
	}
	if err := validateDomain(proof.Domain); err != nil {
		return nil, err
	}
	if proof.Total == 0 || proof.Index >= proof.Total {
		return nil, errors.New("proof index or total is invalid")
	}
	treeSize, err := paddedTreeSize(proof.Total)
	if err != nil {
		return nil, err
	}
	if len(proof.Siblings) != proofDepth(treeSize) || len(proof.Siblings) > maxProofDepth {
		return nil, errors.New("proof sibling count is invalid")
	}
	leaf, err := base64.RawURLEncoding.DecodeString(proof.Leaf)
	if err != nil || base64.RawURLEncoding.EncodeToString(leaf) != proof.Leaf {
		return nil, errors.New("proof leaf is not canonical base64url")
	}
	expected, err := decodeDigest(expectedRoot)
	if err != nil {
		return nil, fmt.Errorf("expected root: %w", err)
	}
	current := leafHash(proof.Domain, proof.Total, proof.Index, leaf)
	position := proof.Index
	for _, encoded := range proof.Siblings {
		sibling, err := decodeDigest(encoded)
		if err != nil {
			return nil, fmt.Errorf("proof sibling: %w", err)
		}
		if position&1 == 0 {
			current = parentHash(proof.Domain, proof.Total, current, sibling)
		} else {
			current = parentHash(proof.Domain, proof.Total, sibling, current)
		}
		position /= 2
	}
	if !bytes.Equal(current, expected) {
		return nil, errors.New("proof does not match expected root")
	}
	return append([]byte(nil), leaf...), nil
}

func VerifyEnvelope(
	expectedRoot string,
	expectedHeight int64,
	expectedKind string,
	expectedKey string,
	envelope Envelope,
) ([]byte, error) {
	if envelope.Protocol != EnvelopeProtocol {
		return nil, fmt.Errorf("unsupported proof envelope protocol %q", envelope.Protocol)
	}
	if expectedHeight < 0 || envelope.Height != expectedHeight {
		return nil, errors.New("proof envelope height does not match the trusted height")
	}
	if envelope.Root != expectedRoot {
		return nil, errors.New("proof envelope root does not match the trusted root")
	}
	if envelope.Kind != expectedKind || envelope.Key != expectedKey {
		return nil, errors.New("proof envelope kind or key does not match the requested item")
	}
	wantDomain := ""
	switch envelope.Kind {
	case KindTransaction:
		wantDomain = DomainTx
	case KindReceipt:
		wantDomain = DomainReceipt
	case KindState:
		wantDomain = DomainState
	default:
		return nil, fmt.Errorf("unsupported proof kind %q", envelope.Kind)
	}
	if envelope.Proof.Domain != wantDomain {
		return nil, errors.New("proof domain does not match envelope kind")
	}
	leaf, err := Verify(expectedRoot, envelope.Proof)
	if err != nil {
		return nil, err
	}
	if envelope.Kind == KindState {
		var stateLeaf StateLeaf
		if err := json.Unmarshal(leaf, &stateLeaf); err != nil {
			return nil, errors.New("state proof leaf is invalid")
		}
		canonical, err := json.Marshal(stateLeaf)
		if err != nil || !bytes.Equal(canonical, leaf) || stateLeaf.Kind == "" || stateLeaf.Key == "" {
			return nil, errors.New("state proof leaf is not canonical")
		}
		if err := validateStateLeaf(stateLeaf); err != nil {
			return nil, err
		}
		if envelope.Key != stateLeaf.Kind+":"+stateLeaf.Key {
			return nil, errors.New("state proof key does not match the leaf")
		}
	} else if envelope.Key != strconv.FormatUint(envelope.Proof.Index, 10) {
		return nil, errors.New("indexed proof key does not match the leaf index")
	}
	return leaf, nil
}

func validateStateLeaf(leaf StateLeaf) error {
	for _, value := range []byte(leaf.Kind) {
		if (value < 'a' || value > 'z') && value != '_' {
			return errors.New("state proof kind is not canonical")
		}
	}
	if err := validateBase64URL("key", leaf.Key); err != nil {
		return err
	}
	if err := validateBase64URL("value", leaf.Value); err != nil {
		return err
	}
	return nil
}

func validateBase64URL(name string, encoded string) error {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) == 0 || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return fmt.Errorf("state proof %s is not canonical base64url", name)
	}
	return nil
}

func leafLevel(domain string, leaves [][]byte) ([][]byte, error) {
	if uint64(len(leaves)) > math.MaxUint64/2 {
		return nil, errors.New("proof leaf count is too large")
	}
	size, err := paddedTreeSize(uint64(len(leaves)))
	if err != nil || size > uint64(math.MaxInt) {
		return nil, errors.New("proof leaf count exceeds platform capacity")
	}
	nodes := make([][]byte, int(size))
	for index := uint64(0); index < size; index++ {
		if index < uint64(len(leaves)) {
			nodes[index] = leafHash(domain, uint64(len(leaves)), index, leaves[index])
		} else {
			nodes[index] = paddingHash(domain, uint64(len(leaves)), index)
		}
	}
	return nodes, nil
}

func parentLevel(domain string, total uint64, nodes [][]byte) [][]byte {
	parents := make([][]byte, len(nodes)/2)
	for index := range parents {
		parents[index] = parentHash(domain, total, nodes[index*2], nodes[index*2+1])
	}
	return parents
}

func paddedTreeSize(total uint64) (uint64, error) {
	if total == 0 {
		return 0, errors.New("proof leaf count must be positive")
	}
	size := uint64(1)
	for size < total {
		if size > math.MaxUint64/2 {
			return 0, errors.New("proof leaf count is too large")
		}
		size *= 2
	}
	return size, nil
}

func proofDepth(size uint64) int {
	depth := 0
	for size > 1 {
		depth++
		size /= 2
	}
	return depth
}

func emptyHash(domain string) []byte {
	return digest(0x03, domain, 0, 0, nil, nil)
}

func leafHash(domain string, total uint64, index uint64, leaf []byte) []byte {
	return digest(0x00, domain, total, index, leaf, nil)
}

func paddingHash(domain string, total uint64, index uint64) []byte {
	return digest(0x02, domain, total, index, nil, nil)
}

func parentHash(domain string, total uint64, left []byte, right []byte) []byte {
	return digest(0x01, domain, total, 0, left, right)
}

func digest(kind byte, domain string, total uint64, index uint64, first []byte, second []byte) []byte {
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write([]byte{kind, byte(len(domain))})
	_, _ = hasher.Write([]byte(domain))
	var integer [8]byte
	binary.BigEndian.PutUint64(integer[:], total)
	_, _ = hasher.Write(integer[:])
	binary.BigEndian.PutUint64(integer[:], index)
	_, _ = hasher.Write(integer[:])
	binary.BigEndian.PutUint64(integer[:], uint64(len(first)))
	_, _ = hasher.Write(integer[:])
	_, _ = hasher.Write(first)
	binary.BigEndian.PutUint64(integer[:], uint64(len(second)))
	_, _ = hasher.Write(integer[:])
	_, _ = hasher.Write(second)
	return hasher.Sum(nil)
}

func validateDomain(domain string) error {
	if len(domain) == 0 || len(domain) > maxDomainBytes {
		return fmt.Errorf("proof domain must contain between 1 and %d bytes", maxDomainBytes)
	}
	for _, value := range []byte(domain) {
		if (value < 'a' || value > 'z') && (value < '0' || value > '9') &&
			value != '-' && value != '_' && value != '.' && value != '/' {
			return errors.New("proof domain is not canonical ASCII")
		}
	}
	return nil
}

func encodeDigest(value []byte) string {
	return "0x" + hex.EncodeToString(value)
}

func decodeDigest(value string) ([]byte, error) {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") {
		return nil, errors.New("digest must be a canonical 32-byte hex value")
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil || encodeDigest(decoded) != value {
		return nil, errors.New("digest must be lowercase canonical hex")
	}
	return decoded, nil
}
