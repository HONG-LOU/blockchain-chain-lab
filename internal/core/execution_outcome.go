package core

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"chainlab/internal/contracts"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

var (
	ErrConsensusExecutionFault = errors.New("consensus execution fault")
	ErrGovernanceDisabled      = errors.New("governance transactions require finalized voting-power snapshots and unbonding locks and are disabled")
)

func IsFatalExecutionError(err error) bool {
	return errors.Is(err, ErrConsensusExecutionFault) ||
		errors.Is(err, contracts.ErrWasmRuntimeFault) ||
		errors.Is(err, contracts.ErrContractStateFault) ||
		errors.Is(err, contracts.ErrNativeRuntimeFault)
}

func intrinsicGasForTransaction(tx types.Transaction) (uint64, error) {
	if err := validateTransactionSchema(tx); err != nil {
		return 0, err
	}
	switch tx.Type {
	case types.TxBatch:
		return EstimateGasForBatch(tx.Batch)
	case types.TxWASMUpload:
		bytecode, err := parseWASMUploadPayload(tx.Payload)
		if err != nil {
			return 0, err
		}
		return EstimateWASMUploadGas(bytecode), nil
	default:
		return EstimateGas(tx.Type)
	}
}

func validateTransactionSchema(tx types.Transaction) error {
	if err := requireCanonicalAddress("from", tx.From); err != nil {
		return err
	}
	if tx.Signer != "" {
		if err := requireCanonicalAddress("signer", tx.Signer); err != nil {
			return err
		}
	}
	for index, authorization := range tx.Authorizations {
		if err := requireCanonicalAddress(fmt.Sprintf("authorization %d signer", index), authorization.Signer); err != nil {
			return err
		}
		if tx.Type != types.TxAccountRecovery {
			if err := requireCanonicalSignature(fmt.Sprintf("authorization %d signature", index), authorization.Signature); err != nil {
				return err
			}
		}
	}
	if tx.Type != types.TxAccountRecovery && tx.Signature != "" {
		if err := requireCanonicalSignature("transaction signature", tx.Signature); err != nil {
			return err
		}
	}
	if tx.Type != types.TxAccountRecovery && tx.PaymasterSignature != "" {
		if err := requireCanonicalSignature("paymaster signature", tx.PaymasterSignature); err != nil {
			return err
		}
	}
	if tx.Type != types.TxAccountRecovery && len(tx.Authorizations) > 0 && tx.Signature != "" {
		return errors.New("multisig transaction cannot include transaction signature")
	}
	if tx.SignatureKind != "" && tx.SignatureKind != types.SignatureKindEthereumType2 {
		return fmt.Errorf("unsupported signature kind %q", tx.SignatureKind)
	}
	if tx.Type != types.TxAccountRecovery && tx.SignatureKind == "" && tx.EthereumRawHash != "" {
		return errors.New("ethereum raw hash requires ethereum type2 signature kind")
	}
	if tx.GasPrice != 0 && (tx.MaxFeePerGas != 0 || tx.MaxPriorityFeePerGas != 0) {
		return errors.New("legacy gas price cannot be combined with dynamic fee fields")
	}
	if tx.Type != types.TxAccountRecovery && tx.SignatureKind == types.SignatureKindEthereumType2 {
		if err := validateEthereumType2Schema(tx); err != nil {
			return err
		}
	}
	if tx.Type != types.TxAccountRecovery {
		if strings.TrimSpace(tx.Paymaster) == "" {
			if strings.TrimSpace(tx.PaymasterSignature) != "" {
				return errors.New("paymaster signature requires paymaster")
			}
		} else if err := requireCanonicalAddress("paymaster", tx.Paymaster); err != nil {
			return err
		}
		if len(tx.Batch) > 0 && tx.Type != types.TxBatch {
			return errors.New("batch operations require batch transaction type")
		}
	}

	switch tx.Type {
	case types.TxTransfer:
		if err := requireCanonicalAddress("transfer to", tx.To); err != nil {
			return err
		}
		if len(tx.Payload) != 0 {
			return errors.New("transfer does not support payload fields")
		}
	case types.TxBatch:
		if strings.TrimSpace(tx.To) != "" || tx.Value != 0 || len(tx.Payload) != 0 {
			return errors.New("batch does not support to, value, or payload fields")
		}
		if len(tx.Batch) == 0 {
			return errors.New("batch requires at least one operation")
		}
		if len(tx.Batch) > types.MaxBatchOperations {
			return fmt.Errorf("batch exceeds %d operations", types.MaxBatchOperations)
		}
		for index, operation := range tx.Batch {
			switch operation.Type {
			case types.TxTransfer:
				if err := requireCanonicalAddress(fmt.Sprintf("batch operation %d transfer to", index), operation.To); err != nil {
					return err
				}
				if len(operation.Payload) != 0 {
					return fmt.Errorf("batch operation %d transfer does not support payload", index)
				}
			case types.TxCall:
				if err := validateContractPayloadBounds(operation.Payload); err != nil {
					return fmt.Errorf("batch operation %d: %w", index, err)
				}
				if err := requireCanonicalAddress(fmt.Sprintf("batch operation %d call to", index), operation.To); err != nil {
					return err
				}
				if strings.TrimSpace(operation.Payload["method"]) == "" {
					return fmt.Errorf("batch operation %d call requires method", index)
				}
				if operation.Value != 0 {
					return fmt.Errorf("batch operation %d call does not support value", index)
				}
			default:
				return fmt.Errorf("unsupported batch operation type %q", operation.Type)
			}
		}
	case types.TxDeploy:
		if err := validateContractPayloadBounds(tx.Payload); err != nil {
			return err
		}
		if strings.TrimSpace(tx.To) != "" || tx.Value != 0 {
			return errors.New("deploy does not support to or value fields")
		}
		if err := validateDeployCodeID(tx.Payload["code_id"]); err != nil {
			return err
		}
	case types.TxCall:
		if err := validateContractPayloadBounds(tx.Payload); err != nil {
			return err
		}
		if err := requireCanonicalAddress("call to", tx.To); err != nil {
			return err
		}
		if strings.TrimSpace(tx.Payload["method"]) == "" {
			return errors.New("call requires to and method")
		}
		if tx.Value != 0 {
			return errors.New("call does not support value")
		}
	case types.TxWASMUpload:
		if strings.TrimSpace(tx.To) != "" || tx.Value != 0 {
			return errors.New("wasm upload does not support to or value fields")
		}
		if _, err := parseWASMUploadPayload(tx.Payload); err != nil {
			return err
		}
	case types.TxStake, types.TxUnstake:
		if tx.Value == 0 {
			return fmt.Errorf("%s value must be positive", tx.Type)
		}
		if strings.TrimSpace(tx.To) != "" || len(tx.Payload) != 0 {
			return fmt.Errorf("%s does not support to or payload fields", tx.Type)
		}
	case types.TxSetCode:
		if err := validateSetCodeMessage(tx); err != nil {
			return err
		}
	case types.TxSessionKey:
		if err := validateSessionKeyMessage(tx); err != nil {
			return err
		}
	case types.TxAccountRecovery:
		if err := validateAccountRecoveryMessage(tx); err != nil {
			return err
		}
	case types.TxValidatorSlash:
		evidence, err := parseSlashPayload(tx.Payload)
		if err != nil {
			return err
		}
		if err := validateSlashEvidence(tx.ChainID, evidence); err != nil {
			return err
		}
		if strings.TrimSpace(tx.To) != "" || tx.Value != 0 {
			return errors.New("validator slash does not support to or value fields")
		}
	case types.TxVote, types.TxProposalSubmit, types.TxProposalExecute:
		return ErrGovernanceDisabled
	case types.TxValidatorJoin, types.TxValidatorLeave:
		return errors.New("dynamic validator set transactions require a certified epoch transition and are disabled")
	default:
		if _, err := EstimateGas(tx.Type); err != nil {
			return err
		}
	}
	return nil
}

