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

type ExecutionContext struct {
	BlockHeight   uint64
	BaseFeePerGas uint64
}

const (
	sessionEpochKey = "session:epoch"
	maxSessionKeys  = 16
)

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
	return e.ExecuteAtHeight(store, tx, 0)
}

func (e *Executor) ExecuteAtHeight(store *state.Store, tx types.Transaction, blockHeight uint64) (types.Receipt, error) {
	return e.ExecuteWithContext(store, tx, ExecutionContext{BlockHeight: blockHeight})
}

func (e *Executor) ExecuteWithContext(store *state.Store, tx types.Transaction, context ExecutionContext) (receipt types.Receipt, err error) {
	if tx.ChainID != e.chainID {
		return types.Receipt{}, fmt.Errorf("wrong chain id %q", tx.ChainID)
	}
	intrinsicGas, err := intrinsicGasForTransaction(tx)
	if err != nil {
		return types.Receipt{}, err
	}
	if tx.GasLimit < intrinsicGas {
		return types.Receipt{}, errors.New("gas limit too low")
	}
	if err := validateTransactionAuthorization(store, tx, context.BlockHeight); err != nil {
		return types.Receipt{}, err
	}
	if err := validatePaymasterAuthorization(tx); err != nil {
		return types.Receipt{}, err
	}
	account := store.GetAccount(tx.From)
	if account.Nonce != tx.Nonce {
		return types.Receipt{}, fmt.Errorf("bad nonce: got %d want %d", tx.Nonce, account.Nonce)
	}

	if err := e.validateFeeCapacity(store, tx, context.BaseFeePerGas); err != nil {
		return types.Receipt{}, err
	}

	ante := store.Clone()
	if err := ante.IncrementNonce(tx.From); err != nil {
		return types.Receipt{}, err
	}
	feePayer := transactionFeePayer(tx)
	maximumFee, err := maximumTransactionFee(tx)
	if err != nil {
		return types.Receipt{}, fmt.Errorf("%w: fee escrow calculation: %v", ErrConsensusExecutionFault, err)
	}
	if err := ante.SubBalance(feePayer, maximumFee); err != nil {
		return types.Receipt{}, fmt.Errorf("%w: fee escrow reservation: %v", ErrConsensusExecutionFault, err)
	}
	working := ante.Clone()
	executionStarted := true
	gasUsed := intrinsicGas
	defer func() {
		if err == nil || !executionStarted {
			return
		}
		if IsFatalExecutionError(err) {
			return
		}
		receipt, err = e.commitFailedExecution(store, ante, tx, context, feePayer, maximumFee, gasUsed, err)
	}()

	receipt = types.Receipt{
		TxHash:  tx.Hash(),
		Success: true,
	}

	switch tx.Type {
	case types.TxTransfer:
		if err := working.Transfer(tx.From, tx.To, tx.Value); err != nil {
			return types.Receipt{}, err
		}
		receipt.Events = append(receipt.Events, types.Event{Type: "transfer", Attributes: map[string]string{"from": tx.From, "to": tx.To}})
		sessionEvent, err := applySessionKeyUsage(working, tx)
		if err != nil {
			return types.Receipt{}, err
		}
		if sessionEvent.Type != "" {
			receipt.Events = append(receipt.Events, sessionEvent)
		}
	case types.TxBatch:
		receipt.Events = append(receipt.Events, types.Event{Type: "batch.executed", Attributes: map[string]string{
			"operations": strconv.Itoa(len(tx.Batch)),
		}})
		for index, operation := range tx.Batch {
			remainingGas := tx.GasLimit - gasUsed
			events, resourceGas, operationErr := e.executeBatchOperation(working, tx, operation, index, remainingGas)
			gasUsed, err = checkedAdd(gasUsed, resourceGas)
			if err != nil {
				return types.Receipt{}, fmt.Errorf("%w: batch gas accounting: %v", ErrConsensusExecutionFault, err)
			}
			if operationErr != nil {
				return types.Receipt{}, operationErr
			}
			receipt.Events = append(receipt.Events, events...)
		}
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
		if _, validator := working.ValidatorIdentityByAccount(tx.From); validator {
			return types.Receipt{}, errors.New("validator stake is locked until an unbonding protocol is activated")
		}
		if err := working.SubStake(tx.From, tx.Value); err != nil {
			return types.Receipt{}, err
		}
		if err := working.AddBalance(tx.From, tx.Value); err != nil {
			return types.Receipt{}, err
		}
		receipt.Events = append(receipt.Events, types.Event{Type: "unstake"})
	case types.TxProposalSubmit, types.TxVote, types.TxProposalExecute:
		return types.Receipt{}, ErrGovernanceDisabled
	case types.TxValidatorJoin:
		return types.Receipt{}, errors.New("dynamic validator joins require a certified epoch transition and are disabled")
	case types.TxValidatorLeave:
		return types.Receipt{}, errors.New("dynamic validator leaves require a certified epoch transition and are disabled")
	case types.TxValidatorSlash:
		evidence, err := parseSlashPayload(tx.Payload)
		if err != nil {
			return types.Receipt{}, err
		}
		if err := validateSlashEvidence(tx.ChainID, evidence); err != nil {
			return types.Receipt{}, err
		}
		if !isActiveValidator(working, tx.From) {
			return types.Receipt{}, errors.New("slash reporter must be an active validator")
		}
		if !isActiveValidator(working, evidence.Target) {
			return types.Receipt{}, errors.New("slash target must be an active validator")
		}
		evidenceID := slashEvidenceID(tx.ChainID, evidence)
		offenceID := slashOffenceID(tx.ChainID, evidence)
		consumedKey := "slash:offence:" + offenceID
		if working.Param(consumedKey) != "" {
			return types.Receipt{}, errors.New("slash evidence has already been consumed")
		}
		currentStake := working.StakeOf(evidence.Target)
		if currentStake == 0 {
			return types.Receipt{}, errors.New("slash target has no stake")
		}
		if err := working.SubStake(evidence.Target, currentStake); err != nil {
			return types.Receipt{}, err
		}
		working.SetParam(consumedKey, "consumed")
		receipt.Events = append(receipt.Events, types.Event{Type: "validator.slashed", Attributes: map[string]string{
			"reporter": tx.From,
			"target":   evidence.Target,
			"amount":   strconv.FormatUint(currentStake, 10),
			"evidence": evidenceID,
			"offence":  offenceID,
			"removed":  "false",
		}})
	case types.TxSetCode:
		eventType, attributes, err := executeSetCode(working, tx)
		if err != nil {
			return types.Receipt{}, err
		}
		receipt.Events = append(receipt.Events, types.Event{Type: eventType, Attributes: attributes})
	case types.TxSessionKey:
		event, err := executeSessionKey(working, tx)
		if err != nil {
			return types.Receipt{}, err
		}
		receipt.Events = append(receipt.Events, event)
	case types.TxAccountRecovery:
		event, err := executeAccountRecovery(working, tx, context.BlockHeight)
		if err != nil {
			return types.Receipt{}, err
		}
		receipt.Events = append(receipt.Events, event)
	case types.TxWASMUpload:
		bytecode, err := parseWASMUploadPayload(tx.Payload)
		if err != nil {
			return types.Receipt{}, err
		}
		if err := e.runtime.ValidateAndCacheWasmCode(bytecode); err != nil {
			return types.Receipt{}, err
		}
		codeID := types.WASMCodeID(bytecode)
		working.SetContractCode(types.ContractCode{
			CodeID:          codeID,
			Runtime:         "wasm",
			MeteringVersion: contracts.WASMMeteringVersion,
			Creator:         strings.ToLower(tx.From),
			Bytecode:        "0x" + hex.EncodeToString(bytecode),
		})
		receipt.CodeID = codeID
		receipt.Events = append(receipt.Events, types.Event{Type: "wasm.code_uploaded", Attributes: map[string]string{
			"code_id": codeID,
			"creator": strings.ToLower(tx.From),
			"size":    strconv.Itoa(len(bytecode)),
		}})
	case types.TxDeploy:
		codeID := tx.Payload["code_id"]
		resourceLimit := tx.GasLimit - gasUsed
		address, events, resourceGas, executionErr := e.runtime.DeployMeteredInTransaction(working, tx.From, codeID, tx.Hash(), tx.Payload, resourceLimit)
		gasUsed, err = checkedAdd(gasUsed, resourceGas)
		if err != nil {
			return types.Receipt{}, fmt.Errorf("%w: deploy gas accounting: %v", ErrConsensusExecutionFault, err)
		}
		if executionErr != nil {
			return types.Receipt{}, executionErr
		}
		receipt.ContractAddress = address
		receipt.Events = append(receipt.Events, events...)
	case types.TxCall:
		method := tx.Payload["method"]
		resourceLimit := tx.GasLimit - gasUsed
		events, resourceGas, executionErr := e.runtime.CallMeteredInTransaction(working, tx.To, tx.From, method, tx.Payload, resourceLimit)
		gasUsed, err = checkedAdd(gasUsed, resourceGas)
		if err != nil {
			return types.Receipt{}, fmt.Errorf("%w: call gas accounting: %v", ErrConsensusExecutionFault, err)
		}
		if executionErr != nil {
			return types.Receipt{}, executionErr
		}
		receipt.Events = append(receipt.Events, events...)
		sessionEvent, err := applySessionKeyUsage(working, tx)
		if err != nil {
			return types.Receipt{}, err
		}
		if sessionEvent.Type != "" {
			receipt.Events = append(receipt.Events, sessionEvent)
		}
	default:
		return types.Receipt{}, fmt.Errorf("unsupported transaction type %q", tx.Type)
	}

	if tx.GasLimit < gasUsed {
		return types.Receipt{}, contracts.ErrContractOutOfGas
	}
	fee, err := CalculateFee(tx, gasUsed, context.BaseFeePerGas)
	if err != nil {
		return types.Receipt{}, fmt.Errorf("%w: fee settlement: %v", ErrConsensusExecutionFault, err)
	}
	if strings.TrimSpace(tx.Paymaster) != "" {
		receipt.FeePayer = tx.Paymaster
		receipt.Events = append(receipt.Events, types.Event{Type: "paymaster.sponsored", Attributes: map[string]string{
			"user":      strings.ToLower(tx.From),
			"paymaster": strings.ToLower(tx.Paymaster),
		}})
	} else if feePayer != strings.ToLower(strings.TrimSpace(tx.From)) {
		receipt.FeePayer = feePayer
	}
	if err := e.settleFeeEscrow(working, feePayer, maximumFee, fee); err != nil {
		return types.Receipt{}, err
	}
	receipt.GasUsed = gasUsed
	receipt.BaseFeePerGas = context.BaseFeePerGas
	receipt.EffectiveGasPrice = fee.EffectiveGasPrice
	receipt.BaseFeeBurned = fee.BaseFeeBurned
	receipt.PriorityFeePaid = fee.PriorityFee
	if err := types.ValidateReceiptSchema(receipt, tx.Hash()); err != nil {
		return types.Receipt{}, fmt.Errorf("%w: invalid generated receipt: %v", ErrConsensusExecutionFault, err)
	}
	store.ReplaceWith(working)
	return receipt, nil
}

