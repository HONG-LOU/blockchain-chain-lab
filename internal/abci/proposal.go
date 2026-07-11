package abci

import (
	"context"
	"errors"
	"fmt"
	"time"

	"chainlab/internal/core"
	"chainlab/internal/state"
	"chainlab/internal/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
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

type proposalEvidenceState struct {
	store    *state.Store
	records  []evidenceRecord
	outcomes []evidenceOutcome
	updates  []abcitypes.ValidatorUpdate
}

func (a *Application) CheckTx(_ context.Context, req *abcitypes.RequestCheckTx) (*abcitypes.ResponseCheckTx, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.requireReadyLocked(); err != nil {
		return nil, err
	}
	if err := a.requireProtocolSupportLocked(a.committed.commitment.Height+1, true); err != nil {
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
	proposer, err := a.proposerLocked(req.ProposerAddress, req.Height)
	if err != nil {
		return nil, err
	}
	evidenceState, err := a.proposalEvidenceStateLocked(req.Misbehavior, req.Height, req.Time)
	if err != nil {
		return nil, err
	}
	maxBytes := req.MaxTxBytes
	if maxBytes <= 0 {
		return nil, errors.New("prepare proposal max transaction bytes must be positive")
	}
	if maxBytes > types.MaxProposalTxBytes {
		maxBytes = types.MaxProposalTxBytes
	}
	execution, err := a.executeProposalFromStoreLocked(evidenceState.store, req.Txs, req.Height, proposer, maxBytes, false)
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
	if err := a.requireProtocolSupportLocked(req.Height, true); err != nil {
		return reject, nil
	}
	if _, err := canonicalBlockHash(req.Hash); err != nil {
		return reject, nil
	}
	proposer, err := a.proposerLocked(req.ProposerAddress, req.Height)
	if err != nil {
		return reject, nil
	}
	evidenceState, err := a.proposalEvidenceStateLocked(req.Misbehavior, req.Height, req.Time)
	if err != nil {
		return reject, nil
	}
	if _, err := a.executeProposalFromStoreLocked(evidenceState.store, req.Txs, req.Height, proposer, types.MaxProposalTxBytes, true); err != nil {
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
	if err := a.requireProtocolSupportLocked(req.Height, true); err != nil {
		return nil, err
	}
	consensusParamUpdates, err := a.consensusParamUpdatesLocked(req.Height)
	if err != nil {
		return nil, err
	}
	blockHash, err := canonicalBlockHash(req.Hash)
	if err != nil {
		return nil, err
	}
	proposer, err := a.proposerLocked(req.ProposerAddress, req.Height)
	if err != nil {
		return nil, err
	}
	evidenceState, err := a.proposalEvidenceStateLocked(req.Misbehavior, req.Height, req.Time)
	if err != nil {
		return nil, err
	}
	execution, err := a.executeProposalFromStoreLocked(evidenceState.store, req.Txs, req.Height, proposer, types.MaxProposalTxBytes, true)
	if err != nil {
		if isFatal(err) {
			a.haltErr = err
		}
		return nil, proposalError("decided block is invalid", err)
	}
	protocol := protocolAtHeight(a.genesis, req.Height)
	txRoot, err := transactionRootForProtocol(protocol, execution.txs)
	if err != nil {
		return nil, err
	}
	receiptRoot, err := receiptRootForProtocol(protocol, execution.receipts)
	if err != nil {
		return nil, err
	}
	flatTree := a.committed.flatTree.Clone()
	if err := applyStoreMutationsToSparseTree(flatTree, execution.store); err != nil {
		return nil, fmt.Errorf("update sparse state tree: %w", err)
	}
	candidateState := committedState{store: execution.store, flatTree: flatTree}
	stateRoot, err := stateRootForCommitted(protocol, candidateState)
	if err != nil {
		return nil, err
	}
	commitment := applicationCommitment{
		Protocol:          protocol,
		ChainID:           a.genesis.ChainID,
		Height:            req.Height,
		BlockHash:         blockHash,
		Proposer:          proposer,
		GasLimit:          a.genesis.BlockGasLimit,
		GasUsed:           execution.gasUsed,
		BaseFeePerGas:     a.committed.commitment.NextBaseFeePerGas,
		NextBaseFeePerGas: core.NextBaseFee(a.committed.commitment.NextBaseFeePerGas, execution.gasUsed, a.genesis.BlockGasLimit),
		TxRoot:            txRoot,
		ReceiptRoot:       receiptRoot,
		EvidenceRoot:      evidenceRoot(evidenceState.records),
		StateRoot:         stateRoot,
	}
	if a.genesis.Protocol == ProtocolVersionV2 {
		commitment.Epoch = validatorEpoch(req.Height, a.genesis.ValidatorPolicy.EpochLength)
		commitment.ValidatorRoot = execution.store.ValidatorRoot()
	}
	blockEvents := []abcitypes.Event{blockEvent(commitment)}
	blockEvents = append(blockEvents, evidenceEvents(evidenceState.records)...)
	blockEvents = append(blockEvents, evidenceOutcomeEvents(evidenceState.outcomes)...)
	candidate := &blockCandidate{
		state: committedState{
			store:      execution.store,
			flatTree:   flatTree,
			commitment: commitment,
			appHash:    applicationHash(commitment),
		},
		txs:      cloneTransactions(execution.rawTxs),
		receipts: cloneReceipts(execution.receipts),
	}
	a.candidate = candidate
	return &abcitypes.ResponseFinalizeBlock{
		Events:                cloneABCIEvents(blockEvents),
		TxResults:             cloneExecTxResults(execution.txResults),
		ValidatorUpdates:      cloneValidatorUpdates(evidenceState.updates),
		ConsensusParamUpdates: consensusParamUpdates,
		AppHash:               cloneBytes(candidate.state.appHash),
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
	if err := a.requireProtocolSupportLocked(req.Height, true); err != nil {
		return err
	}
	return nil
}

func (a *Application) proposalEvidenceStateLocked(
	input []abcitypes.Misbehavior,
	height int64,
	blockTime time.Time,
) (proposalEvidenceState, error) {
	if a.genesis.Protocol == ProtocolVersion {
		records, err := validateEvidence(input, height, a.proposers)
		return proposalEvidenceState{store: a.committed.store, records: records}, err
	}
	lifecycle, exists := a.committed.store.ValidatorLifecycle()
	if !exists {
		return proposalEvidenceState{}, errors.New("protocol version 2 validator lifecycle is missing")
	}
	records, err := validateEvidenceV2(input, height, blockTime, lifecycle, *a.genesis.ValidatorPolicy)
	if err != nil {
		return proposalEvidenceState{}, err
	}
	working := a.committed.store.Clone()
	outcomes, updates, err := applyEvidenceV2(working, records, height, *a.genesis.ValidatorPolicy)
	if err != nil {
		return proposalEvidenceState{}, err
	}
	return proposalEvidenceState{store: working, records: records, outcomes: outcomes, updates: updates}, nil
}

func (a *Application) executeProposalFromStoreLocked(
	base *state.Store,
	rawTxs [][]byte,
	height int64,
	proposer string,
	maxBytes int64,
	strict bool,
) (proposalExecution, error) {
	if strict && len(rawTxs) > types.MaxTransactionsPerBlock {
		return proposalExecution{}, fmt.Errorf("proposal exceeds %d transactions", types.MaxTransactionsPerBlock)
	}
	working := base.Clone()
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

func cloneValidatorUpdates(updates []abcitypes.ValidatorUpdate) []abcitypes.ValidatorUpdate {
	cloned := make([]abcitypes.ValidatorUpdate, len(updates))
	for index, update := range updates {
		cloned[index] = abcitypes.UpdateValidator(cloneBytes(update.PubKey.GetSecp256K1()), update.Power, cmtsecp256k1.KeyType)
	}
	return cloned
}

var _ abcitypes.Application = (*Application)(nil)
