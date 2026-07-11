package proof

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"

	"golang.org/x/crypto/sha3"
)

const (
	SparseProtocol         = "chainlab-sparse-merkle-proof-v1"
	SparseEnvelopeProtocol = "chainlab-state-proof-v2"
	DomainSparseState      = "chainlab-state-v2"

	sparseDepth    = 256
	maxSparseKey   = 2048
	maxSparseValue = 16 * 1024 * 1024
)

type SparseProof struct {
	Protocol string   `json:"protocol"`
	Domain   string   `json:"domain"`
	Key      string   `json:"key"`
	Exists   bool     `json:"exists"`
	Value    string   `json:"value,omitempty"`
	Bitmap   string   `json:"bitmap"`
	Siblings []string `json:"siblings"`
}

type SparseEnvelope struct {
	Protocol string      `json:"protocol"`
	Kind     string      `json:"kind"`
	Height   int64       `json:"height"`
	Root     string      `json:"root"`
	Key      string      `json:"key"`
	Proof    SparseProof `json:"proof"`
}

type sparseNodeKey struct {
	Depth  uint16
	Prefix [32]byte
}

type sparseLeaf struct {
	Key   string
	Value []byte
}

// SparseTree is an in-memory fixed-depth authenticated map. Nodes equal to the
// deterministic empty hash at their depth are omitted.
type SparseTree struct {
	domain        string
	defaults      [sparseDepth + 1][32]byte
	parent        *SparseTree
	nodes         map[sparseNodeKey][32]byte
	deletedNodes  map[sparseNodeKey]struct{}
	leaves        map[[32]byte]sparseLeaf
	deletedLeaves map[[32]byte]struct{}
	entryCount    int
}

func NewSparseTree(domain string) (*SparseTree, error) {
	if err := validateDomain(domain); err != nil {
		return nil, err
	}
	tree := &SparseTree{
		domain: domain,
		nodes:  make(map[sparseNodeKey][32]byte),
		leaves: make(map[[32]byte]sparseLeaf),
	}
	tree.defaults[sparseDepth] = sparseEmptyLeafHash(domain)
	for depth := sparseDepth - 1; depth >= 0; depth-- {
		tree.defaults[depth] = sparseBranchHash(
			domain, uint16(depth), tree.defaults[depth+1], tree.defaults[depth+1],
		)
	}
	return tree, nil
}

func BuildSparseTree(domain string, entries map[string][]byte) (*SparseTree, error) {
	tree, err := NewSparseTree(domain)
	if err != nil {
		return nil, err
	}
	for key, value := range entries {
		if err := tree.Set(key, value); err != nil {
			return nil, err
		}
	}
	return tree, nil
}

func (t *SparseTree) Clone() *SparseTree {
	if t == nil {
		return nil
	}
	cloned := &SparseTree{
		domain: t.domain, defaults: t.defaults, parent: t,
		nodes: make(map[sparseNodeKey][32]byte), deletedNodes: make(map[sparseNodeKey]struct{}),
		leaves: make(map[[32]byte]sparseLeaf), deletedLeaves: make(map[[32]byte]struct{}),
		entryCount: t.entryCount,
	}
	return cloned
}

// Publish merges a cloned tree into its parent after the caller's transaction
// succeeds. Unpublished clones leave their parent unchanged.
func (t *SparseTree) Publish() *SparseTree {
	if t == nil || t.parent == nil {
		return t
	}
	base := t.parent.Publish()
	for key := range t.deletedNodes {
		delete(base.nodes, key)
	}
	for key, value := range t.nodes {
		base.nodes[key] = value
	}
	for path := range t.deletedLeaves {
		delete(base.leaves, path)
	}
	for path, leaf := range t.leaves {
		base.leaves[path] = sparseLeaf{Key: leaf.Key, Value: append([]byte(nil), leaf.Value...)}
	}
	base.entryCount = t.entryCount
	return base
}

func (t *SparseTree) Root() string {
	if t == nil {
		return ""
	}
	root := t.defaults[0]
	if value, exists := t.node(sparseNodeKey{}); exists {
		root = value
	}
	return sparseEncodeDigest(root)
}

func (t *SparseTree) EntryCount() int {
	if t == nil {
		return 0
	}
	return t.entryCount
}

func (t *SparseTree) Set(key string, value []byte) error {
	if t == nil {
		return errors.New("sparse tree is required")
	}
	if err := validateSparseKey(key); err != nil {
		return err
	}
	if len(value) == 0 || len(value) > maxSparseValue {
		return fmt.Errorf("sparse value must contain between 1 and %d bytes", maxSparseValue)
	}
	path := sparsePath(t.domain, key)
	existing, found := t.leaf(path)
	if found {
		if existing.Key != key {
			return errors.New("sparse key hash collision")
		}
		if bytes.Equal(existing.Value, value) {
			return nil
		}
	}
	if !found {
		t.entryCount++
	}
	t.leaves[path] = sparseLeaf{Key: key, Value: append([]byte(nil), value...)}
	delete(t.deletedLeaves, path)
	current := sparseLeafHash(t.domain, path, key, value)
	t.setNode(sparseNodeKey{Depth: sparseDepth, Prefix: path}, current)
	t.recalculatePath(path, current)
	return nil
}

