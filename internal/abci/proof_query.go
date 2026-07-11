package abci

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
	chainproof "chainlab/pkg/proof"
)

var ErrInvalidProofQuery = errors.New("invalid inclusion proof query")

func (a *Application) buildProofEnvelope(
	path string,
	data []byte,
	height int64,
	committed committedState,
	rawTxs [][]byte,
	receipts []types.Receipt,
) (chainproof.Envelope, []byte, error) {
	envelope := chainproof.Envelope{
		Protocol: chainproof.EnvelopeProtocol,
		Height:   height,
	}
	var responseKey []byte
	switch path {
	case "/proof/transaction":
		index, err := proofQueryIndex(data, len(rawTxs))
		if err != nil {
			return chainproof.Envelope{}, nil, err
		}
		txs := make([]types.Transaction, len(rawTxs))
		for txIndex, raw := range rawTxs {
			tx, err := decodeTransaction(raw, a.genesis.ChainID, a.genesis.BlockGasLimit)
			if err != nil {
				return chainproof.Envelope{}, nil, fmt.Errorf("decode proof transaction %d: %w", txIndex, err)
			}
			txs[txIndex] = tx
		}
		merkleProof, err := types.TransactionMerkleProof(txs, index)
		if err != nil {
			return chainproof.Envelope{}, nil, err
		}
		envelope.Kind = chainproof.KindTransaction
		envelope.Root = committed.commitment.TxRoot
		envelope.Key = strconv.FormatUint(index, 10)
		envelope.Proof = merkleProof
		responseKey = []byte(envelope.Key)
	case "/proof/receipt":
		index, err := proofQueryIndex(data, len(receipts))
		if err != nil {
			return chainproof.Envelope{}, nil, err
		}
		merkleProof, err := types.ReceiptMerkleProof(receipts, index)
		if err != nil {
			return chainproof.Envelope{}, nil, err
		}
		envelope.Kind = chainproof.KindReceipt
		envelope.Root = committed.commitment.ReceiptRoot
		envelope.Key = strconv.FormatUint(index, 10)
		envelope.Proof = merkleProof
		responseKey = []byte(envelope.Key)
	case "/proof/account":
		address, err := chaincrypto.NormalizeAddress(string(data))
		if err != nil || address != string(data) {
			return chainproof.Envelope{}, nil, fmt.Errorf("%w: account address is not canonical", ErrInvalidProofQuery)
		}
		_, merkleProof, err := stateMerkleProof(committed.store, flatKindAccount, []byte(address))
		if err != nil {
			return chainproof.Envelope{}, nil, fmt.Errorf("%w: account state is unavailable", ErrInvalidProofQuery)
		}
		envelope.Kind = chainproof.KindState
		envelope.Root = committed.commitment.StateRoot
		envelope.Key = flatStateID(flatKindAccount, []byte(address))
		envelope.Proof = merkleProof
		responseKey = []byte(address)
	default:
		return chainproof.Envelope{}, nil, fmt.Errorf("%w: unsupported proof path", ErrInvalidProofQuery)
	}
	if _, err := chainproof.VerifyEnvelope(
		envelope.Root, envelope.Height, envelope.Kind, envelope.Key, envelope,
	); err != nil {
		return chainproof.Envelope{}, nil, fmt.Errorf("verify generated inclusion proof: %w", err)
	}
	return envelope, responseKey, nil
}

func (a *Application) buildSparseProofEnvelope(
	path string,
	data []byte,
	height int64,
	committed committedState,
) (chainproof.SparseEnvelope, []byte, error) {
	if committed.flatTree == nil {
		return chainproof.SparseEnvelope{}, nil, errors.New("sparse state tree is unavailable")
	}
	var kind string
	var key []byte
	var responseKey []byte
	switch path {
	case "/proof/account":
		address, err := chaincrypto.NormalizeAddress(string(data))
		if err != nil || address != string(data) {
			return chainproof.SparseEnvelope{}, nil, fmt.Errorf("%w: account address is not canonical", ErrInvalidProofQuery)
		}
		kind = flatKindAccount
		key = []byte(address)
		responseKey = []byte(address)
	case "/proof/state":
		var err error
		kind, key, err = parseFlatProofKey(string(data))
		if err != nil {
			return chainproof.SparseEnvelope{}, nil, fmt.Errorf("%w: %v", ErrInvalidProofQuery, err)
		}
		responseKey = append([]byte(nil), data...)
	default:
		return chainproof.SparseEnvelope{}, nil, fmt.Errorf("%w: unsupported sparse proof path", ErrInvalidProofQuery)
	}
	sparseKey := flatStateID(kind, key)
	proof, err := committed.flatTree.Prove(sparseKey)
	if err != nil {
		return chainproof.SparseEnvelope{}, nil, err
	}
	envelope := chainproof.SparseEnvelope{
		Protocol: chainproof.SparseEnvelopeProtocol,
		Kind:     chainproof.KindState, Height: height,
		Root: committed.commitment.StateRoot, Key: sparseKey, Proof: proof,
	}
	if _, _, err := chainproof.VerifySparseEnvelope(
		envelope.Root, envelope.Height, envelope.Kind, envelope.Key, envelope,
	); err != nil {
		return chainproof.SparseEnvelope{}, nil, fmt.Errorf("verify generated sparse state proof: %w", err)
	}
	return envelope, responseKey, nil
}

func parseFlatProofKey(value string) (string, []byte, error) {
	kind, encoded, found := strings.Cut(value, ":")
	if !found || !validFlatStateKind(kind) || encoded == "" {
		return "", nil, errors.New("flat-state proof key must be kind:base64url-key")
	}
	key, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(key) == 0 || base64.RawURLEncoding.EncodeToString(key) != encoded ||
		flatStateID(kind, key) != value {
		return "", nil, errors.New("flat-state proof key is not canonical")
	}
	return kind, key, nil
}

func proofQueryIndex(data []byte, count int) (uint64, error) {
	encoded := string(data)
	index, err := strconv.ParseUint(encoded, 10, 64)
	if err != nil || strconv.FormatUint(index, 10) != encoded || index >= uint64(count) {
		return 0, fmt.Errorf("%w: proof index is unavailable", ErrInvalidProofQuery)
	}
	return index, nil
}
