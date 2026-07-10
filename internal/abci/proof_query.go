package abci

import (
	"errors"
	"fmt"
	"strconv"

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

func proofQueryIndex(data []byte, count int) (uint64, error) {
	encoded := string(data)
	index, err := strconv.ParseUint(encoded, 10, 64)
	if err != nil || strconv.FormatUint(index, 10) != encoded || index >= uint64(count) {
		return 0, fmt.Errorf("%w: proof index is unavailable", ErrInvalidProofQuery)
	}
	return index, nil
}