func validateEthereumType2Schema(tx types.Transaction) error {
	if tx.Type != types.TxTransfer {
		return errors.New("ethereum type2 envelope only supports transfer transactions")
	}
	if tx.GasPrice != 0 || strings.TrimSpace(tx.Signer) != "" || len(tx.Authorizations) != 0 || strings.TrimSpace(tx.Paymaster) != "" || strings.TrimSpace(tx.PaymasterSignature) != "" || len(tx.Payload) != 0 || len(tx.Batch) != 0 {
		return errors.New("ethereum type2 transaction contains unsupported ChainLab fields")
	}
	calculatedHash, err := types.EthereumType2TransactionHash(tx)
	if err != nil {
		return err
	}
	if tx.EthereumRawHash == "" || tx.EthereumRawHash != calculatedHash {
		return errors.New("ethereum raw hash does not match canonical type2 envelope")
	}
	return nil
}

func validateDeployCodeID(codeID string) error {
	if codeID == "" {
		return errors.New("deploy requires code_id")
	}
	if codeID != strings.TrimSpace(codeID) {
		return errors.New("deploy code_id is not canonically encoded")
	}
	switch codeID {
	case contracts.AccountCodeID, contracts.MultisigCodeID, "counter.v1", "token.v1", "wasm.echo.v1":
		return nil
	}
	if len(codeID) != len("wasm:0x")+64 || !strings.HasPrefix(codeID, "wasm:0x") || codeID != strings.ToLower(codeID) {
		return fmt.Errorf("unsupported deploy code_id %q", codeID)
	}
	if _, err := hex.DecodeString(codeID[len("wasm:0x"):]); err != nil {
		return fmt.Errorf("unsupported deploy code_id %q", codeID)
	}
	return nil
}

