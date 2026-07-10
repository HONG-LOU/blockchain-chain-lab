package abci

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strconv"

	"chainlab/internal/core"
	"chainlab/internal/hash"
	"chainlab/internal/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
)

func decodeTransaction(raw []byte, chainID string, blockGasLimit uint64) (types.Transaction, error) {
	if len(raw) == 0 {
		return types.Transaction{}, errors.New("transaction is empty")
	}
	if len(raw) > types.MaxTransactionBytes {
		return types.Transaction{}, fmt.Errorf("transaction exceeds %d bytes", types.MaxTransactionBytes)
	}
	tx, err := types.DecodeRawTransactionForChain("0x"+hex.EncodeToString(raw), chainID)
	if err != nil {
		return types.Transaction{}, err
	}
	canonical, err := hash.CanonicalBytes(tx)
	if err != nil {
		return types.Transaction{}, fmt.Errorf("encode canonical transaction: %w", err)
	}
	if len(canonical) > types.MaxTransactionBytes {
		return types.Transaction{}, fmt.Errorf("transaction exceeds %d canonical bytes", types.MaxTransactionBytes)
	}
	if tx.GasLimit == 0 || tx.GasLimit > blockGasLimit || tx.GasLimit > math.MaxInt64 {
		return types.Transaction{}, fmt.Errorf("transaction gas limit must be between 1 and %d", blockGasLimit)
	}
	return tx, nil
}

func checkTxResult(tx types.Transaction, receipt types.Receipt) (*abcitypes.ResponseCheckTx, error) {
	data, err := hash.CanonicalBytes(receipt)
	if err != nil {
		return nil, err
	}
	return &abcitypes.ResponseCheckTx{
		Code:      CodeOK,
		Data:      data,
		GasWanted: int64(tx.GasLimit),
		GasUsed:   int64(receipt.GasUsed),
	}, nil
}

func executionResult(tx types.Transaction, receipt types.Receipt) (*abcitypes.ExecTxResult, error) {
	data, err := hash.CanonicalBytes(receipt)
	if err != nil {
		return nil, err
	}
	result := &abcitypes.ExecTxResult{
		Data:      data,
		GasWanted: int64(tx.GasLimit),
		GasUsed:   int64(receipt.GasUsed),
		Events:    receiptEvents(receipt),
	}
	if !receipt.Success {
		result.Code = CodeExecutionFailed
		result.Codespace = Codespace
		result.Log = receipt.FailureCode
	}
	return result, nil
}

func receiptEvents(receipt types.Receipt) []abcitypes.Event {
	events := make([]abcitypes.Event, 0, len(receipt.Events))
	for _, event := range receipt.Events {
		keys := make([]string, 0, len(event.Attributes))
		for key := range event.Attributes {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		attributes := make([]abcitypes.EventAttribute, 0, len(keys))
		for _, key := range keys {
			attributes = append(attributes, abcitypes.EventAttribute{
				Key:   key,
				Value: event.Attributes[key],
				Index: true,
			})
		}
		events = append(events, abcitypes.Event{Type: event.Type, Attributes: attributes})
	}
	return events
}

func blockEvent(commitment applicationCommitment) abcitypes.Event {
	return abcitypes.Event{
		Type: "chainlab.block",
		Attributes: []abcitypes.EventAttribute{
			{Key: "base_fee_per_gas", Value: strconv.FormatUint(commitment.BaseFeePerGas, 10), Index: true},
			{Key: "evidence_root", Value: commitment.EvidenceRoot, Index: true},
			{Key: "gas_used", Value: strconv.FormatUint(commitment.GasUsed, 10), Index: true},
			{Key: "next_base_fee_per_gas", Value: strconv.FormatUint(commitment.NextBaseFeePerGas, 10), Index: true},
			{Key: "receipt_root", Value: commitment.ReceiptRoot, Index: true},
			{Key: "state_root", Value: commitment.StateRoot, Index: true},
			{Key: "tx_root", Value: commitment.TxRoot, Index: true},
		},
	}
}

func canonicalBlockHash(raw []byte) (string, error) {
	if len(raw) != 32 {
		return "", errors.New("proposal block hash must be 32 bytes")
	}
	return "0x" + hex.EncodeToString(raw), nil
}

func invalidCheckTx(err error) *abcitypes.ResponseCheckTx {
	return &abcitypes.ResponseCheckTx{
		Code:      CodeInvalidTx,
		Log:       err.Error(),
		Codespace: Codespace,
	}
}

func checkedAdd(left uint64, right uint64) (uint64, error) {
	if math.MaxUint64-left < right {
		return 0, errors.New("uint64 addition overflow")
	}
	return left + right, nil
}

func isFatal(err error) bool {
	return core.IsFatalExecutionError(err)
}

func rawTransactionID(raw []byte) string {
	return hash.KeccakHex(raw)
}

func proposalTxSize(raw []byte) int64 {
	length := uint64(len(raw))
	varintBytes := int64((bits.Len64(length|1) + 6) / 7)
	return 1 + varintBytes + int64(length)
}

func cloneTransactions(txs [][]byte) [][]byte {
	cloned := make([][]byte, len(txs))
	for index, tx := range txs {
		cloned[index] = cloneBytes(tx)
	}
	return cloned
}

func cloneExecTxResults(results []*abcitypes.ExecTxResult) []*abcitypes.ExecTxResult {
	cloned := make([]*abcitypes.ExecTxResult, len(results))
	for index, result := range results {
		if result == nil {
			continue
		}
		copyResult := *result
		copyResult.Data = cloneBytes(result.Data)
		copyResult.Events = cloneABCIEvents(result.Events)
		cloned[index] = &copyResult
	}
	return cloned
}

func cloneABCIEvents(events []abcitypes.Event) []abcitypes.Event {
	cloned := make([]abcitypes.Event, len(events))
	for index, event := range events {
		cloned[index] = event
		cloned[index].Attributes = append([]abcitypes.EventAttribute(nil), event.Attributes...)
	}
	return cloned
}

func proposalError(prefix string, err error) error {
	return fmt.Errorf("%s: %w", prefix, err)
}