func (e *Executor) executeBatchOperation(store *state.Store, tx types.Transaction, operation types.BatchOperation, index int, gasLimit uint64) ([]types.Event, uint64, error) {
	switch operation.Type {
	case types.TxTransfer:
		if err := store.Transfer(tx.From, operation.To, operation.Value); err != nil {
			return nil, 0, err
		}
		return []types.Event{{
			Type: "transfer",
			Attributes: map[string]string{
				"from":     tx.From,
				"to":       operation.To,
				"op_index": strconv.Itoa(index),
			},
		}}, 0, nil
	case types.TxCall:
		method := operation.Payload["method"]
		events, resourceGas, err := e.runtime.CallMeteredInTransaction(store, operation.To, tx.From, method, operation.Payload, gasLimit)
		if err != nil {
			return nil, resourceGas, err
		}
		tagBatchEvents(events, index)
		return events, resourceGas, nil
	default:
		return nil, 0, fmt.Errorf("unsupported batch operation type %q", operation.Type)
	}
}

func tagBatchEvents(events []types.Event, index int) {
	for i := range events {
		if events[i].Attributes == nil {
			events[i].Attributes = make(map[string]string)
		}
		events[i].Attributes["op_index"] = strconv.Itoa(index)
	}
}

func validateTransactionAuthorization(store *state.Store, tx types.Transaction, blockHeight uint64) error {
	if tx.Type == types.TxAccountRecovery {
		return validateAccountRecoveryAuthorization(store, tx)
	}
	if tx.Type == types.TxSetCode {
		if strings.TrimSpace(tx.Signer) != "" || len(tx.Authorizations) > 0 || tx.SignatureKind != "" {
			return errors.New("set_code must be signed directly by from")
		}
		if !chaincrypto.Verify(tx.From, tx.SigningBytes(), tx.Signature) {
			return errors.New("invalid transaction signature")
		}
		return nil
	}
	if tx.SignatureKind == types.SignatureKindEthereumType2 {
		return validateEthereumType2Authorization(tx)
	}
	if len(tx.Authorizations) > 0 {
		return validateMultisigAuthorization(store, tx)
	}
	signer := strings.ToLower(strings.TrimSpace(tx.Signer))
	if signer == "" {
		if !chaincrypto.Verify(tx.From, tx.SigningBytes(), tx.Signature) {
			return errors.New("invalid transaction signature")
		}
		return nil
	}
	if !chaincrypto.Verify(signer, tx.SigningBytes(), tx.Signature) {
		return errors.New("invalid transaction signature")
	}
	account := store.GetAccount(tx.From)
	if !isAccountV1Authority(account) {
		return errors.New("transaction signer requires account.v1 from account")
	}
	owner := strings.ToLower(strings.TrimSpace(store.GetStorage(tx.From, "owner")))
	if owner == "" {
		return fmt.Errorf("%w: smart account owner is not set", contracts.ErrContractStateFault)
	}
	if normalized, err := chaincrypto.NormalizeAddress(owner); err != nil || normalized != owner {
		return fmt.Errorf("%w: invalid smart account owner", contracts.ErrContractStateFault)
	}
	if signer == owner {
		return nil
	}
	return validateSessionKeyAuthorization(store, tx, signer, blockHeight)
}

