package abci

import (
	"context"
	"errors"
	"fmt"

	"chainlab/internal/core"
	"chainlab/internal/state"
	"chainlab/internal/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
)

const maxProposalCandidates = types.MaxTransactionsPerBlock * 2

type proposalExecution struct {
	store     *state.Store
	rawTxs    [][]byte
	txs       []types.Transaction
	receipts  []types.Receipt
	txResults []*abcitypes.ExecTxResult
	gasUsed   uint64
}

func (a *Application) CheckTx(_ context.Context, req *abcitypes.RequestCheckTx) (*abcitypes.ResponseCheckTx, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.requireReadyLocked(); err != nil {
		return nil, err
	}
	if req == nil {
		return invalidCheckTx(errors.New("check transaction request is required")), nil
	}
	if req.Type != abcitypes.CheckTxType_New && req.Type != abcitypes.CheckTxType_Recheck {
		return invalidCheckTx(errors.New("check transaction type is unsupported")), nil
	}
	tx, err := decodeTransaction(req.Tx, a.genesis.ChainID, a.genesis.BlockGasLimit)
	if err != nil {
		return invalidCheckTx(err), nil
	}
	working := a.committed.store.Clone()
	executor := core.NewExecutor(a.genesis.ChainID, "", a.runtime)
	receipt, err := executor.ExecuteWithContext(working, tx, core.ExecutionContext{
		BlockHeight:   uint64(a.committed.commitment.Height + 1),
		BaseFeePerGas: a.committed.commitment.NextBaseFeePerGas,
	})
	if err != nil {
		if isFatal(err) {
			a.haltErr = err
			return nil, err
		}
		return invalidCheckTx(err), nil
	}
	return checkTxResult(tx, receipt)
}

func (a *Application) PrepareProposal(_ context.Context, req *abcitypes.RequestPrepareProposal) (*abcitypes.ResponsePrepareProposal, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.requireProposalRequestLocked(req); err != nil {
		return nil, err
	}
	proposer, err := a.proposerLocked(req.ProposerAddress)
	if err != nil {
		return nil, err
	}
	if _, err := validateEvidence(req.Misbehavior, req.Height, a.proposers); err != nil {
		return nil, err
	}
	maxBytes := req.MaxTxBytes
	if maxBytes <= 0 {
		return nil, errors.New("prepare proposal max transaction bytes must be positive")
	}
	if maxBytes > types.MaxProposalTxBytes {
		maxBytes = types.MaxProposalTxBytes
	}
	execution, err := a.executeProposalLocked(req.Txs, req.Height, proposer, maxBytes, false)
	if err != nil {
		return nil, err
	}
	return &abcitypes.ResponsePrepareProposal{Txs: cloneTransactions(execution.rawTxs)}, nil
}

func (a *Application) ProcessProposal(_ context.Context, req *abcitypes.RequestProcessProposal) (*abcitypes.ResponseProcessProposal, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.requireReadyLocked(); err != nil {
		return nil, err
	}
	reject := &abcitypes.ResponseProcessProposal{Status: abcitypes.ResponseProcessProposal_REJECT}
	if req == nil || req.Height != a.committed.commitment.Height+1 || a.candidate != nil {
		return reject, nil
	}
	if _, err := canonicalBlockHash(req.Hash); err != nil {
		return reject, nil
	}
	proposer, err := a.proposerLocked(req.ProposerAddress)
	if err != nil {
		return reject, nil
	}
	if _, err := validateEvidence(req.Misbehavior, req.Height, a.proposers); err != nil {
		return reject, nil
	}
	if _, err := a.executeProposalLocked(req.Txs, req.Height, proposer, types.MaxProposalTxBytes, true); err != nil {
		if isFatal(err) {
			a.haltErr = err
			return nil, err
		}
		return reject, nil
	}
	return &abcitypes.ResponseProcessProposal{Status: abcitypes.ResponseProcessProposal_ACCEPT}, nil
}

