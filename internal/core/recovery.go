package core

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

const (
	maxRecoveryGuardians       = 16
	maxRecoveryGuardianListLen = maxRecoveryGuardians*43 - 1
	maxRecoveryVoteListLen     = maxRecoveryGuardians*85 + maxRecoveryGuardians - 1
	recoveryProposalWindow     = uint64(256)
	recoveryGuardiansKey       = "recovery:guardians"
	recoveryThresholdKey       = "recovery:threshold"
	recoveryDelayKey           = "recovery:delay"
	recoveryPendingOwnerKey    = "recovery:pending_owner"
	recoveryExecuteAfterKey    = "recovery:execute_after"
	recoveryExpiresAtKey       = "recovery:expires_at"
	recoveryApprovalsKey       = "recovery:approvals"
	recoveryVotesKey           = "recovery:votes"
	recoveryStoragePrefix      = "recovery:"
)

func validateAccountRecoveryAuthorization(store *state.Store, tx types.Transaction) error {
	if len(tx.Authorizations) > 0 {
		return errors.New("account recovery does not support multisig authorizations")
	}
	if strings.TrimSpace(tx.SignatureKind) != "" {
		return errors.New("account recovery does not support ethereum signatures")
	}
	if strings.TrimSpace(tx.Paymaster) != "" || strings.TrimSpace(tx.PaymasterSignature) != "" {
		return errors.New("account recovery does not support paymasters")
	}
	if strings.TrimSpace(tx.To) != "" || tx.Value != 0 || len(tx.Batch) > 0 {
		return errors.New("account recovery does not support to, value, or batch fields")
	}
	signer := strings.ToLower(strings.TrimSpace(tx.Signer))
	if signer == "" {
		return errors.New("account recovery requires signer")
	}
	if !chaincrypto.Verify(signer, tx.SigningBytes(), tx.Signature) {
		return errors.New("invalid transaction signature")
	}
	account := store.GetAccount(tx.From)
	if !isAccountV1Authority(account) {
		return errors.New("account recovery requires account.v1 from account")
	}
	action, err := accountRecoveryAction(tx.Payload)
	if err != nil {
		return err
	}
	if err := validateAccountRecoveryPayload(action, tx.Payload); err != nil {
		return err
	}
	switch action {
	case "configure", "cancel", "clear":
		owner := strings.ToLower(strings.TrimSpace(store.GetStorage(tx.From, "owner")))
		if owner == "" || signer != owner {
			return errors.New("account recovery owner action requires current owner")
		}
	case "approve", "execute":
		guardians, err := storedRecoveryGuardians(store, tx.From)
		if err != nil {
			return err
		}
		if !containsAddress(guardians, signer) {
			return errors.New("account recovery guardian action requires configured guardian")
		}
	}
	return nil
}

func validateAccountRecoveryPayload(action string, payload map[string]string) error {
	allowed := map[string]struct{}{"action": {}}
	switch action {
	case "configure":
		allowed["guardians"] = struct{}{}
		allowed["threshold"] = struct{}{}
		allowed["delay"] = struct{}{}
	case "approve", "execute":
		allowed["new_owner"] = struct{}{}
	}
	for key := range payload {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("account recovery %s payload contains unsupported fields", action)
		}
	}
	return nil
}