func validateEthereumType2Authorization(tx types.Transaction) error {
	if strings.TrimSpace(tx.Signer) != "" || len(tx.Authorizations) > 0 || strings.TrimSpace(tx.Paymaster) != "" {
		return errors.New("ethereum type2 transactions cannot use ChainLab signer, multisig, or paymaster fields")
	}
	digest, err := types.EthereumType2SigningDigest(tx)
	if err != nil {
		return err
	}
	if !chaincrypto.VerifyDigest(tx.From, digest, tx.Signature) {
		return errors.New("invalid ethereum type2 transaction signature")
	}
	return nil
}

func validateMultisigAuthorization(store *state.Store, tx types.Transaction) error {
	if strings.TrimSpace(tx.Signer) != "" {
		return errors.New("multisig transaction cannot include signer")
	}
	account := store.GetAccount(tx.From)
	if account.CodeID != contracts.MultisigCodeID {
		return errors.New("transaction authorizations require multisig.v1 from account")
	}
	if len(tx.Authorizations) > contracts.MaxMultisigOwners {
		return fmt.Errorf("multisig authorization count exceeds %d", contracts.MaxMultisigOwners)
	}
	owners, err := multisigOwners(store.GetStorage(tx.From, "owners"))
	if err != nil {
		return fmt.Errorf("%w: %v", contracts.ErrContractStateFault, err)
	}
	threshold, err := strconv.ParseUint(strings.TrimSpace(store.GetStorage(tx.From, "threshold")), 10, 64)
	if err != nil || threshold == 0 {
		return fmt.Errorf("%w: multisig threshold is not set", contracts.ErrContractStateFault)
	}
	if threshold > uint64(len(owners)) {
		return fmt.Errorf("%w: multisig threshold exceeds owner count", contracts.ErrContractStateFault)
	}
	seen := make(map[string]struct{}, len(tx.Authorizations))
	for _, authorization := range tx.Authorizations {
		signer := strings.ToLower(strings.TrimSpace(authorization.Signer))
		if signer == "" || strings.TrimSpace(authorization.Signature) == "" {
			return errors.New("multisig authorization requires signer and signature")
		}
		if _, ok := seen[signer]; ok {
			return errors.New("duplicate multisig authorization")
		}
		seen[signer] = struct{}{}
		if _, ok := owners[signer]; !ok {
			return errors.New("multisig authorization signer is not an owner")
		}
		if !chaincrypto.Verify(signer, tx.SigningBytes(), authorization.Signature) {
			return errors.New("invalid multisig authorization signature")
		}
	}
	if uint64(len(seen)) < threshold {
		return errors.New("multisig authorization threshold not met")
	}
	return nil
}

func multisigOwners(raw string) (map[string]struct{}, error) {
	owners := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		owner, err := chaincrypto.NormalizeAddress(part)
		if err != nil || owner != part {
			return nil, errors.New("multisig owner is not canonically encoded")
		}
		owners[owner] = struct{}{}
	}
	if len(owners) == 0 {
		return nil, errors.New("multisig owners are not set")
	}
	return owners, nil
}

func validatePaymasterAuthorization(tx types.Transaction) error {
	if strings.TrimSpace(tx.Paymaster) == "" {
		return nil
	}
	if strings.TrimSpace(tx.PaymasterSignature) == "" {
		return errors.New("paymaster signature is required")
	}
	if !chaincrypto.Verify(tx.Paymaster, tx.PaymasterSigningBytes(), tx.PaymasterSignature) {
		return errors.New("invalid paymaster signature")
	}
	return nil
}