func (t *SparseTree) Delete(key string) error {
	if t == nil {
		return errors.New("sparse tree is required")
	}
	if err := validateSparseKey(key); err != nil {
		return err
	}
	path := sparsePath(t.domain, key)
	existing, found := t.leaf(path)
	if !found {
		return nil
	}
	if existing.Key != key {
		return errors.New("sparse key hash collision")
	}
	t.deleteLeaf(path)
	t.entryCount--
	t.deleteNode(sparseNodeKey{Depth: sparseDepth, Prefix: path})
	t.recalculatePath(path, t.defaults[sparseDepth])
	return nil
}

func (t *SparseTree) Get(key string) ([]byte, bool, error) {
	if t == nil {
		return nil, false, errors.New("sparse tree is required")
	}
	if err := validateSparseKey(key); err != nil {
		return nil, false, err
	}
	path := sparsePath(t.domain, key)
	leaf, found := t.leaf(path)
	if !found {
		return nil, false, nil
	}
	if leaf.Key != key {
		return nil, false, errors.New("sparse key hash collision")
	}
	return append([]byte(nil), leaf.Value...), true, nil
}

func (t *SparseTree) Prove(key string) (SparseProof, error) {
	if t == nil {
		return SparseProof{}, errors.New("sparse tree is required")
	}
	if err := validateSparseKey(key); err != nil {
		return SparseProof{}, err
	}
	path := sparsePath(t.domain, key)
	leaf, exists := t.leaf(path)
	if exists && leaf.Key != key {
		return SparseProof{}, errors.New("sparse key hash collision")
	}
	bitmap := make([]byte, sparseDepth/8)
	siblings := make([]string, 0)
	for level := 0; level < sparseDepth; level++ {
		depth := sparseDepth - 1 - level
		key := sparseSiblingNodeKey(path, depth)
		sibling := t.defaults[depth+1]
		if value, found := t.node(key); found {
			sibling = value
		}
		if sibling != t.defaults[depth+1] {
			setSparseBitmapBit(bitmap, level)
			siblings = append(siblings, sparseEncodeDigest(sibling))
		}
	}
	proof := SparseProof{
		Protocol: SparseProtocol, Domain: t.domain, Key: key, Exists: exists,
		Bitmap: base64.RawURLEncoding.EncodeToString(bitmap), Siblings: siblings,
	}
	if exists {
		proof.Value = base64.RawURLEncoding.EncodeToString(leaf.Value)
	}
	return proof, nil
}

func VerifySparse(expectedRoot string, proof SparseProof) ([]byte, bool, error) {
	if proof.Protocol != SparseProtocol {
		return nil, false, fmt.Errorf("unsupported sparse proof protocol %q", proof.Protocol)
	}
	if err := validateDomain(proof.Domain); err != nil {
		return nil, false, err
	}
	if err := validateSparseKey(proof.Key); err != nil {
		return nil, false, err
	}
	bitmap, err := base64.RawURLEncoding.DecodeString(proof.Bitmap)
	if err != nil || len(bitmap) != sparseDepth/8 || base64.RawURLEncoding.EncodeToString(bitmap) != proof.Bitmap {
		return nil, false, errors.New("sparse proof bitmap is not canonical")
	}
	wantSiblings := 0
	for _, value := range bitmap {
		wantSiblings += bits.OnesCount8(value)
	}
	if len(proof.Siblings) != wantSiblings || len(proof.Siblings) > sparseDepth {
		return nil, false, errors.New("sparse proof sibling count is invalid")
	}
	defaults := sparseDefaults(proof.Domain)
	path := sparsePath(proof.Domain, proof.Key)
	current := defaults[sparseDepth]
	var leaf []byte
	if proof.Exists {
		leaf, err = base64.RawURLEncoding.DecodeString(proof.Value)
		if err != nil || len(leaf) == 0 || len(leaf) > maxSparseValue ||
			base64.RawURLEncoding.EncodeToString(leaf) != proof.Value {
			return nil, false, errors.New("sparse proof value is not canonical base64url")
		}
		current = sparseLeafHash(proof.Domain, path, proof.Key, leaf)
	} else if proof.Value != "" {
		return nil, false, errors.New("sparse non-membership proof must not contain a value")
	}
	siblingIndex := 0
	for level := 0; level < sparseDepth; level++ {
		depth := sparseDepth - 1 - level
		sibling := defaults[depth+1]
		if sparseBitmapBit(bitmap, level) {
			decoded, err := sparseDecodeDigest(proof.Siblings[siblingIndex])
			if err != nil {
				return nil, false, fmt.Errorf("sparse proof sibling: %w", err)
			}
			sibling = decoded
			siblingIndex++
		}
		if sparsePathBit(path, depth) == 0 {
			current = sparseBranchHash(proof.Domain, uint16(depth), current, sibling)
		} else {
			current = sparseBranchHash(proof.Domain, uint16(depth), sibling, current)
		}
	}
	expected, err := sparseDecodeDigest(expectedRoot)
	if err != nil {
		return nil, false, fmt.Errorf("expected sparse root: %w", err)
	}
	if current != expected {
		return nil, false, errors.New("sparse proof does not match expected root")
	}
	return append([]byte(nil), leaf...), proof.Exists, nil
}