func validateProposalSubmitSchema(payload map[string]string) error {
	allowed := []string{"title", "description", "kind", "voting_period", "param", "value"}
	if err := requirePayloadFields(payload, "proposal submit", allowed); err != nil {
		return err
	}
	for _, key := range allowed {
		if value, ok := payload[key]; ok && value != strings.TrimSpace(value) {
			return errors.New("proposal submit payload is not canonically encoded")
		}
	}
	if payload["title"] == "" || payload["kind"] == "" || payload["voting_period"] == "" {
		return errors.New("proposal submit requires title, kind, and voting_period")
	}
	if payload["kind"] != "param.change" {
		return fmt.Errorf("unsupported proposal kind %q", payload["kind"])
	}
	if payload["param"] == "" || payload["value"] == "" {
		return errors.New("param.change proposal requires param and value")
	}
	period, err := strconv.ParseUint(payload["voting_period"], 10, 64)
	if err != nil || period == 0 || strconv.FormatUint(period, 10) != payload["voting_period"] {
		return errors.New("proposal voting_period must be a canonical positive integer")
	}
	return nil
}

func requirePayloadFields(payload map[string]string, schema string, allowed []string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = struct{}{}
	}
	for field := range payload {
		if _, ok := allowedSet[field]; !ok {
			return fmt.Errorf("%s payload contains unsupported field %q", schema, field)
		}
	}
	return nil
}

func requireCanonicalSignature(field string, signature string) error {
	return types.ValidateCanonicalSignature(field, signature)
}

func validateSessionKeyMessage(tx types.Transaction) error {
	if tx.To != "" || tx.Value != 0 {
		return errors.New("session key transaction does not support to or value fields")
	}
	action := strings.ToLower(strings.TrimSpace(tx.Payload["action"]))
	key := tx.Payload["key"]
	if key == "" {
		return errors.New("session key address is required")
	}
	if err := requireCanonicalAddress("session key", key); err != nil {
		return err
	}
	allowed := map[string]struct{}{"action": {}, "key": {}}
	switch action {
	case "add":
		for _, field := range []string{"limit", "expires", "to", "call_to", "call_method"} {
			allowed[field] = struct{}{}
		}
		limit := uint64(0)
		if raw := tx.Payload["limit"]; raw != "" {
			value, err := parsePositiveUint(raw, "session key limit")
			if err != nil {
				return err
			}
			limit = value
		}
		if _, err := parseOptionalUint(tx.Payload["expires"], "session key expires"); err != nil {
			return err
		}
		if _, err := optionalCanonicalAddress(tx.Payload["to"], "session key transfer target"); err != nil {
			return err
		}
		callTo, err := optionalCanonicalAddress(tx.Payload["call_to"], "session key call target")
		if err != nil {
			return err
		}
		callMethod := tx.Payload["call_method"]
		if (callTo == "") != (callMethod == "") {
			return errors.New("session key call policy requires call_to and call_method")
		}
		if len(callMethod) > contracts.MaxNativeMethodBytes {
			return errors.New("session key call method exceeds byte limit")
		}
		if limit == 0 && callTo == "" {
			return errors.New("session key requires transfer limit or call policy")
		}
	case "revoke":
	default:
		return errors.New("session key action must be add or revoke")
	}
	for key := range tx.Payload {
		if _, ok := allowed[key]; !ok {
			return errors.New("session key payload contains unsupported fields")
		}
	}
	return nil
}

func validateContractPayloadBounds(payload map[string]string) error {
	if len(payload) > contracts.MaxNativeArgumentFields {
		return fmt.Errorf("contract payload exceeds %d fields", contracts.MaxNativeArgumentFields)
	}
	total := 0
	for key, value := range payload {
		if len(key) > contracts.MaxNativeArgumentKeyBytes || len(value) > contracts.MaxNativeArgumentValueBytes {
			return errors.New("contract payload field exceeds byte limit")
		}
		if total > contracts.MaxNativeInputBytes-len(key) || total+len(key) > contracts.MaxNativeInputBytes-len(value) {
			return errors.New("contract payload exceeds total byte limit")
		}
		total += len(key) + len(value)
	}
	if method := payload["method"]; method != "" && len(method) > contracts.MaxNativeMethodBytes {
		return errors.New("contract method exceeds byte limit")
	}
	return nil
}

