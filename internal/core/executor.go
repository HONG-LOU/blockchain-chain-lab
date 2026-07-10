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

const sessionEpochKey = "session:epoch"

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

func (e *Executor) ExecuteWithContext(store *state.Store, tx types.Transaction, context ExecutionContext) (types.Receipt, error) {
	if tx.ChainID != e.chainID {
		return types.Receipt{}, fmt.Errorf("wrong chain id %q", tx.ChainID)
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

	baseGas, err := EstimateGas(tx.Type)
	if err != nil {
		return types.Receipt{}, err
	}

	working := store.Clone()
	if err := working.IncrementNonce(tx.From); err != nil {
		return types.Receipt{}, err
	}

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
		sessionEvent, err := applySessionKeyUsage(working, tx)
		if err != nil {
			return types.Receipt{}, err
		}
		if sessionEvent.Type != "" {
			receipt.Events = append(receipt.Events, sessionEvent)
		}
	case types.TxBatch:
		if len(tx.Batch) == 0 {
			return types.Receipt{}, errors.New("batch requires at least one operation")
		}
		receipt.Events = append(receipt.Events, types.Event{Type: "batch.executed", Attributes: map[string]string{
			"operations": strconv.Itoa(len(tx.Batch)),
		}})
		for index, operation := range tx.Batch {
			events, operationGas, err := e.executeBatchOperation(working, tx, operation, index)
			if err != nil {
				return types.Receipt{}, err
			}
			gasUsed, err = checkedAdd(gasUsed, operationGas)
			if err != nil {
				return types.Receipt{}, err
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
		if err := working.SubStake(tx.From, tx.Value); err != nil {
			return types.Receipt{}, err
		}
		if err := working.AddBalance(tx.From, tx.Value); err != nil {
			return types.Receipt{}, err
		}
		receipt.Events = append(receipt.Events, types.Event{Type: "unstake"})
	case types.TxProposalSubmit:
		proposal, err := parseProposalSubmitPayload(tx, context.BlockHeight)
		if err != nil {
			return types.Receipt{}, err
		}
		if working.StakeOf(tx.From) == 0 {
			return types.Receipt{}, errors.New("proposal submit requires stake")
		}
		working.SetProposal(proposal)
		receipt.ProposalID = proposal.ID
		receipt.Events = append(receipt.Events, types.Event{Type: "governance.proposal.submitted", Attributes: map[string]string{
			"proposal": proposal.ID,
			"kind":     proposal.Kind,
			"proposer": strings.ToLower(tx.From),
		}})
	case types.TxVote:
		proposal := tx.Payload["proposal"]
		choice := tx.Payload["choice"]
		if proposal == "" || choice == "" {
			return types.Receipt{}, errors.New("vote requires proposal and choice")
		}
		choice = strings.ToLower(strings.TrimSpace(choice))
		if choice != "yes" && choice != "no" && choice != "abstain" {
			return types.Receipt{}, errors.New("vote choice must be yes, no, or abstain")
		}
		record := working.Proposal(proposal)
		if record.Status != types.ProposalStatusOpen {
			return types.Receipt{}, errors.New("proposal is not open")
		}
		if record.VotingEndHeight != 0 && context.BlockHeight >= record.VotingEndHeight {
			return types.Receipt{}, errors.New("proposal voting period has ended")
		}
		power := working.StakeOf(tx.From)
		if power == 0 {
			return types.Receipt{}, errors.New("voter has no stake")
		}
		working.RecordVote(proposal, tx.From, choice, power)
		receipt.Events = append(receipt.Events, types.Event{Type: "governance.vote", Attributes: map[string]string{"proposal": proposal, "choice": choice}})
	case types.TxProposalExecute:
		proposalID := strings.TrimSpace(tx.Payload["proposal"])
		if proposalID == "" {
			return types.Receipt{}, errors.New("proposal execute requires proposal")
		}
		proposal := working.Proposal(proposalID)
		if proposal.Status != types.ProposalStatusOpen {
			return types.Receipt{}, errors.New("proposal is not open")
		}
		if context.BlockHeight < proposal.VotingEndHeight {
			return types.Receipt{}, errors.New("proposal voting period is still open")
		}
		eventType := "governance.proposal.rejected"
		if proposal.Votes["yes"] > proposal.Votes["no"] && proposal.Votes["yes"] > 0 {
			if err := executeProposal(working, proposal); err != nil {
				return types.Receipt{}, err
			}
			proposal.Status = types.ProposalStatusExecuted
			eventType = "governance.proposal.executed"
		} else {
			proposal.Status = types.ProposalStatusRejected
		}
		working.SetProposal(proposal)
		receipt.ProposalID = proposal.ID
		receipt.Events = append(receipt.Events, types.Event{Type: eventType, Attributes: map[string]string{
			"proposal": proposal.ID,
			"yes":      strconv.FormatUint(proposal.Votes["yes"], 10),
			"no":       strconv.FormatUint(proposal.Votes["no"], 10),
		}})
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
		return types.Receipt{}, errors.New("gas limit too low")
	}
	fee, err := CalculateFee(tx, gasUsed, context.BaseFeePerGas)
	if err != nil {
		return types.Receipt{}, err
	}
	feePayer := tx.From
	if strings.TrimSpace(tx.Paymaster) != "" {
		feePayer = tx.Paymaster
		receipt.FeePayer = tx.Paymaster
		receipt.Events = append(receipt.Events, types.Event{Type: "paymaster.sponsored", Attributes: map[string]string{
			"user":      strings.ToLower(tx.From),
			"paymaster": strings.ToLower(tx.Paymaster),
		}})
	} else if tx.Type == types.TxAccountRecovery {
		action, _ := accountRecoveryAction(tx.Payload)
		if action == "approve" || action == "execute" {
			feePayer = strings.ToLower(strings.TrimSpace(tx.Signer))
			receipt.FeePayer = feePayer
		}
	}
	if err := e.chargeFee(working, feePayer, fee.TotalFee, fee.PriorityFee); err != nil {
		return types.Receipt{}, err
	}
	receipt.GasUsed = gasUsed
	receipt.BaseFeePerGas = context.BaseFeePerGas
	receipt.EffectiveGasPrice = fee.EffectiveGasPrice
	receipt.BaseFeeBurned = fee.BaseFeeBurned
	receipt.PriorityFeePaid = fee.PriorityFee
	store.ReplaceWith(working)
	return receipt, nil
}

func (e *Executor) executeBatchOperation(store *state.Store, tx types.Transaction, operation types.BatchOperation, index int) ([]types.Event, uint64, error) {
	switch operation.Type {
	case types.TxTransfer:
		if strings.TrimSpace(operation.To) == "" {
			return nil, 0, fmt.Errorf("batch operation %d transfer requires to", index)
		}
		if err := store.Transfer(tx.From, operation.To, operation.Value); err != nil {
			return nil, 0, err
		}
		gas, err := EstimateGas(types.TxTransfer)
		if err != nil {
			return nil, 0, err
		}
		return []types.Event{{
			Type: "transfer",
			Attributes: map[string]string{
				"from":     tx.From,
				"to":       operation.To,
				"op_index": strconv.Itoa(index),
			},
		}}, gas, nil
	case types.TxCall:
		method := operation.Payload["method"]
		if strings.TrimSpace(operation.To) == "" || strings.TrimSpace(method) == "" {
			return nil, 0, fmt.Errorf("batch operation %d call requires to and method", index)
		}
		events, resourceGas, err := e.runtime.CallMetered(store, operation.To, tx.From, method, operation.Payload)
		if err != nil {
			return nil, 0, err
		}
		gas, err := EstimateGas(types.TxCall)
		if err != nil {
			return nil, 0, err
		}
		gas, err = checkedAdd(gas, resourceGas)
		if err != nil {
			return nil, 0, err
		}
		tagBatchEvents(events, index)
		return events, gas, nil
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
		return errors.New("smart account owner is not set")
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
	owners, err := multisigOwners(store.GetStorage(tx.From, "owners"))
	if err != nil {
		return err
	}
	threshold, err := strconv.ParseUint(strings.TrimSpace(store.GetStorage(tx.From, "threshold")), 10, 64)
	if err != nil || threshold == 0 {
		return errors.New("multisig threshold is not set")
	}
	if threshold > uint64(len(owners)) {
		return errors.New("multisig threshold exceeds owner count")
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
		owner := strings.ToLower(strings.TrimSpace(part))
		if owner == "" {
			continue
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

func (e *Executor) chargeFee(store *state.Store, from string, totalFee uint64, priorityFee uint64) error {
	if totalFee == 0 {
		return nil
	}
	if err := store.SubBalance(from, totalFee); err != nil {
		return err
	}
	if e.feeCollector == "" || priorityFee == 0 {
		return nil
	}
	return store.AddBalance(e.feeCollector, priorityFee)
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
	store.SetStorage(tx.From, "owner", owner)
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
	key := strings.ToLower(strings.TrimSpace(tx.Payload["key"]))
	if key == "" {
		return types.Event{}, errors.New("session key address is required")
	}
	prefix := sessionKeyStoragePrefix(key)
	attributes := map[string]string{
		"account": strings.ToLower(tx.From),
		"key":     key,
	}
	switch action {
	case "add":
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
		allowedTo := strings.ToLower(strings.TrimSpace(tx.Payload["to"]))
		callTo := strings.ToLower(strings.TrimSpace(tx.Payload["call_to"]))
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
			store.SetStorage(tx.From, prefix+"limit", strconv.FormatUint(limit, 10))
			store.SetStorage(tx.From, prefix+"spent", "0")
			attributes["limit"] = strconv.FormatUint(limit, 10)
		}
		if expires == 0 {
			store.DeleteStorage(tx.From, prefix+"expires")
		} else {
			store.SetStorage(tx.From, prefix+"expires", strconv.FormatUint(expires, 10))
			attributes["expires"] = strconv.FormatUint(expires, 10)
		}
		if allowedTo == "" {
			store.DeleteStorage(tx.From, prefix+"to")
		} else {
			store.SetStorage(tx.From, prefix+"to", allowedTo)
			attributes["to"] = allowedTo
		}
		if callTo == "" {
			store.DeleteStorage(tx.From, prefix+"call_to")
			store.DeleteStorage(tx.From, prefix+"call_method")
		} else {
			store.SetStorage(tx.From, prefix+"call_to", callTo)
			store.SetStorage(tx.From, prefix+"call_method", callMethod)
			attributes["call_to"] = callTo
			attributes["call_method"] = callMethod
		}
		epoch, err := sessionEpoch(store, tx.From)
		if err != nil {
			return types.Event{}, err
		}
		store.SetStorage(tx.From, prefix+"epoch", strconv.FormatUint(epoch, 10))
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
	store.SetStorage(tx.From, sessionKeyStoragePrefix(signer)+"spent", strconv.FormatUint(spent, 10))
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
			return sessionKeyPolicy{}, errors.New("session key is not authorized")
		}
	}
	spent, err := parseOptionalUint(store.GetStorage(account, prefix+"spent"), "session key spent")
	if err != nil {
		return sessionKeyPolicy{}, err
	}
	expires, err := parseOptionalUint(store.GetStorage(account, prefix+"expires"), "session key expires")
	if err != nil {
		return sessionKeyPolicy{}, err
	}
	callTo := strings.ToLower(strings.TrimSpace(store.GetStorage(account, prefix+"call_to")))
	callMethod := strings.TrimSpace(store.GetStorage(account, prefix+"call_method"))
	if (callTo == "") != (callMethod == "") {
		return sessionKeyPolicy{}, errors.New("session key call policy is incomplete")
	}
	if limit == 0 && callTo == "" {
		return sessionKeyPolicy{}, errors.New("session key is not authorized")
	}
	currentEpoch, err := sessionEpoch(store, account)
	if err != nil {
		return sessionKeyPolicy{}, err
	}
	policyEpoch, err := parseOptionalUint(store.GetStorage(account, prefix+"epoch"), "session key epoch")
	if err != nil || policyEpoch != currentEpoch {
		return sessionKeyPolicy{}, errors.New("session key is not authorized")
	}
	return sessionKeyPolicy{
		Key:        key,
		Limit:      limit,
		Spent:      spent,
		Expires:    expires,
		To:         strings.ToLower(strings.TrimSpace(store.GetStorage(account, prefix+"to"))),
		CallTo:     callTo,
		CallMethod: callMethod,
	}, nil
}

func sessionKeyStoragePrefix(key string) string {
	return "session:" + strings.ToLower(strings.TrimSpace(key)) + ":"
}

func sessionEpoch(store *state.Store, account string) (uint64, error) {
	return parseOptionalUint(store.GetStorage(account, sessionEpochKey), "session epoch")
}

func isAccountV1Authority(account types.Account) bool {
	return account.CodeID == contracts.AccountCodeID || (account.CodeID == "" && account.DelegatedCodeID == contracts.AccountCodeID)
}

func parsePositiveUint(raw string, field string) (uint64, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s must be positive", field)
	}
	return value, nil
}

func parseOptionalUint(raw string, field string) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an unsigned integer", field)
	}
	return value, nil
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