func VerifySparseEnvelope(
	expectedRoot string,
	expectedHeight int64,
	expectedKind string,
	expectedKey string,
	envelope SparseEnvelope,
) ([]byte, bool, error) {
	if envelope.Protocol != SparseEnvelopeProtocol {
		return nil, false, fmt.Errorf("unsupported sparse envelope protocol %q", envelope.Protocol)
	}
	if expectedHeight < 0 || envelope.Height != expectedHeight {
		return nil, false, errors.New("sparse envelope height does not match the trusted height")
	}
	if envelope.Root != expectedRoot {
		return nil, false, errors.New("sparse envelope root does not match the trusted root")
	}
	if envelope.Kind != KindState || envelope.Kind != expectedKind || envelope.Key != expectedKey {
		return nil, false, errors.New("sparse envelope kind or key does not match the requested item")
	}
	if envelope.Proof.Domain != DomainSparseState || envelope.Proof.Key != envelope.Key {
		return nil, false, errors.New("sparse proof domain or key does not match the envelope")
	}
	leaf, exists, err := VerifySparse(expectedRoot, envelope.Proof)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, nil
	}
	var stateLeaf StateLeaf
	if err := json.Unmarshal(leaf, &stateLeaf); err != nil {
		return nil, false, errors.New("sparse state proof leaf is invalid")
	}
	canonical, err := json.Marshal(stateLeaf)
	if err != nil || !bytes.Equal(canonical, leaf) {
		return nil, false, errors.New("sparse state proof leaf is not canonical")
	}
	if err := validateStateLeaf(stateLeaf); err != nil {
		return nil, false, err
	}
	if envelope.Key != stateLeaf.Kind+":"+stateLeaf.Key {
		return nil, false, errors.New("sparse state proof key does not match the leaf")
	}
	return leaf, true, nil
}

func (t *SparseTree) recalculatePath(path [32]byte, current [32]byte) {
	for depth := sparseDepth - 1; depth >= 0; depth-- {
		siblingKey := sparseSiblingNodeKey(path, depth)
		sibling := t.defaults[depth+1]
		if value, exists := t.node(siblingKey); exists {
			sibling = value
		}
		if sparsePathBit(path, depth) == 0 {
			current = sparseBranchHash(t.domain, uint16(depth), current, sibling)
		} else {
			current = sparseBranchHash(t.domain, uint16(depth), sibling, current)
		}
		key := sparseNodeKeyAtDepth(path, depth)
		if current == t.defaults[depth] {
			t.deleteNode(key)
		} else {
			t.setNode(key, current)
		}
	}
}

func (t *SparseTree) node(key sparseNodeKey) ([32]byte, bool) {
	if value, exists := t.nodes[key]; exists {
		return value, true
	}
	if _, deleted := t.deletedNodes[key]; deleted {
		return [32]byte{}, false
	}
	if t.parent != nil {
		return t.parent.node(key)
	}
	return [32]byte{}, false
}

func (t *SparseTree) setNode(key sparseNodeKey, value [32]byte) {
	t.nodes[key] = value
	delete(t.deletedNodes, key)
}

func (t *SparseTree) deleteNode(key sparseNodeKey) {
	delete(t.nodes, key)
	if t.parent != nil {
		t.deletedNodes[key] = struct{}{}
	}
}

func (t *SparseTree) leaf(path [32]byte) (sparseLeaf, bool) {
	if leaf, exists := t.leaves[path]; exists {
		return leaf, true
	}
	if _, deleted := t.deletedLeaves[path]; deleted {
		return sparseLeaf{}, false
	}
	if t.parent != nil {
		return t.parent.leaf(path)
	}
	return sparseLeaf{}, false
}