type FeeBreakdown struct {
	EffectiveGasPrice uint64
	BaseFeeBurned     uint64
	PriorityFee       uint64
	TotalFee          uint64
}

func CalculateFee(tx types.Transaction, gasUsed uint64, baseFeePerGas uint64) (FeeBreakdown, error) {
	maxFeePerGas, maxPriorityFeePerGas := feeCaps(tx)
	if maxFeePerGas < baseFeePerGas {
		return FeeBreakdown{}, errors.New("max fee per gas is below block base fee")
	}
	if maxPriorityFeePerGas > maxFeePerGas {
		return FeeBreakdown{}, errors.New("max priority fee per gas exceeds max fee per gas")
	}
	availablePriority := maxFeePerGas - baseFeePerGas
	priorityFeePerGas := maxPriorityFeePerGas
	if priorityFeePerGas > availablePriority {
		priorityFeePerGas = availablePriority
	}
	effectiveGasPrice, err := checkedAdd(baseFeePerGas, priorityFeePerGas)
	if err != nil {
		return FeeBreakdown{}, err
	}
	totalFee, err := checkedMul(gasUsed, effectiveGasPrice)
	if err != nil {
		return FeeBreakdown{}, err
	}
	baseFeeBurned, err := checkedMul(gasUsed, baseFeePerGas)
	if err != nil {
		return FeeBreakdown{}, err
	}
	priorityFee, err := checkedMul(gasUsed, priorityFeePerGas)
	if err != nil {
		return FeeBreakdown{}, err
	}
	return FeeBreakdown{
		EffectiveGasPrice: effectiveGasPrice,
		BaseFeeBurned:     baseFeeBurned,
		PriorityFee:       priorityFee,
		TotalFee:          totalFee,
	}, nil
}

func feeCaps(tx types.Transaction) (uint64, uint64) {
	if tx.MaxFeePerGas == 0 && tx.MaxPriorityFeePerGas == 0 {
		return tx.GasPrice, tx.GasPrice
	}
	return tx.MaxFeePerGas, tx.MaxPriorityFeePerGas
}

func (e *Executor) validateFeeCapacity(store *state.Store, tx types.Transaction, baseFeePerGas uint64) error {
	if _, err := CalculateFee(tx, 0, baseFeePerGas); err != nil {
		return err
	}
	maximumFee, err := maximumTransactionFee(tx)
	if err != nil {
		return err
	}
	feePayer := transactionFeePayer(tx)
	feeBalance := store.GetAccount(feePayer).Balance
	if feeBalance < maximumFee {
		return errors.New("insufficient funds")
	}
	valueExposure, err := transactionValueExposure(tx)
	if err != nil {
		return err
	}
	if feePayer == strings.ToLower(strings.TrimSpace(tx.From)) {
		if valueExposure > feeBalance-maximumFee {
			return errors.New("insufficient funds")
		}
	} else if store.GetAccount(tx.From).Balance < valueExposure {
		return errors.New("insufficient funds")
	}
	if err := validateExecutionCreditCapacity(store, tx, feePayer, 0); err != nil {
		return err
	}
	collector := strings.ToLower(strings.TrimSpace(e.feeCollector))
	if collector != "" && collector != feePayer {
		maximumBreakdown, err := CalculateFee(tx, tx.GasLimit, baseFeePerGas)
		if err != nil {
			return err
		}
		if err := validateExecutionCreditCapacity(store, tx, collector, maximumBreakdown.PriorityFee); err != nil {
			return err
		}
	}
	return nil
}

func validateExecutionCreditCapacity(store *state.Store, tx types.Transaction, target string, additional uint64) error {
	incoming, err := transactionIncomingValue(tx, target)
	if err != nil {
		return err
	}
	total, err := checkedAdd(incoming, additional)
	if err != nil {
		return errors.New("recipient balance overflow")
	}
	balance := store.GetAccount(target).Balance
	if total > math.MaxUint64-balance {
		return errors.New("recipient balance overflow")
	}
	return nil
}

func transactionIncomingValue(tx types.Transaction, target string) (uint64, error) {
	target = strings.ToLower(strings.TrimSpace(target))
	sender := strings.ToLower(strings.TrimSpace(tx.From))
	if target == "" {
		return 0, nil
	}
	switch tx.Type {
	case types.TxTransfer:
		if target == sender {
			return 0, nil
		}
		if strings.ToLower(strings.TrimSpace(tx.To)) == target {
			return tx.Value, nil
		}
	case types.TxBatch:
		if target == sender {
			return 0, nil
		}
		var incoming uint64
		for _, operation := range tx.Batch {
			if operation.Type != types.TxTransfer || strings.ToLower(strings.TrimSpace(operation.To)) != target {
				continue
			}
			next, err := checkedAdd(incoming, operation.Value)
			if err != nil {
				return 0, errors.New("recipient balance overflow")
			}
			incoming = next
		}
		return incoming, nil
	case types.TxUnstake:
		if target == sender {
			return tx.Value, nil
		}
	}
	return 0, nil
}

func maximumTransactionFee(tx types.Transaction) (uint64, error) {
	maxFeePerGas, _ := feeCaps(tx)
	return checkedMul(tx.GasLimit, maxFeePerGas)
}

func transactionValueExposure(tx types.Transaction) (uint64, error) {
	switch tx.Type {
	case types.TxTransfer, types.TxStake:
		return tx.Value, nil
	case types.TxBatch:
		var total uint64
		for _, operation := range tx.Batch {
			if operation.Type != types.TxTransfer {
				continue
			}
			var err error
			total, err = checkedAdd(total, operation.Value)
			if err != nil {
				return 0, errors.New("batch value overflow")
			}
		}
		return total, nil
	default:
		return 0, nil
	}
}

