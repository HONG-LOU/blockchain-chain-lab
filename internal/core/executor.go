package core

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

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

	baseGas, err := EstimateGas(tx.Type)
	if err != nil {
		return types.Receipt{}, err
	}

	working := store.Clone()
	working.IncrementNonce(tx.From)

	receipt := types.Receipt{
		TxHash:  tx.Hash(),
		Success: true,
	}
	gasUsed := baseGas

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
	case types.TxValidatorLeave:
		if err := working.RemoveValidator(tx.From); err != nil {
			return types.Receipt{}, err
		}
		receipt.Events = append(receipt.Events, types.Event{Type: "validator.left", Attributes: map[string]string{"validator": tx.From}})
	case types.TxValidatorSlash:
		target, slashAmount, evidence, err := parseSlashPayload(tx.Payload)
		if err != nil {
			return types.Receipt{}, err
		}
		if !isActiveValidator(working, tx.From) {
			return types.Receipt{}, errors.New("slash reporter must be an active validator")
		}
		if !isActiveValidator(working, target) {
			return types.Receipt{}, errors.New("slash target must be an active validator")
		}
		currentStake := working.StakeOf(target)
		if currentStake == 0 {
			return types.Receipt{}, errors.New("slash target has no stake")
		}
		if slashAmount > currentStake {
			slashAmount = currentStake
		}
		if err := working.SubStake(target, slashAmount); err != nil {
			return types.Receipt{}, err
		}
		removed := "false"
		if working.StakeOf(target) == 0 {
			if err := working.RemoveValidator(target); err != nil {
				return types.Receipt{}, err
			}
			removed = "true"
		}
		receipt.Events = append(receipt.Events, types.Event{Type: "validator.slashed", Attributes: map[string]string{
			"reporter": tx.From,
			"target":   strings.ToLower(target),
			"amount":   strconv.FormatUint(slashAmount, 10),
			"evidence": evidence,
			"removed":  removed,
		}})
	case types.TxWASMUpload:
		bytecode, err := parseWASMUploadPayload(tx.Payload)
		if err != nil {
			return types.Receipt{}, err
		}
		gasUsed = EstimateWASMUploadGas(bytecode)
		if err := contracts.ValidateWasmCode(bytecode); err != nil {
			return types.Receipt{}, err
		}
		codeID := types.WASMCodeID(bytecode)
		working.SetContractCode(types.ContractCode{
			CodeID:   codeID,
			Runtime:  "wasm",
			Creator:  strings.ToLower(tx.From),
			Bytecode: "0x" + hex.EncodeToString(bytecode),
		})
		receipt.CodeID = codeID
		receipt.Events = append(receipt.Events, types.Event{Type: "wasm.code_uploaded", Attributes: map[string]string{
			"code_id": codeID,
			"creator": strings.ToLower(tx.From),
			"size":    strconv.Itoa(len(bytecode)),
		}})
	case types.TxDeploy:
		codeID := tx.Payload["code_id"]
		if codeID == "" {
			return types.Receipt{}, errors.New("deploy requires code_id")
		}
		address, events, resourceGas, err := e.runtime.DeployMetered(working, tx.From, codeID, tx.Hash(), tx.Payload)
		if err != nil {
			return types.Receipt{}, err
		}
		gasUsed, err = checkedAdd(gasUsed, resourceGas)
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
		events, resourceGas, err := e.runtime.CallMetered(working, tx.To, tx.From, method, tx.Payload)
		if err != nil {
			return types.Receipt{}, err
		}
		gasUsed, err = checkedAdd(gasUsed, resourceGas)
		if err != nil {
			return types.Receipt{}, err
		}
		receipt.Events = append(receipt.Events, events...)
	default:
		return types.Receipt{}, fmt.Errorf("unsupported transaction type %q", tx.Type)
	}

	if tx.GasLimit < gasUsed {
		return types.Receipt{}, errors.New("gas limit too low")
	}
	fee, err := checkedMul(gasUsed, tx.GasPrice)
	if err != nil {
		return types.Receipt{}, err
	}
	if err := e.chargeFee(working, tx.From, fee); err != nil {
		return types.Receipt{}, err
	}
	receipt.GasUsed = gasUsed
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
	case types.TxValidatorJoin, types.TxValidatorLeave:
		return 40_000, nil
	case types.TxValidatorSlash:
		return 45_000, nil
	case types.TxWASMUpload:
		return 120_000, nil
	case types.TxDeploy:
		return 80_000, nil
	case types.TxCall:
		return 50_000, nil
	default:
		return 0, fmt.Errorf("unsupported transaction type %q", txType)
	}
}

func EstimateGasForPayload(txType types.TxType, payload map[string]string) (uint64, error) {
	if txType == types.TxWASMUpload {
		bytecode, err := parseWASMUploadPayload(payload)
		if err != nil {
			return 0, err
		}
		return EstimateWASMUploadGas(bytecode), nil
	}
	return EstimateGas(txType)
}

func EstimateWASMUploadGas(bytecode []byte) uint64 {
	base, _ := EstimateGas(types.TxWASMUpload)
	size := uint64(len(bytecode))
	if size > (math.MaxUint64-base)/4 {
		return math.MaxUint64
	}
	return base + size*4
}

func checkedAdd(left uint64, right uint64) (uint64, error) {
	if math.MaxUint64-left < right {
		return 0, errors.New("gas overflow")
	}
	return left + right, nil
}

func checkedMul(left uint64, right uint64) (uint64, error) {
	if left != 0 && right > math.MaxUint64/left {
		return 0, errors.New("fee overflow")
	}
	return left * right, nil
}

func parseWASMUploadPayload(payload map[string]string) ([]byte, error) {
	raw := strings.TrimSpace(payload["bytecode"])
	if raw == "" {
		raw = strings.TrimSpace(payload["wasm"])
	}
	if raw == "" {
		return nil, errors.New("wasm upload requires bytecode")
	}
	bytecode, err := hex.DecodeString(strings.TrimPrefix(raw, "0x"))
	if err != nil {
		return nil, fmt.Errorf("invalid wasm bytecode hex: %w", err)
	}
	if len(bytecode) == 0 {
		return nil, errors.New("wasm bytecode is required")
	}
	return bytecode, nil
}

func parseSlashPayload(payload map[string]string) (string, uint64, string, error) {
	target := strings.ToLower(strings.TrimSpace(payload["target"]))
	if target == "" {
		return "", 0, "", errors.New("slash target is required")
	}
	amountRaw := payload["amount"]
	if amountRaw == "" {
		return "", 0, "", errors.New("slash amount is required")
	}
	amount, err := strconv.ParseUint(amountRaw, 10, 64)
	if err != nil || amount == 0 {
		return "", 0, "", errors.New("slash amount must be positive")
	}
	evidence := strings.TrimSpace(payload["evidence"])
	if evidence == "" {
		return "", 0, "", errors.New("slash evidence is required")
	}
	return target, amount, evidence, nil
}

func isActiveValidator(store *state.Store, address string) bool {
	address = strings.ToLower(strings.TrimSpace(address))
	for _, validator := range store.Validators() {
		if validator == address {
			return true
		}
	}
	return false
}