func executeAccountRecovery(store *state.Store, tx types.Transaction, blockHeight uint64) (types.Event, error) {
	action, err := accountRecoveryAction(tx.Payload)
	if err != nil {
		return types.Event{}, err
	}
	signer := strings.ToLower(strings.TrimSpace(tx.Signer))
	attributes := map[string]string{
		"account": strings.ToLower(strings.TrimSpace(tx.From)),
		"action":  action,
		"signer":  signer,
	}

	switch action {
	case "configure":
		guardians, err := parseRecoveryGuardians(tx.Payload["guardians"])
		if err != nil {
			return types.Event{}, err
		}
		if err := validateRecoveryGuardians(store, tx.From, guardians); err != nil {
			return types.Event{}, err
		}
		threshold, err := parseRecoveryThreshold(tx.Payload["threshold"], len(guardians))
		if err != nil {
			return types.Event{}, err
		}
		delayRaw, ok := tx.Payload["delay"]
		if !ok || strings.TrimSpace(delayRaw) == "" {
			return types.Event{}, errors.New("recovery delay is required")
		}
		delay, err := parseRecoveryDelay(delayRaw)
		if err != nil {
			return types.Event{}, err
		}
		guardiansRaw := strings.Join(guardians, ",")
		store.SetStorage(tx.From, recoveryGuardiansKey, guardiansRaw)
		store.SetStorage(tx.From, recoveryThresholdKey, strconv.FormatUint(threshold, 10))
		store.SetStorage(tx.From, recoveryDelayKey, strconv.FormatUint(delay, 10))
		clearPendingRecovery(store, tx.From)
		attributes["guardians"] = guardiansRaw
		attributes["threshold"] = strconv.FormatUint(threshold, 10)
		attributes["delay"] = strconv.FormatUint(delay, 10)
		return types.Event{Type: "account.recovery_configured", Attributes: attributes}, nil

	case "approve":
		newOwner, err := requiredRecoveryNewOwner(tx.Payload["new_owner"])
		if err != nil {
			return types.Event{}, err
		}
		if err := validateRecoveryNewOwner(store, tx.From, newOwner); err != nil {
			return types.Event{}, err
		}
		guardians, err := storedRecoveryGuardians(store, tx.From)
		if err != nil {
			return types.Event{}, err
		}
		threshold, err := storedRecoveryThreshold(store, tx.From, len(guardians))
		if err != nil {
			return types.Event{}, err
		}
		delay, err := storedRecoveryDelay(store, tx.From)
		if err != nil {
			return types.Event{}, err
		}

		pendingRaw := strings.TrimSpace(store.GetStorage(tx.From, recoveryPendingOwnerKey))
		expiresAtRaw := strings.TrimSpace(store.GetStorage(tx.From, recoveryExpiresAtKey))
		if pendingRaw != "" {
			if expiresAtRaw == "" {
				return types.Event{}, errors.New("recovery proposal expiry is not set")
			}
			expiresAt, err := parseStoredRecoveryExpiry(expiresAtRaw)
			if err != nil {
				return types.Event{}, err
			}
			if blockHeight > expiresAt {
				clearPendingRecovery(store, tx.From)
				pendingRaw = ""
				expiresAtRaw = ""
			}
		} else if expiresAtRaw != "" {
			expiresAt, err := parseStoredRecoveryExpiry(expiresAtRaw)
			if err != nil {
				return types.Event{}, err
			}
			if blockHeight > expiresAt {
				clearPendingRecovery(store, tx.From)
				expiresAtRaw = ""
			}
		}
		if pendingRaw != "" {
			pendingOwner, err := normalizeRecoveryAddress(pendingRaw, "recovery pending owner")
			if err != nil {
				return types.Event{}, err
			}
			if pendingOwner != newOwner {
				return types.Event{}, errors.New("recovery for another owner is already pending")
			}
			return types.Event{}, errors.New("recovery approval threshold already met")
		}

		votes, err := storedRecoveryVotes(store, tx.From, guardians)
		if err != nil {
			return types.Event{}, err
		}
		voteUpdated := false
		for index, vote := range votes {
			if vote.Guardian == signer {
				if vote.NewOwner == newOwner {
					return types.Event{}, errors.New("recovery approval already recorded")
				}
				if uint64(len(recoveryVotesForOwner(votes, newOwner))+1) < threshold {
					return types.Event{}, errors.New("recovery vote change must reach approval threshold")
				}
				votes[index].NewOwner = newOwner
				voteUpdated = true
				break
			}
		}
		if !voteUpdated {
			votes = append(votes, recoveryVote{Guardian: signer, NewOwner: newOwner})
		}
		store.SetStorage(tx.From, recoveryVotesKey, encodeRecoveryVotes(votes))
		approvals := recoveryVotesForOwner(votes, newOwner)

		executeAfterRaw := ""
		if uint64(len(approvals)) >= threshold {
			executeAfter, err := checkedAdd(blockHeight, delay)
			if err != nil {
				return types.Event{}, errors.New("recovery execute height overflow")
			}
			expiresAt, err := checkedAdd(executeAfter, recoveryProposalWindow)
			if err != nil {
				return types.Event{}, errors.New("recovery proposal expiry overflow")
			}
			executeAfterRaw = strconv.FormatUint(executeAfter, 10)
			expiresAtRaw = strconv.FormatUint(expiresAt, 10)
			store.SetStorage(tx.From, recoveryPendingOwnerKey, newOwner)
			store.SetStorage(tx.From, recoveryApprovalsKey, strings.Join(approvals, ","))
			store.SetStorage(tx.From, recoveryExecuteAfterKey, executeAfterRaw)
			store.SetStorage(tx.From, recoveryExpiresAtKey, expiresAtRaw)
			store.DeleteStorage(tx.From, recoveryVotesKey)
		} else {
			if strings.TrimSpace(store.GetStorage(tx.From, recoveryExecuteAfterKey)) != "" {
				return types.Event{}, errors.New("recovery execute height is set before approval threshold")
			}
			if expiresAtRaw == "" {
				expiresAt, err := checkedAdd(blockHeight, recoveryProposalWindow)
				if err != nil {
					return types.Event{}, errors.New("recovery proposal expiry overflow")
				}
				expiresAtRaw = strconv.FormatUint(expiresAt, 10)
				store.SetStorage(tx.From, recoveryExpiresAtKey, expiresAtRaw)
			}
		}
		attributes["guardian"] = signer
		attributes["new_owner"] = newOwner
		attributes["approvals"] = strconv.Itoa(len(approvals))
		attributes["threshold"] = strconv.FormatUint(threshold, 10)
		if voteUpdated {
			attributes["updated"] = "true"
		}
		if executeAfterRaw != "" {
			attributes["execute_after"] = executeAfterRaw
			attributes["pending"] = "true"
		}
		attributes["expires_at"] = expiresAtRaw
		return types.Event{Type: "account.recovery_approved", Attributes: attributes}, nil

	case "execute":
		newOwner, err := requiredRecoveryNewOwner(tx.Payload["new_owner"])
		if err != nil {
			return types.Event{}, err
		}
		if err := validateRecoveryNewOwner(store, tx.From, newOwner); err != nil {
			return types.Event{}, err
		}
		guardians, err := storedRecoveryGuardians(store, tx.From)
		if err != nil {
			return types.Event{}, err
		}
		threshold, err := storedRecoveryThreshold(store, tx.From, len(guardians))
		if err != nil {
			return types.Event{}, err
		}
		pendingRaw := strings.TrimSpace(store.GetStorage(tx.From, recoveryPendingOwnerKey))
		if pendingRaw == "" {
			votes, err := storedRecoveryVotes(store, tx.From, guardians)
			if err != nil {
				return types.Event{}, err
			}
			if len(votes) > 0 {
				expiresAtRaw := strings.TrimSpace(store.GetStorage(tx.From, recoveryExpiresAtKey))
				if expiresAtRaw == "" {
					return types.Event{}, errors.New("recovery voting round expiry is not set")
				}
				expiresAt, err := parseStoredRecoveryExpiry(expiresAtRaw)
				if err != nil {
					return types.Event{}, err
				}
				if blockHeight > expiresAt {
					return types.Event{}, errors.New("recovery voting round has expired")
				}
				return types.Event{}, errors.New("recovery approval threshold not met")
			}
			return types.Event{}, errors.New("recovery has no pending owner")
		}
		pendingOwner, err := normalizeRecoveryAddress(pendingRaw, "recovery pending owner")
		if err != nil {
			return types.Event{}, err
		}
		if newOwner != pendingOwner {
			return types.Event{}, errors.New("recovery new owner does not match pending owner")
		}
		approvals, err := storedRecoveryApprovals(store, tx.From, guardians)
		if err != nil {
			return types.Event{}, err
		}
		if uint64(len(approvals)) < threshold {
			return types.Event{}, errors.New("recovery approval threshold not met")
		}
		executeAfterRaw := strings.TrimSpace(store.GetStorage(tx.From, recoveryExecuteAfterKey))
		if executeAfterRaw == "" {
			return types.Event{}, errors.New("recovery execute height is not set")
		}
		executeAfter, err := parseStoredRecoveryHeight(executeAfterRaw)
		if err != nil {
			return types.Event{}, err
		}
		expiresAtRaw := strings.TrimSpace(store.GetStorage(tx.From, recoveryExpiresAtKey))
		if expiresAtRaw == "" {
			return types.Event{}, errors.New("recovery proposal expiry is not set")
		}
		expiresAt, err := parseStoredRecoveryExpiry(expiresAtRaw)
		if err != nil {
			return types.Event{}, err
		}
		if blockHeight > expiresAt {
			return types.Event{}, errors.New("recovery proposal has expired")
		}
		if blockHeight < executeAfter {
			return types.Event{}, errors.New("recovery delay has not elapsed")
		}
		epoch, err := sessionEpoch(store, tx.From)
		if err != nil {
			return types.Event{}, err
		}
		if epoch == math.MaxUint64 {
			return types.Event{}, errors.New("session epoch overflow")
		}
		oldOwner := strings.ToLower(strings.TrimSpace(store.GetStorage(tx.From, "owner")))
		store.SetStorage(tx.From, "owner", pendingOwner)
		store.SetStorage(tx.From, sessionEpochKey, strconv.FormatUint(epoch+1, 10))
		clearPendingRecovery(store, tx.From)
		attributes["guardian"] = signer
		attributes["old_owner"] = oldOwner
		attributes["new_owner"] = pendingOwner
		attributes["approvals"] = strconv.Itoa(len(approvals))
		attributes["session_epoch"] = strconv.FormatUint(epoch+1, 10)
		return types.Event{Type: "account.recovery_executed", Attributes: attributes}, nil

	case "cancel":
		pendingOwner := strings.ToLower(strings.TrimSpace(store.GetStorage(tx.From, recoveryPendingOwnerKey)))
		clearPendingRecovery(store, tx.From)
		if pendingOwner != "" {
			attributes["new_owner"] = pendingOwner
		}
		return types.Event{Type: "account.recovery_cancelled", Attributes: attributes}, nil

	case "clear":
		store.DeleteStoragePrefix(tx.From, recoveryStoragePrefix)
		return types.Event{Type: "account.recovery_cleared", Attributes: attributes}, nil
	}
	return types.Event{}, errors.New("unreachable account recovery action")
}