func (a *Application) FinalizeBlock(_ context.Context, req *abcitypes.RequestFinalizeBlock) (*abcitypes.ResponseFinalizeBlock, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.requireReadyLocked(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.New("finalize block request is required")
	}
	if a.candidate != nil {
		return nil, errors.New("a finalized block candidate is already pending commit")
	}
	if req.Height != a.committed.commitment.Height+1 {
		return nil, errors.New("finalize block height is out of sequence")
	}
	blockHash, err := canonicalBlockHash(req.Hash)
	if err != nil {
		return nil, err
	}
	proposer, err := a.proposerLocked(req.ProposerAddress)
	if err != nil {
		return nil, err
	}
	evidence, err := validateEvidence(req.Misbehavior, req.Height, a.proposers)
	if err != nil {
		return nil, err
	}
	execution, err := a.executeProposalLocked(req.Txs, req.Height, proposer, types.MaxProposalTxBytes, true)
	if err != nil {
		if isFatal(err) {
			a.haltErr = err
		}
		return nil, proposalError("decided block is invalid", err)
	}
	commitment := applicationCommitment{
		Protocol:          ProtocolVersion,
		ChainID:           a.genesis.ChainID,
		Height:            req.Height,
		BlockHash:         blockHash,
		Proposer:          proposer,
		GasLimit:          a.genesis.BlockGasLimit,
		GasUsed:           execution.gasUsed,
		BaseFeePerGas:     a.committed.commitment.NextBaseFeePerGas,
		NextBaseFeePerGas: core.NextBaseFee(a.committed.commitment.NextBaseFeePerGas, execution.gasUsed, a.genesis.BlockGasLimit),
		TxRoot:            types.TransactionRoot(execution.txs),
		ReceiptRoot:       types.ReceiptRoot(execution.receipts),
		EvidenceRoot:      evidenceRoot(evidence),
		StateRoot:         execution.store.Root(),
	}
	blockEvents := []abcitypes.Event{blockEvent(commitment)}
	blockEvents = append(blockEvents, evidenceEvents(evidence)...)
	candidate := &blockCandidate{
		state: committedState{
			store:      execution.store,
			commitment: commitment,
			appHash:    applicationHash(commitment),
		},
		txs: cloneTransactions(execution.rawTxs),
	}
	a.candidate = candidate
	return &abcitypes.ResponseFinalizeBlock{
		Events:    cloneABCIEvents(blockEvents),
		TxResults: cloneExecTxResults(execution.txResults),
		AppHash:   cloneBytes(candidate.state.appHash),
	}, nil
}

func (a *Application) requireProposalRequestLocked(req *abcitypes.RequestPrepareProposal) error {
	if err := a.requireReadyLocked(); err != nil {
		return err
	}
	if req == nil {
		return errors.New("prepare proposal request is required")
	}
	if a.candidate != nil {
		return errors.New("cannot prepare a proposal while a finalized candidate is pending")
	}
	if req.Height != a.committed.commitment.Height+1 {
		return errors.New("prepare proposal height is out of sequence")
	}
	return nil
}

func (a *Application) executeProposalLocked(rawTxs [][]byte, height int64, proposer string, maxBytes int64, strict bool) (proposalExecution, error) {
	if strict && len(rawTxs) > types.MaxTransactionsPerBlock {
		return proposalExecution{}, fmt.Errorf("proposal exceeds %d transactions", types.MaxTransactionsPerBlock)
	}
	working := a.committed.store.Clone()
	executor := core.NewExecutor(a.genesis.ChainID, proposer, a.runtime)
	result := proposalExecution{
		store:     working,
		rawTxs:    make([][]byte, 0, min(len(rawTxs), types.MaxTransactionsPerBlock)),
		txs:       make([]types.Transaction, 0, min(len(rawTxs), types.MaxTransactionsPerBlock)),
		receipts:  make([]types.Receipt, 0, min(len(rawTxs), types.MaxTransactionsPerBlock)),
		txResults: make([]*abcitypes.ExecTxResult, 0, min(len(rawTxs), types.MaxTransactionsPerBlock)),
	}
	var totalBytes int64
	for index, raw := range rawTxs {
		if !strict && index >= maxProposalCandidates {
			break
		}
		if len(result.txs) >= types.MaxTransactionsPerBlock {
			break
		}
		txBytes := proposalTxSize(raw)
		if txBytes < 0 || totalBytes > maxBytes-txBytes {
			if strict {
				return proposalExecution{}, errors.New("proposal transaction bytes exceed the protocol limit")
			}
			break
		}
		tx, err := decodeTransaction(raw, a.genesis.ChainID, a.genesis.BlockGasLimit)
		if err != nil {
			if strict {
				return proposalExecution{}, fmt.Errorf("transaction %d: %w", index, err)
			}
			continue
		}
		candidate := working.Clone()
		receipt, err := executor.ExecuteWithContext(candidate, tx, core.ExecutionContext{
			BlockHeight:   uint64(height),
			BaseFeePerGas: a.committed.commitment.NextBaseFeePerGas,
		})
		if err != nil {
			if isFatal(err) {
				a.haltErr = err
				return proposalExecution{}, err
			}
			if strict {
				return proposalExecution{}, fmt.Errorf("transaction %d: %w", index, err)
			}
			continue
		}
		nextGas, err := checkedAdd(result.gasUsed, receipt.GasUsed)
		if err != nil || nextGas > a.genesis.BlockGasLimit {
			if strict {
				return proposalExecution{}, fmt.Errorf("transaction %d exceeds block gas limit", index)
			}
			continue
		}
		txResult, err := executionResult(tx, receipt)
		if err != nil {
			return proposalExecution{}, err
		}
		working = candidate
		result.store = working
		result.gasUsed = nextGas
		totalBytes += txBytes
		result.rawTxs = append(result.rawTxs, cloneBytes(raw))
		result.txs = append(result.txs, tx)
		result.receipts = append(result.receipts, receipt)
		result.txResults = append(result.txResults, txResult)
	}
	return result, nil
}

var _ abcitypes.Application = (*Application)(nil)