func validateSetCodeMessage(tx types.Transaction) error {
	if strings.TrimSpace(tx.To) != "" || tx.Value != 0 {
		return errors.New("set_code does not support to or value fields")
	}
	for key := range tx.Payload {
		if key != "code_id" && key != "owner" {
			return errors.New("set_code payload contains unsupported fields")
		}
	}
	codeID := strings.TrimSpace(tx.Payload["code_id"])
	if codeID == "" {
		if strings.TrimSpace(tx.Payload["owner"]) != "" {
			return errors.New("set_code clear does not support owner")
		}
		return nil
	}
	if codeID != contracts.AccountCodeID {
		return fmt.Errorf("unsupported delegated code id %q", codeID)
	}
	ownerRaw := strings.TrimSpace(tx.Payload["owner"])
	if ownerRaw == "" {
		return errors.New("set_code account.v1 requires owner")
	}
	owner, err := normalizeRecoveryAddress(ownerRaw, "set_code owner")
	if err != nil {
		return err
	}
	if strings.EqualFold(owner, tx.From) {
		return errors.New("set_code owner must differ from delegated account")
	}
	return nil
}

func validateAccountRecoveryMessage(tx types.Transaction) error {
	action, err := accountRecoveryAction(tx.Payload)
	if err != nil {
		return err
	}
	if err := validateAccountRecoveryPayload(action, tx.Payload); err != nil {
		return err
	}
	switch action {
	case "configure":
		guardians, err := parseRecoveryGuardians(tx.Payload["guardians"])
		if err != nil {
			return err
		}
		if _, err := parseRecoveryThreshold(tx.Payload["threshold"], len(guardians)); err != nil {
			return err
		}
		delay, ok := tx.Payload["delay"]
		if !ok || strings.TrimSpace(delay) == "" {
			return errors.New("recovery delay is required")
		}
		if _, err := parseRecoveryDelay(delay); err != nil {
			return err
		}
	case "approve", "execute":
		if _, err := requiredRecoveryNewOwner(tx.Payload["new_owner"]); err != nil {
			return err
		}
	}
	return nil
}

func requireCanonicalAddress(field string, address string) error {
	normalized, err := chaincrypto.NormalizeAddress(address)
	if err != nil {
		return fmt.Errorf("%s %w", field, err)
	}
	if address != normalized {
		return fmt.Errorf("%s address is not canonically encoded", field)
	}
	return nil
}

func (e *Executor) commitFailedExecution(
	store *state.Store,
	ante *state.Store,
	tx types.Transaction,
	context ExecutionContext,
	feePayer string,
	maximumFee uint64,
	gasUsed uint64,
	executionErr error,
) (types.Receipt, error) {
	if errors.Is(executionErr, contracts.ErrContractOutOfGas) || gasUsed > tx.GasLimit {
		gasUsed = tx.GasLimit
	}
	fee, err := CalculateFee(tx, gasUsed, context.BaseFeePerGas)
	if err != nil {
		return types.Receipt{}, fmt.Errorf("%w: failed receipt fee settlement: %v", ErrConsensusExecutionFault, err)
	}
	if err := e.settleFeeEscrow(ante, feePayer, maximumFee, fee); err != nil {
		return types.Receipt{}, err
	}
	receipt := types.Receipt{
		TxHash:            tx.Hash(),
		Success:           false,
		FailureCode:       receiptFailureCode(executionErr),
		GasUsed:           gasUsed,
		BaseFeePerGas:     context.BaseFeePerGas,
		EffectiveGasPrice: fee.EffectiveGasPrice,
		BaseFeeBurned:     fee.BaseFeeBurned,
		PriorityFeePaid:   fee.PriorityFee,
	}
	if strings.TrimSpace(tx.Paymaster) != "" || feePayer != strings.ToLower(strings.TrimSpace(tx.From)) {
		receipt.FeePayer = feePayer
	}
	if err := types.ValidateReceiptSchema(receipt, tx.Hash()); err != nil {
		return types.Receipt{}, fmt.Errorf("%w: invalid generated failed receipt: %v", ErrConsensusExecutionFault, err)
	}
	store.ReplaceWith(ante)
	return receipt, nil
}

func receiptFailureCode(err error) string {
	switch {
	case errors.Is(err, contracts.ErrContractOutOfGas):
		return types.ReceiptFailureOutOfGas
	case errors.Is(err, contracts.ErrWasmGuestTrap):
		return types.ReceiptFailureContractTrap
	case errors.Is(err, contracts.ErrWasmResourceLimit), errors.Is(err, contracts.ErrReadOnlyContract), errors.Is(err, contracts.ErrWasmInvalidUTF8):
		return types.ReceiptFailureResourceLimit
	case errors.Is(err, contracts.ErrNativeResourceLimit), errors.Is(err, state.ErrStorageLimit):
		return types.ReceiptFailureResourceLimit
	default:
		return types.ReceiptFailureExecutionReverted
	}
}
