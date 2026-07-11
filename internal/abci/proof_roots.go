package abci

import (
	"encoding/base64"
	"errors"

	"chainlab/internal/hash"
	"chainlab/internal/state"
	"chainlab/internal/types"
	chainproof "chainlab/pkg/proof"
)

func transactionRootForProtocol(protocol string, txs []types.Transaction) (string, error) {
	if protocolUsesMerkleProofs(protocol) {
		return types.TransactionMerkleRoot(txs)
	}
	return types.TransactionRoot(txs), nil
}

func decodeFlatCommitmentKey(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) == 0 || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("state proof key is not canonical base64url")
	}
	return decoded, nil
}

func receiptRootForProtocol(protocol string, receipts []types.Receipt) (string, error) {
	if protocolUsesMerkleProofs(protocol) {
		return types.ReceiptMerkleRoot(receipts)
	}
	return types.ReceiptRoot(receipts), nil
}

func stateRootForProtocol(protocol string, store *state.Store) (string, error) {
	if store == nil {
		return "", errors.New("state store is required")
	}
	if !protocolUsesMerkleProofs(protocol) {
		return store.Root(), nil
	}
	if protocolUsesSparseState(protocol) {
		tree, err := buildStoreSparseTree(store)
		if err != nil {
			return "", err
		}
		return tree.Root(), nil
	}
	_, leaves, err := stateMerkleLeaves(store)
	if err != nil {
		return "", err
	}
	return chainproof.Root(chainproof.DomainState, leaves)
}

func stateRootForCommitted(protocol string, committed committedState) (string, error) {
	if !protocolUsesSparseState(protocol) {
		return stateRootForProtocol(protocol, committed.store)
	}
	if committed.flatTree == nil {
		return "", errors.New("sparse state tree is required")
	}
	return committed.flatTree.Root(), nil
}

func stateMerkleProof(store *state.Store, kind string, key []byte) (flatStateCommitmentEntry, chainproof.Proof, error) {
	entries, leaves, err := stateMerkleLeaves(store)
	if err != nil {
		return flatStateCommitmentEntry{}, chainproof.Proof{}, err
	}
	wanted := flatStateID(kind, key)
	for index, entry := range entries {
		decodedKey, err := decodeFlatCommitmentKey(entry.Key)
		if err != nil {
			return flatStateCommitmentEntry{}, chainproof.Proof{}, err
		}
		if flatStateID(entry.Kind, decodedKey) == wanted {
			proof, err := chainproof.Build(chainproof.DomainState, leaves, uint64(index))
			return entry, proof, err
		}
	}
	return flatStateCommitmentEntry{}, chainproof.Proof{}, errors.New("state proof entry is unavailable")
}

func stateMerkleLeaves(store *state.Store) ([]flatStateCommitmentEntry, [][]byte, error) {
	if store == nil {
		return nil, nil, errors.New("state store is required")
	}
	flat, err := flattenStateSnapshot(store.Snapshot())
	if err != nil {
		return nil, nil, err
	}
	ordered := flatEntriesSorted(flat)
	entries := make([]flatStateCommitmentEntry, len(ordered))
	leaves := make([][]byte, len(ordered))
	for index, item := range ordered {
		entries[index] = flatCommitmentEntry(item)
		leaves[index], err = hash.CanonicalBytes(entries[index])
		if err != nil {
			return nil, nil, err
		}
	}
	return entries, leaves, nil
}