func transactionFeePayer(tx types.Transaction) string {
	if strings.TrimSpace(tx.Paymaster) != "" {
		return strings.ToLower(strings.TrimSpace(tx.Paymaster))
	}
	if tx.Type == types.TxAccountRecovery {
		action, _ := accountRecoveryAction(tx.Payload)
		if action == "approve" || action == "execute" {
			return strings.ToLower(strings.TrimSpace(tx.Signer))
		}
	}
	return strings.ToLower(strings.TrimSpace(tx.From))
}

func (e *Executor) settleFeeEscrow(store *state.Store, payer string, maximumFee uint64, fee FeeBreakdown) error {
	if fee.TotalFee > maximumFee {
		return fmt.Errorf("%w: actual fee exceeds escrow", ErrConsensusExecutionFault)
	}
	refund := maximumFee - fee.TotalFee
	if err := store.AddBalance(payer, refund); err != nil {
		return fmt.Errorf("%w: fee escrow refund: %v", ErrConsensusExecutionFault, err)
	}
	if e.feeCollector == "" || fee.PriorityFee == 0 {
		return nil
	}
	if err := store.AddBalance(e.feeCollector, fee.PriorityFee); err != nil {
		return fmt.Errorf("%w: priority fee credit: %v", ErrConsensusExecutionFault, err)
	}
	return nil
}