func (t *SparseTree) deleteLeaf(path [32]byte) {
	delete(t.leaves, path)
	if t.parent != nil {
		t.deletedLeaves[path] = struct{}{}
	}
}

func sparseDefaults(domain string) [sparseDepth + 1][32]byte {
	var defaults [sparseDepth + 1][32]byte
	defaults[sparseDepth] = sparseEmptyLeafHash(domain)
	for depth := sparseDepth - 1; depth >= 0; depth-- {
		defaults[depth] = sparseBranchHash(domain, uint16(depth), defaults[depth+1], defaults[depth+1])
	}
	return defaults
}

func sparseNodeKeyAtDepth(path [32]byte, depth int) sparseNodeKey {
	prefix := path
	if depth < sparseDepth {
		byteIndex := depth / 8
		remaining := depth % 8
		if remaining == 0 {
			for index := byteIndex; index < len(prefix); index++ {
				prefix[index] = 0
			}
		} else {
			prefix[byteIndex] &= byte(0xff << (8 - remaining))
			for index := byteIndex + 1; index < len(prefix); index++ {
				prefix[index] = 0
			}
		}
	}
	return sparseNodeKey{Depth: uint16(depth), Prefix: prefix}
}

func sparseSiblingNodeKey(path [32]byte, parentDepth int) sparseNodeKey {
	sibling := path
	byteIndex := parentDepth / 8
	bitIndex := 7 - (parentDepth % 8)
	sibling[byteIndex] ^= 1 << bitIndex
	return sparseNodeKeyAtDepth(sibling, parentDepth+1)
}

func sparsePathBit(path [32]byte, depth int) byte {
	return (path[depth/8] >> (7 - (depth % 8))) & 1
}

func setSparseBitmapBit(bitmap []byte, level int) {
	bitmap[level/8] |= 1 << (7 - (level % 8))
}

func sparseBitmapBit(bitmap []byte, level int) bool {
	return bitmap[level/8]&(1<<(7-(level%8))) != 0
}

func sparsePath(domain string, key string) [32]byte {
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write([]byte{0x10, byte(len(domain))})
	_, _ = hasher.Write([]byte(domain))
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(key)))
	_, _ = hasher.Write(length[:])
	_, _ = hasher.Write([]byte(key))
	var result [32]byte
	copy(result[:], hasher.Sum(nil))
	return result
}

func sparseEmptyLeafHash(domain string) [32]byte {
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write([]byte{0x11, byte(len(domain))})
	_, _ = hasher.Write([]byte(domain))
	var result [32]byte
	copy(result[:], hasher.Sum(nil))
	return result
}

func sparseLeafHash(domain string, path [32]byte, key string, value []byte) [32]byte {
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write([]byte{0x12, byte(len(domain))})
	_, _ = hasher.Write([]byte(domain))
	_, _ = hasher.Write(path[:])
	var keyLength [4]byte
	binary.BigEndian.PutUint32(keyLength[:], uint32(len(key)))
	_, _ = hasher.Write(keyLength[:])
	_, _ = hasher.Write([]byte(key))
	var valueLength [8]byte
	binary.BigEndian.PutUint64(valueLength[:], uint64(len(value)))
	_, _ = hasher.Write(valueLength[:])
	_, _ = hasher.Write(value)
	var result [32]byte
	copy(result[:], hasher.Sum(nil))
	return result
}

func sparseBranchHash(domain string, depth uint16, left [32]byte, right [32]byte) [32]byte {
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write([]byte{0x13, byte(len(domain))})
	_, _ = hasher.Write([]byte(domain))
	var encodedDepth [2]byte
	binary.BigEndian.PutUint16(encodedDepth[:], depth)
	_, _ = hasher.Write(encodedDepth[:])
	_, _ = hasher.Write(left[:])
	_, _ = hasher.Write(right[:])
	var result [32]byte
	copy(result[:], hasher.Sum(nil))
	return result
}

func validateSparseKey(key string) error {
	if len(key) == 0 || len(key) > maxSparseKey {
		return fmt.Errorf("sparse key must contain between 1 and %d bytes", maxSparseKey)
	}
	for _, value := range []byte(key) {
		if value < 0x21 || value > 0x7e {
			return errors.New("sparse key is not canonical visible ASCII")
		}
	}
	return nil
}

func sparseEncodeDigest(value [32]byte) string {
	return "0x" + hex.EncodeToString(value[:])
}

func sparseDecodeDigest(value string) ([32]byte, error) {
	var result [32]byte
	if len(value) != 66 || value[:2] != "0x" {
		return result, errors.New("digest must be a canonical 32-byte hex value")
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil || len(decoded) != len(result) || "0x"+hex.EncodeToString(decoded) != value {
		return result, errors.New("digest must be lowercase canonical hex")
	}
	copy(result[:], decoded)
	return result, nil
}
