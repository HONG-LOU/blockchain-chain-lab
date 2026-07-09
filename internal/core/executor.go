package core

import (
	"errors"
	"fmt"
	"math"

	"chainlab/internal/contracts"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

type Executor struct {
	chainID      string
	feeCollector string
	runtime      *contracts.Runtime
}

func NewExecutor(chainID string, feeCollector string, runtime *contracts.Runtime) *Executor {
	if runtime == nil {
		runtime = contracts.NewRuntimeWithDefaults()
	}
	return &Executor{
		chainID:      chainID,
		feeCollector: feeCollector,
		runtime:      runtime,
	}
}

func (e *Executor) Execute(store *state.Store, tx types.Transaction) (types.Receipt, error) {
	if tx.ChainID != e.chainID {
		return types.Receipt{}, fmt.Errorf("wrong chain id %q", tx.ChainID)
	}
	if !chaincrypto.Verify(tx.From, tx.SigningBytes(), tx.Signature) {
		return types.Receipt{}, errors.New("invalid transaction signature")
	}
	account := store.GetAccount(tx.From)
	if account.Nonce != tx.Nonce {
		return types.Receipt{}, fmt.Errorf("bad nonce: got %d want %d", tx.Nonce, account.Nonce)
	}

	gasUsed, err := EstimateGas(tx.Type)
	if err != nil {
		return types.Receipt{}, err
	}
	if tx.GasLimit < gasUsed {
		return types.Receipt{}, errors.New("gas limit too low")
	}
	fee, err := checkedMul(gasUsed, tx.GasPrice)
	if err != nil {
		return types.Receipt{}, err
	}

	working := store.Clone()
	if err := e.chargeFee(working, tx.From, fee); err != nil {
		return types.Receipt{}, err
	}
	working.IncrementNonce(tx.From)

	receipt := types.Receipt{
		TxHash:  tx.Hash(),
		Success: true,
		GasUsed: gasUsed,
	}

	switch tx.Type {
	case types.TxTransfer:
		if err := working.Transfer(tx.From, tx.To, tx.Value); err != nil {
			return types.Receipt{}, err
		}
		receipt.Events = append(receipt.Events, types.Event{Type: "transfer", Attributes: map[string]string{"from": tx.From, "to": tx.To}})
	case types.TxStake:
		if tx.Value == 0 {
			return types.Receipt{}, errors.New("stake value must be positive")
		}
		if err := working.SubBalance(tx.From, tx.Value); err != nil {
			return types.Receipt{}, err
		}
		if err := working.AddStake(tx.From, tx.Value); err != nil {
			return types.Receipt{}, err
		}
		receipt.Events = append(receipt.Events, types.Event{Type: "stake"})
	case types.TxUnstake:
		if tx.Value == 0 {
			return types.Receipt{}, errors.New("unstake value must be positive")
		}
		if err := working.SubStake(tx.From, tx.Value); err != nil {
			return types.Receipt{}, err
		}
		if err := working.AddBalance(tx.From, tx.Value); err != nil {
			return types.Receipt{}, err
		}
		receipt.Events = append(receipt.Events, types.Event{Type: "unstake"})
	case types.TxVote:
		proposal := tx.Payload["proposal"]
		choice := tx.Payload["choice"]
		if proposal == "" || choice == "" {
			return types.Receipt{}, errors.New("vote requires proposal and choice")
		}
		power := working.StakeOf(tx.From)
		if power == 0 {
			return types.Receipt{}, errors.New("voter has no stake")
		}
		working.RecordVote(proposal, tx.From, choice, power)
		receipt.Events = append(receipt.Events, types.Event{Type: "governance.vote", Attributes: map[string]string{"proposal": proposal, "choice": choice}})
	case types.TxValidatorJoin:
		if working.StakeOf(tx.From) == 0 {
			return types.Receipt{}, errors.New("validator join requires stake")
		}
		if err := working.AddValidator(tx.From); err != nil {
			return types.Receipt{}, err
		}
		receipt.Events = append(receipt.Events, types.Event{Type: "validator.joined", Attributes: map[string]string{"validator": tx.From}})
	case types.TxDeploy:
		codeID := tx.Payload["code_id"]
		if codeID == "" {
			return types.Receipt{}, errors.New("deploy requires code_id")
		}
		address, events, err := e.runtime.Deploy(working, tx.From, codeID, tx.Hash(), tx.Payload)
		if err != nil {
			return types.Receipt{}, err
		}
		receipt.ContractAddress = address
		receipt.Events = append(receipt.Events, events...)
	case types.TxCall:
		method := tx.Payload["method"]
		if tx.To == "" || method == "" {
			return types.Receipt{}, errors.New("call requires to and method")
		}
		events, err := e.runtime.Call(working, tx.To, tx.From, method, tx.Payload)
		if err != nil {
			return types.Receipt{}, err
		}
		receipt.Events = append(receipt.Events, events...)
	default:
		return types.Receipt{}, fmt.Errorf("unsupported transaction type %q", tx.Type)
	}

	store.ReplaceWith(working)
	return receipt, nil
}

func (e *Executor) chargeFee(store *state.Store, from string, fee uint64) error {
	if fee == 0 {
		return nil
	}
	if err := store.SubBalance(from, fee); err != nil {
		return err
	}
	if e.feeCollector == "" {
		return nil
	}
	return store.AddBalance(e.feeCollector, fee)
}

func EstimateGas(txType types.TxType) (uint64, error) {
	switch txType {
	case types.TxTransfer:
		return 21_000, nil
	case types.TxStake, types.TxUnstake:
		return 30_000, nil
	case types.TxVote:
		return 25_000, nil
	case types.TxValidatorJoin:
		return 40_000, nil
	case types.TxDeploy:
		return 80_000, nil
	case types.TxCall:
		return 50_000, nil
	default:
		return 0, fmt.Errorf("unsupported transaction type %q", txType)
	}
}

func checkedMul(left uint64, right uint64) (uint64, error) {
	if left != 0 && right > math.MaxUint64/left {
		return 0, errors.New("fee overflow")
	}
	return left * right, nil
}
