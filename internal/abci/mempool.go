package abci

import (
	"context"
	"errors"
	"fmt"
	"math"

	"chainlab/internal/contracts"
	"chainlab/internal/core"
	"chainlab/internal/state"
	"chainlab/internal/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
)

type mempoolEntry struct {
	raw []byte
	tx  types.Transaction
}

type appMempool struct {
	entries []mempoolEntry
	ids     map[string]struct{}
	bytes   uint64
	working *state.Store
}

func newAppMempool(base *state.Store) appMempool {
	return appMempool{
		ids:     make(map[string]struct{}),
		working: base.Clone(),
	}
}

func (a *Application) InsertTx(_ context.Context, req *abcitypes.RequestInsertTx) (*abcitypes.ResponseInsertTx, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.requireReadyLocked(); err != nil {
		return nil, err
	}
	if req == nil {
		return &abcitypes.ResponseInsertTx{Code: CodeInvalidRequest}, nil
	}
	code, err := a.mempool.insert(
		req.Tx,
		a.genesis.ChainID,
		a.committed.commitment.Height+1,
		a.committed.commitment.NextBaseFeePerGas,
		a.genesis.BlockGasLimit,
		a.runtime,
	)
	if err != nil {
		if isFatal(err) {
			a.haltErr = err
			return nil, err
		}
		return &abcitypes.ResponseInsertTx{Code: CodeInvalidTx}, nil
	}
	return &abcitypes.ResponseInsertTx{Code: code}, nil
}

func (a *Application) ReapTxs(_ context.Context, req *abcitypes.RequestReapTxs) (*abcitypes.ResponseReapTxs, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.requireReadyLocked(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.New("reap transactions request is required")
	}
	return &abcitypes.ResponseReapTxs{Txs: a.mempool.reap(req.MaxBytes, req.MaxGas)}, nil
}

func (pool *appMempool) insert(
	raw []byte,
	chainID string,
	height int64,
	baseFee uint64,
	blockGasLimit uint64,
	runtime *contracts.Runtime,
) (uint32, error) {
	id := rawTransactionID(raw)
	if _, duplicate := pool.ids[id]; duplicate {
		return CodeOK, nil
	}
	if len(pool.entries) >= types.MaxTransactionsPerBlock {
		return CodeMempoolFull, nil
	}
	if uint64(len(raw)) > maxMempoolBytes-pool.bytes {
		return CodeMempoolFull, nil
	}
	tx, err := decodeTransaction(raw, chainID, blockGasLimit)
	if err != nil {
		return CodeInvalidTx, err
	}
	candidate := pool.working.Clone()
	executor := core.NewExecutor(chainID, "", runtime)
	if _, err := executor.ExecuteWithContext(candidate, tx, core.ExecutionContext{
		BlockHeight:   uint64(height),
		BaseFeePerGas: baseFee,
	}); err != nil {
		return CodeInvalidTx, err
	}
	pool.entries = append(pool.entries, mempoolEntry{raw: cloneBytes(raw), tx: tx})
	pool.ids[id] = struct{}{}
	pool.bytes += uint64(len(raw))
	pool.working = candidate
	return CodeOK, nil
}

func (pool appMempool) reap(maxBytes uint64, maxGas uint64) [][]byte {
	if maxBytes == 0 {
		maxBytes = math.MaxUint64
	}
	if maxGas == 0 {
		maxGas = math.MaxUint64
	}
	result := make([][]byte, 0, len(pool.entries))
	var usedBytes uint64
	var usedGas uint64
	for _, entry := range pool.entries {
		txBytes := uint64(proposalTxSize(entry.raw))
		if txBytes > maxBytes-usedBytes || entry.tx.GasLimit > maxGas-usedGas {
			break
		}
		usedBytes += txBytes
		usedGas += entry.tx.GasLimit
		result = append(result, cloneBytes(entry.raw))
	}
	return result
}

func (pool appMempool) rebuild(
	base *state.Store,
	chainID string,
	height int64,
	baseFee uint64,
	blockGasLimit uint64,
	runtime *contracts.Runtime,
	included [][]byte,
) (appMempool, error) {
	excluded := make(map[string]struct{}, len(included))
	for _, raw := range included {
		excluded[rawTransactionID(raw)] = struct{}{}
	}
	rebuilt := newAppMempool(base)
	for _, entry := range pool.entries {
		if _, committed := excluded[rawTransactionID(entry.raw)]; committed {
			continue
		}
		code, err := rebuilt.insert(entry.raw, chainID, height, baseFee, blockGasLimit, runtime)
		if err != nil {
			if isFatal(err) {
				return appMempool{}, err
			}
			continue
		}
		if code != CodeOK {
			return appMempool{}, fmt.Errorf("rebuild mempool returned unexpected code %d", code)
		}
	}
	return rebuilt, nil
}