func accountRecoveryAction(payload map[string]string) (string, error) {
	action := strings.ToLower(strings.TrimSpace(payload["action"]))
	switch action {
	case "configure", "approve", "execute", "cancel", "clear":
		return action, nil
	default:
		return "", errors.New("account recovery action must be configure, approve, execute, cancel, or clear")
	}
}

func parseRecoveryGuardians(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("recovery guardians are required")
	}
	if len(raw) > maxRecoveryGuardianListLen {
		return nil, fmt.Errorf("recovery guardian count exceeds %d", maxRecoveryGuardians)
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxRecoveryGuardians {
		return nil, fmt.Errorf("recovery guardian count exceeds %d", maxRecoveryGuardians)
	}
	guardians := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		guardian, err := normalizeRecoveryAddress(part, "recovery guardian")
		if err != nil {
			return nil, err
		}
		if _, ok := seen[guardian]; ok {
			return nil, errors.New("recovery guardians must be unique")
		}
		seen[guardian] = struct{}{}
		guardians = append(guardians, guardian)
	}
	return guardians, nil
}

func storedRecoveryGuardians(store *state.Store, account string) ([]string, error) {
	raw := strings.TrimSpace(store.GetStorage(account, recoveryGuardiansKey))
	if raw == "" {
		return nil, errors.New("account recovery guardian action requires configured guardian")
	}
	return parseRecoveryGuardians(raw)
}