func EstimateGas(txType types.TxType) (uint64, error) {
	switch txType {
	case types.TxTransfer:
		return 21_000, nil
	case types.TxBatch:
		return 42_000, nil
	case types.TxStake, types.TxUnstake:
		return 30_000, nil
	case types.TxVote:
		return 25_000, nil
	case types.TxProposalSubmit, types.TxProposalExecute:
		return 35_000, nil
	case types.TxValidatorJoin, types.TxValidatorLeave:
		return 40_000, nil
	case types.TxValidatorSlash:
		return 45_000, nil
	case types.TxSetCode:
		return 45_000, nil
	case types.TxSessionKey:
		return 45_000, nil
	case types.TxAccountRecovery:
		return 55_000, nil
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

func EstimateGasForBatch(batch []types.BatchOperation) (uint64, error) {
	if len(batch) == 0 {
		return 0, errors.New("batch requires at least one operation")
	}
	if len(batch) > types.MaxBatchOperations {
		return 0, fmt.Errorf("batch exceeds %d operations", types.MaxBatchOperations)
	}
	total, err := EstimateGas(types.TxBatch)
	if err != nil {
		return 0, err
	}
	for _, operation := range batch {
		operationGas, err := EstimateGasForBatchOperation(operation)
		if err != nil {
			return 0, err
		}
		total, err = checkedAdd(total, operationGas)
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func EstimateGasForBatchOperation(operation types.BatchOperation) (uint64, error) {
	switch operation.Type {
	case types.TxTransfer, types.TxCall:
		return EstimateGas(operation.Type)
	default:
		return 0, fmt.Errorf("unsupported batch operation type %q", operation.Type)
	}
}

func parseProposalSubmitPayload(tx types.Transaction, blockHeight uint64) (types.Proposal, error) {
	title := strings.TrimSpace(tx.Payload["title"])
	if title == "" {
		return types.Proposal{}, errors.New("proposal title is required")
	}
	kind := strings.TrimSpace(tx.Payload["kind"])
	if kind == "" {
		return types.Proposal{}, errors.New("proposal kind is required")
	}
	periodRaw := strings.TrimSpace(tx.Payload["voting_period"])
	if periodRaw == "" {
		return types.Proposal{}, errors.New("proposal voting_period is required")
	}
	votingPeriod, err := strconv.ParseUint(periodRaw, 10, 64)
	if err != nil || votingPeriod == 0 {
		return types.Proposal{}, errors.New("proposal voting_period must be positive")
	}
	if math.MaxUint64-blockHeight < votingPeriod {
		return types.Proposal{}, errors.New("proposal voting end height overflow")
	}
	proposal := types.Proposal{
		ID:              types.ProposalID(tx.Hash()),
		Proposer:        strings.ToLower(tx.From),
		Title:           title,
		Description:     strings.TrimSpace(tx.Payload["description"]),
		Kind:            kind,
		Status:          types.ProposalStatusOpen,
		SubmitHeight:    blockHeight,
		VotingEndHeight: blockHeight + votingPeriod,
		Votes:           make(map[string]uint64),
		Voters:          make(map[string]string),
	}
	if kind == "param.change" {
		proposal.Param = strings.TrimSpace(tx.Payload["param"])
		proposal.Value = strings.TrimSpace(tx.Payload["value"])
		if proposal.Param == "" {
			return types.Proposal{}, errors.New("param.change proposal requires param")
		}
		if proposal.Value == "" {
			return types.Proposal{}, errors.New("param.change proposal requires value")
		}
		return proposal, nil
	}
	return types.Proposal{}, fmt.Errorf("unsupported proposal kind %q", kind)
}

func executeProposal(store *state.Store, proposal types.Proposal) error {
	if proposal.Kind == "param.change" {
		store.SetParam(proposal.Param, proposal.Value)
		return nil
	}
	return fmt.Errorf("unsupported proposal kind %q", proposal.Kind)
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
	if len(payload) != 1 {
		return nil, errors.New("wasm upload payload must contain only bytecode")
	}
	raw, ok := payload["bytecode"]
	if !ok {
		return nil, errors.New("wasm upload payload must contain only bytecode")
	}
	if raw == "" {
		return nil, errors.New("wasm upload requires bytecode")
	}
	if raw != strings.TrimSpace(raw) || !strings.HasPrefix(raw, "0x") || raw != strings.ToLower(raw) {
		return nil, errors.New("wasm bytecode must be canonical 0x-prefixed lowercase hex")
	}
	raw = raw[2:]
	if len(raw) > contracts.MaxWASMModuleBytes*2 {
		return nil, fmt.Errorf("wasm module size exceeds %d bytes", contracts.MaxWASMModuleBytes)
	}
	bytecode, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid wasm bytecode hex: %w", err)
	}
	if len(bytecode) == 0 {
		return nil, errors.New("wasm bytecode is required")
	}
	return bytecode, nil
}

func executeSetCode(store *state.Store, tx types.Transaction) (string, map[string]string, error) {
	account := store.GetAccount(tx.From)
	if strings.TrimSpace(account.CodeID) != "" {
		return "", nil, errors.New("set_code cannot target a contract account")
	}
	codeID := strings.TrimSpace(tx.Payload["code_id"])
	attributes := map[string]string{"account": strings.ToLower(tx.From)}
	if codeID == "" {
		store.ClearDelegation(tx.From)
		return "account.delegation_cleared", attributes, nil
	}
	if codeID != contracts.AccountCodeID {
		return "", nil, fmt.Errorf("unsupported delegated code id %q", codeID)
	}
	ownerRaw := strings.TrimSpace(tx.Payload["owner"])
	if ownerRaw == "" {
		return "", nil, errors.New("set_code account.v1 requires owner")
	}
	owner, err := normalizeRecoveryAddress(ownerRaw, "set_code owner")
	if err != nil {
		return "", nil, err
	}
	if strings.EqualFold(owner, tx.From) {
		return "", nil, errors.New("set_code owner must differ from delegated account")
	}
	store.ClearDelegation(tx.From)
	store.SetDelegatedCodeID(tx.From, codeID)
	if err := store.SetStorage(tx.From, "owner", owner); err != nil {
		return "", nil, err
	}
	attributes["code_id"] = codeID
	attributes["owner"] = owner
	return "account.delegation_set", attributes, nil
}

type sessionKeyPolicy struct {
	Key        string
	Limit      uint64
	Spent      uint64
	Expires    uint64
	To         string
	CallTo     string
	CallMethod string
}

func executeSessionKey(store *state.Store, tx types.Transaction) (types.Event, error) {
	account := store.GetAccount(tx.From)
	if !isAccountV1Authority(account) {
		return types.Event{}, errors.New("session key requires account.v1 from account")
	}
	owner := strings.ToLower(strings.TrimSpace(store.GetStorage(tx.From, "owner")))
	if owner == "" || strings.ToLower(strings.TrimSpace(tx.Signer)) != owner {
		return types.Event{}, errors.New("session key changes require account owner")
	}
	action := strings.ToLower(strings.TrimSpace(tx.Payload["action"]))
	keyRaw := tx.Payload["key"]
	if keyRaw == "" {
		return types.Event{}, errors.New("session key address is required")
	}
	key, err := chaincrypto.NormalizeAddress(keyRaw)
	if err != nil || key != keyRaw {
		return types.Event{}, errors.New("session key must be a canonical 20-byte address")
	}
	prefix := sessionKeyStoragePrefix(key)
	attributes := map[string]string{
		"account": strings.ToLower(tx.From),
		"key":     key,
	}
	switch action {
	case "add":
		if store.GetStorage(tx.From, prefix+"epoch") == "" && activeSessionKeyCount(store, tx.From) >= maxSessionKeys {
			return types.Event{}, fmt.Errorf("session key count exceeds %d", maxSessionKeys)
		}
		limitRaw := strings.TrimSpace(tx.Payload["limit"])
		limit := uint64(0)
		if limitRaw != "" {
			var err error
			limit, err = parsePositiveUint(limitRaw, "session key limit")
			if err != nil {
				return types.Event{}, err
			}
		}
		expires, err := parseOptionalUint(tx.Payload["expires"], "session key expires")
		if err != nil {
			return types.Event{}, err
		}
		allowedTo, err := optionalCanonicalAddress(tx.Payload["to"], "session key transfer target")
		if err != nil {
			return types.Event{}, err
		}
		callTo, err := optionalCanonicalAddress(tx.Payload["call_to"], "session key call target")
		if err != nil {
			return types.Event{}, err
		}
		callMethod := strings.TrimSpace(tx.Payload["call_method"])
		if (callTo == "") != (callMethod == "") {
			return types.Event{}, errors.New("session key call policy requires call_to and call_method")
		}
		if limit == 0 && callTo == "" {
			return types.Event{}, errors.New("session key requires transfer limit or call policy")
		}
		if limit == 0 {
			store.DeleteStorage(tx.From, prefix+"limit")
			store.DeleteStorage(tx.From, prefix+"spent")
		} else {
			if err := store.SetStorage(tx.From, prefix+"limit", strconv.FormatUint(limit, 10)); err != nil {
				return types.Event{}, err
			}
			if err := store.SetStorage(tx.From, prefix+"spent", "0"); err != nil {
				return types.Event{}, err
			}
			attributes["limit"] = strconv.FormatUint(limit, 10)
		}
		if expires == 0 {
			store.DeleteStorage(tx.From, prefix+"expires")
		} else {
			if err := store.SetStorage(tx.From, prefix+"expires", strconv.FormatUint(expires, 10)); err != nil {
				return types.Event{}, err
			}
			attributes["expires"] = strconv.FormatUint(expires, 10)
		}
		if allowedTo == "" {
			store.DeleteStorage(tx.From, prefix+"to")
		} else {
			if err := store.SetStorage(tx.From, prefix+"to", allowedTo); err != nil {
				return types.Event{}, err
			}
			attributes["to"] = allowedTo
		}
		if callTo == "" {
			store.DeleteStorage(tx.From, prefix+"call_to")
			store.DeleteStorage(tx.From, prefix+"call_method")
		} else {
			if err := store.SetStorage(tx.From, prefix+"call_to", callTo); err != nil {
				return types.Event{}, err
			}
			if err := store.SetStorage(tx.From, prefix+"call_method", callMethod); err != nil {
				return types.Event{}, err
			}
			attributes["call_to"] = callTo
			attributes["call_method"] = callMethod
		}
		epoch, err := sessionEpoch(store, tx.From)
		if err != nil {
			return types.Event{}, err
		}
		if err := store.SetStorage(tx.From, prefix+"epoch", strconv.FormatUint(epoch, 10)); err != nil {
			return types.Event{}, err
		}
		return types.Event{Type: "account.session_key_added", Attributes: attributes}, nil
	case "revoke":
		store.DeleteStorage(tx.From, prefix+"limit")
		store.DeleteStorage(tx.From, prefix+"spent")
		store.DeleteStorage(tx.From, prefix+"expires")
		store.DeleteStorage(tx.From, prefix+"to")
		store.DeleteStorage(tx.From, prefix+"call_to")
		store.DeleteStorage(tx.From, prefix+"call_method")
		store.DeleteStorage(tx.From, prefix+"epoch")
		return types.Event{Type: "account.session_key_revoked", Attributes: attributes}, nil
	default:
		return types.Event{}, errors.New("session key action must be add or revoke")
	}
}

func validateSessionKeyAuthorization(store *state.Store, tx types.Transaction, signer string, blockHeight uint64) error {
	policy, err := parseSessionKeyPolicy(store, tx.From, signer)
	if err != nil {
		return err
	}
	if policy.Expires != 0 && blockHeight > policy.Expires {
		return errors.New("session key expired")
	}
	switch tx.Type {
	case types.TxTransfer:
		return validateSessionKeyTransfer(tx, policy)
	case types.TxCall:
		return validateSessionKeyCall(tx, policy)
	default:
		return errors.New("session key can only authorize transfer or call")
	}
}

func validateSessionKeyTransfer(tx types.Transaction, policy sessionKeyPolicy) error {
	if policy.Limit == 0 {
		return errors.New("session key is not authorized for transfer")
	}
	if policy.To != "" && !strings.EqualFold(policy.To, tx.To) {
		return errors.New("session key recipient not allowed")
	}
	if policy.Spent > policy.Limit {
		return errors.New("session key spent exceeds limit")
	}
	remaining := policy.Limit - policy.Spent
	if tx.Value > remaining {
		return errors.New("session key transfer exceeds remaining limit")
	}
	return nil
}

func validateSessionKeyCall(tx types.Transaction, policy sessionKeyPolicy) error {
	if policy.CallTo == "" || policy.CallMethod == "" {
		return errors.New("session key is not authorized for call")
	}
	if !strings.EqualFold(policy.CallTo, tx.To) {
		return errors.New("session key call target not allowed")
	}
	if strings.TrimSpace(tx.Payload["method"]) != policy.CallMethod {
		return errors.New("session key call method not allowed")
	}
	return nil
}

func applySessionKeyUsage(store *state.Store, tx types.Transaction) (types.Event, error) {
	signer := strings.ToLower(strings.TrimSpace(tx.Signer))
	if signer == "" {
		return types.Event{}, nil
	}
	account := store.GetAccount(tx.From)
	if !isAccountV1Authority(account) {
		return types.Event{}, nil
	}
	owner := strings.ToLower(strings.TrimSpace(store.GetStorage(tx.From, "owner")))
	if signer == owner {
		return types.Event{}, nil
	}
	policy, err := parseSessionKeyPolicy(store, tx.From, signer)
	if err != nil {
		return types.Event{}, err
	}
	if tx.Type == types.TxCall {
		return types.Event{Type: "account.session_key_used", Attributes: map[string]string{
			"account": strings.ToLower(tx.From),
			"key":     signer,
			"type":    "call",
			"to":      strings.ToLower(tx.To),
			"method":  strings.TrimSpace(tx.Payload["method"]),
		}}, nil
	}
	spent, err := checkedAdd(policy.Spent, tx.Value)
	if err != nil {
		return types.Event{}, err
	}
	if err := store.SetStorage(tx.From, sessionKeyStoragePrefix(signer)+"spent", strconv.FormatUint(spent, 10)); err != nil {
		return types.Event{}, err
	}
	return types.Event{Type: "account.session_key_used", Attributes: map[string]string{
		"account": strings.ToLower(tx.From),
		"key":     signer,
		"spent":   strconv.FormatUint(spent, 10),
		"value":   strconv.FormatUint(tx.Value, 10),
	}}, nil
}

func parseSessionKeyPolicy(store *state.Store, account string, key string) (sessionKeyPolicy, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	prefix := sessionKeyStoragePrefix(key)
	limit := uint64(0)
	limitRaw := strings.TrimSpace(store.GetStorage(account, prefix+"limit"))
	if limitRaw != "" {
		var err error
		limit, err = parsePositiveUint(limitRaw, "session key limit")
		if err != nil {
			return sessionKeyPolicy{}, fmt.Errorf("%w: invalid stored session key limit", contracts.ErrContractStateFault)
		}
	}
	spent, err := parseOptionalUint(store.GetStorage(account, prefix+"spent"), "session key spent")
	if err != nil {
		return sessionKeyPolicy{}, fmt.Errorf("%w: %v", contracts.ErrContractStateFault, err)
	}
	expires, err := parseOptionalUint(store.GetStorage(account, prefix+"expires"), "session key expires")
	if err != nil {
		return sessionKeyPolicy{}, fmt.Errorf("%w: %v", contracts.ErrContractStateFault, err)
	}
	callToRaw := store.GetStorage(account, prefix+"call_to")
	callTo, err := optionalCanonicalAddress(callToRaw, "stored session key call target")
	if err != nil {
		return sessionKeyPolicy{}, fmt.Errorf("%w: %v", contracts.ErrContractStateFault, err)
	}
	callMethod := strings.TrimSpace(store.GetStorage(account, prefix+"call_method"))
	if (callTo == "") != (callMethod == "") {
		return sessionKeyPolicy{}, fmt.Errorf("%w: session key call policy is incomplete", contracts.ErrContractStateFault)
	}
	if limit == 0 && callTo == "" {
		return sessionKeyPolicy{}, errors.New("session key is not authorized")
	}
	currentEpoch, err := sessionEpoch(store, account)
	if err != nil {
		return sessionKeyPolicy{}, err
	}
	policyEpoch, err := parseOptionalUint(store.GetStorage(account, prefix+"epoch"), "session key epoch")
	if err != nil {
		return sessionKeyPolicy{}, fmt.Errorf("%w: %v", contracts.ErrContractStateFault, err)
	}
	if policyEpoch != currentEpoch {
		return sessionKeyPolicy{}, errors.New("session key is not authorized")
	}
	toRaw := store.GetStorage(account, prefix+"to")
	to, err := optionalCanonicalAddress(toRaw, "stored session key transfer target")
	if err != nil {
		return sessionKeyPolicy{}, fmt.Errorf("%w: %v", contracts.ErrContractStateFault, err)
	}
	return sessionKeyPolicy{
		Key:        key,
		Limit:      limit,
		Spent:      spent,
		Expires:    expires,
		To:         to,
		CallTo:     callTo,
		CallMethod: callMethod,
	}, nil
}

func activeSessionKeyCount(store *state.Store, account string) int {
	keys := make(map[string]struct{})
	for key := range store.GetAccount(account).Storage {
		if !strings.HasPrefix(key, "session:") || !strings.HasSuffix(key, ":epoch") || key == sessionEpochKey {
			continue
		}
		keys[strings.TrimSuffix(strings.TrimPrefix(key, "session:"), ":epoch")] = struct{}{}
	}
	return len(keys)
}

func optionalCanonicalAddress(raw string, field string) (string, error) {
	if raw == "" {
		return "", nil
	}
	address, err := chaincrypto.NormalizeAddress(raw)
	if err != nil || address != raw {
		return "", fmt.Errorf("%s must be a canonical 20-byte address", field)
	}
	return address, nil
}

func sessionKeyStoragePrefix(key string) string {
	return "session:" + strings.ToLower(strings.TrimSpace(key)) + ":"
}

func sessionEpoch(store *state.Store, account string) (uint64, error) {
	epoch, err := parseOptionalUint(store.GetStorage(account, sessionEpochKey), "session epoch")
	if err != nil {
		return 0, fmt.Errorf("%w: %v", contracts.ErrContractStateFault, err)
	}
	return epoch, nil
}

func isAccountV1Authority(account types.Account) bool {
	return account.CodeID == contracts.AccountCodeID || (account.CodeID == "" && account.DelegatedCodeID == contracts.AccountCodeID)
}

func parsePositiveUint(raw string, field string) (uint64, error) {
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 || strconv.FormatUint(value, 10) != raw {
		return 0, fmt.Errorf("%s must be positive", field)
	}
	return value, nil
}

func parseOptionalUint(raw string, field string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || strconv.FormatUint(value, 10) != raw {
		return 0, fmt.Errorf("%s must be an unsigned integer", field)
	}
	return value, nil
}

type validatorSlashEvidence struct {
	Target          string `json:"target"`
	Height          uint64 `json:"height"`
	FirstBlockHash  string `json:"first_block_hash"`
	FirstSignature  string `json:"first_signature"`
	SecondBlockHash string `json:"second_block_hash"`
	SecondSignature string `json:"second_signature"`
}

func parseSlashPayload(payload map[string]string) (validatorSlashEvidence, error) {
	fields := []string{"target", "height", "first_block_hash", "first_signature", "second_block_hash", "second_signature"}
	if err := requirePayloadFields(payload, "validator slash", fields); err != nil {
		return validatorSlashEvidence{}, err
	}
	heightRaw := payload["height"]
	height, err := strconv.ParseUint(heightRaw, 10, 64)
	if err != nil || height == 0 || strconv.FormatUint(height, 10) != heightRaw {
		return validatorSlashEvidence{}, errors.New("slash evidence height must be a positive canonical integer")
	}
	evidence := validatorSlashEvidence{
		Target:          payload["target"],
		Height:          height,
		FirstBlockHash:  payload["first_block_hash"],
		FirstSignature:  payload["first_signature"],
		SecondBlockHash: payload["second_block_hash"],
		SecondSignature: payload["second_signature"],
	}
	if err := validateSlashEvidenceShape(evidence); err != nil {
		return validatorSlashEvidence{}, err
	}
	return evidence, nil
}

func validateSlashEvidenceShape(evidence validatorSlashEvidence) error {
	target, err := chaincrypto.NormalizeAddress(evidence.Target)
	if err != nil || target != evidence.Target {
		return errors.New("slash target is not canonically encoded")
	}
	if evidence.FirstBlockHash == evidence.SecondBlockHash {
		return errors.New("slash evidence must contain two distinct block hashes")
	}
	if err := types.ValidateCanonicalHash("first slash evidence block hash", evidence.FirstBlockHash); err != nil {
		return err
	}
	if err := types.ValidateCanonicalHash("second slash evidence block hash", evidence.SecondBlockHash); err != nil {
		return err
	}
	if err := types.ValidateCanonicalSignature("first slash evidence signature", evidence.FirstSignature); err != nil {
		return err
	}
	return types.ValidateCanonicalSignature("second slash evidence signature", evidence.SecondSignature)
}

func validateSlashEvidence(chainID string, evidence validatorSlashEvidence) error {
	if err := validateSlashEvidenceShape(evidence); err != nil {
		return err
	}
	firstMessage := types.FinalityVoteSigningBytes(chainID, evidence.Height, evidence.FirstBlockHash)
	secondMessage := types.FinalityVoteSigningBytes(chainID, evidence.Height, evidence.SecondBlockHash)
	if !chaincrypto.Verify(evidence.Target, firstMessage, evidence.FirstSignature) ||
		!chaincrypto.Verify(evidence.Target, secondMessage, evidence.SecondSignature) {
		return errors.New("slash evidence signatures are invalid")
	}
	return nil
}

func slashEvidenceID(chainID string, evidence validatorSlashEvidence) string {
	return types.FinalityEquivocationID(
		chainID,
		evidence.Target,
		evidence.Height,
		evidence.FirstBlockHash,
		evidence.SecondBlockHash,
	)
}

func slashOffenceID(chainID string, evidence validatorSlashEvidence) string {
	return types.FinalityEquivocationOffenceID(
		chainID,
		evidence.Target,
		evidence.Height,
	)
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