func parseRecoveryThreshold(raw string, guardianCount int) (uint64, error) {
	threshold, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || threshold == 0 {
		return 0, errors.New("recovery threshold must be positive")
	}
	if threshold > uint64(guardianCount) {
		return 0, errors.New("recovery threshold exceeds guardian count")
	}
	return threshold, nil
}

func storedRecoveryThreshold(store *state.Store, account string, guardianCount int) (uint64, error) {
	return parseRecoveryThreshold(store.GetStorage(account, recoveryThresholdKey), guardianCount)
}

func parseRecoveryDelay(raw string) (uint64, error) {
	delay, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, errors.New("recovery delay must be an unsigned integer")
	}
	return delay, nil
}

func storedRecoveryDelay(store *state.Store, account string) (uint64, error) {
	raw := strings.TrimSpace(store.GetStorage(account, recoveryDelayKey))
	if raw == "" {
		return 0, errors.New("recovery delay is not configured")
	}
	return parseRecoveryDelay(raw)
}

func requiredRecoveryNewOwner(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("recovery new owner is required")
	}
	return normalizeRecoveryAddress(raw, "recovery new owner")
}

func validateRecoveryNewOwner(store *state.Store, account string, newOwner string) error {
	if strings.EqualFold(strings.TrimSpace(account), newOwner) {
		return errors.New("recovery new owner must differ from recovered account")
	}
	currentOwner := strings.ToLower(strings.TrimSpace(store.GetStorage(account, "owner")))
	if currentOwner != "" && currentOwner == newOwner {
		return errors.New("recovery new owner must differ from current owner")
	}
	if strings.TrimSpace(store.GetAccount(newOwner).CodeID) != "" {
		return errors.New("recovery new owner must be an EOA")
	}
	return nil
}

func validateRecoveryGuardians(store *state.Store, account string, guardians []string) error {
	account = strings.ToLower(strings.TrimSpace(account))
	owner := strings.ToLower(strings.TrimSpace(store.GetStorage(account, "owner")))
	for _, guardian := range guardians {
		if guardian == account {
			return errors.New("recovery guardian must differ from recovered account")
		}
		if owner != "" && guardian == owner {
			return errors.New("recovery guardian must differ from current owner")
		}
		if strings.TrimSpace(store.GetAccount(guardian).CodeID) != "" {
			return errors.New("recovery guardian must be an EOA")
		}
	}
	return nil
}

func normalizeRecoveryAddress(raw string, field string) (string, error) {
	address, err := chaincrypto.NormalizeAddress(raw)
	if errors.Is(err, chaincrypto.ErrInvalidAddress) {
		return "", fmt.Errorf("%s must be a 20-byte hex address", field)
	}
	if errors.Is(err, chaincrypto.ErrZeroAddress) {
		return "", fmt.Errorf("%s must not be the zero address", field)
	}
	if err != nil {
		return "", err
	}
	return address, nil
}

func storedRecoveryApprovals(store *state.Store, account string, guardians []string) ([]string, error) {
	raw := strings.TrimSpace(store.GetStorage(account, recoveryApprovalsKey))
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxRecoveryGuardianListLen {
		return nil, errors.New("recovery approvals exceed configured guardian limit")
	}
	parts := strings.Split(raw, ",")
	if len(parts) > len(guardians) {
		return nil, errors.New("recovery approvals exceed configured guardian count")
	}
	approvals := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		approval, err := normalizeRecoveryAddress(part, "recovery approval")
		if err != nil {
			return nil, err
		}
		if !containsAddress(guardians, approval) {
			return nil, errors.New("recovery approval is not from a configured guardian")
		}
		if _, ok := seen[approval]; ok {
			return nil, errors.New("recovery approvals must be unique")
		}
		seen[approval] = struct{}{}
		approvals = append(approvals, approval)
	}
	return approvals, nil
}

type recoveryVote struct {
	Guardian string
	NewOwner string
}

func storedRecoveryVotes(store *state.Store, account string, guardians []string) ([]recoveryVote, error) {
	raw := strings.TrimSpace(store.GetStorage(account, recoveryVotesKey))
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxRecoveryVoteListLen {
		return nil, errors.New("recovery votes exceed configured guardian limit")
	}
	parts := strings.Split(raw, ",")
	if len(parts) > len(guardians) {
		return nil, errors.New("recovery votes exceed configured guardian count")
	}
	votes := make([]recoveryVote, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		fields := strings.SplitN(part, "=", 2)
		if len(fields) != 2 {
			return nil, errors.New("recovery vote is invalid")
		}
		guardian, err := normalizeRecoveryAddress(fields[0], "recovery vote guardian")
		if err != nil {
			return nil, err
		}
		if !containsAddress(guardians, guardian) {
			return nil, errors.New("recovery vote is not from a configured guardian")
		}
		if _, ok := seen[guardian]; ok {
			return nil, errors.New("recovery guardian votes must be unique")
		}
		newOwner, err := normalizeRecoveryAddress(fields[1], "recovery vote owner")
		if err != nil {
			return nil, err
		}
		seen[guardian] = struct{}{}
		votes = append(votes, recoveryVote{Guardian: guardian, NewOwner: newOwner})
	}
	return votes, nil
}

func encodeRecoveryVotes(votes []recoveryVote) string {
	encoded := make([]string, len(votes))
	for i, vote := range votes {
		encoded[i] = vote.Guardian + "=" + vote.NewOwner
	}
	return strings.Join(encoded, ",")
}

func recoveryVotesForOwner(votes []recoveryVote, newOwner string) []string {
	approvals := make([]string, 0, len(votes))
	for _, vote := range votes {
		if vote.NewOwner == newOwner {
			approvals = append(approvals, vote.Guardian)
		}
	}
	return approvals
}

func parseStoredRecoveryHeight(raw string) (uint64, error) {
	height, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, errors.New("recovery execute height is invalid")
	}
	return height, nil
}

func parseStoredRecoveryExpiry(raw string) (uint64, error) {
	height, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, errors.New("recovery proposal expiry is invalid")
	}
	return height, nil
}

func clearPendingRecovery(store *state.Store, account string) {
	store.DeleteStorage(account, recoveryPendingOwnerKey)
	store.DeleteStorage(account, recoveryExecuteAfterKey)
	store.DeleteStorage(account, recoveryExpiresAtKey)
	store.DeleteStorage(account, recoveryApprovalsKey)
	store.DeleteStorage(account, recoveryVotesKey)
}

func containsAddress(addresses []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	for _, address := range addresses {
		if address == target {
			return true
		}
	}
	return false
}
