package abci

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"chainlab/internal/hash"
	"chainlab/internal/state"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
)

type evidenceRecord struct {
	Type             int32  `json:"type"`
	Validator        string `json:"validator"`
	Power            int64  `json:"power"`
	Height           int64  `json:"height"`
	TimeUnix         int64  `json:"time_unix"`
	TimeNanosecond   int    `json:"time_nanosecond"`
	TotalVotingPower int64  `json:"total_voting_power"`
	ConsensusAddress string `json:"consensus_address,omitempty"`
}

type evidenceOutcome struct {
	record           evidenceRecord
	newOffence       bool
	slashBasisPoints uint32
	slashAmount      uint64
	removalHeight    int64
}

func validateEvidence(input []abcitypes.Misbehavior, height int64, validators map[string]string) ([]evidenceRecord, error) {
	if len(input) > len(validators) {
		return nil, errors.New("misbehavior count exceeds the fixed validator set")
	}
	records := make([]evidenceRecord, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for index, item := range input {
		if item.Type != abcitypes.MisbehaviorType_DUPLICATE_VOTE && item.Type != abcitypes.MisbehaviorType_LIGHT_CLIENT_ATTACK {
			return nil, fmt.Errorf("misbehavior %d has an unsupported type", index)
		}
		if len(item.Validator.Address) != 20 {
			return nil, fmt.Errorf("misbehavior %d has an invalid validator address", index)
		}
		validatorKey := hex.EncodeToString(item.Validator.Address)
		validator, exists := validators[validatorKey]
		if !exists {
			return nil, fmt.Errorf("misbehavior %d validator is not in the fixed validator set", index)
		}
		if item.Validator.Power != 1 || item.TotalVotingPower != int64(len(validators)) {
			return nil, fmt.Errorf("misbehavior %d voting power does not match the fixed equal-power set", index)
		}
		if item.Height <= 0 || item.Height >= height {
			return nil, fmt.Errorf("misbehavior %d height is outside committed history", index)
		}
		if item.Time.IsZero() {
			return nil, fmt.Errorf("misbehavior %d time is required", index)
		}
		identity := fmt.Sprintf("%d:%s:%d", item.Type, validator, item.Height)
		if _, duplicate := seen[identity]; duplicate {
			return nil, fmt.Errorf("misbehavior %d duplicates an earlier offence", index)
		}
		seen[identity] = struct{}{}
		records = append(records, evidenceRecord{
			Type:             int32(item.Type),
			Validator:        validator,
			Power:            item.Validator.Power,
			Height:           item.Height,
			TimeUnix:         item.Time.Unix(),
			TimeNanosecond:   item.Time.Nanosecond(),
			TotalVotingPower: item.TotalVotingPower,
		})
	}
	sort.Slice(records, func(left int, right int) bool {
		if records[left].Height != records[right].Height {
			return records[left].Height < records[right].Height
		}
		if records[left].Validator != records[right].Validator {
			return records[left].Validator < records[right].Validator
		}
		return records[left].Type < records[right].Type
	})
	return records, nil
}

func evidenceRoot(records []evidenceRecord) string {
	if records == nil {
		records = []evidenceRecord{}
	}
	return hash.MustHex(records)
}

func evidenceEvents(records []evidenceRecord) []abcitypes.Event {
	events := make([]abcitypes.Event, 0, len(records))
	for _, record := range records {
		events = append(events, abcitypes.Event{
			Type: "chainlab.misbehavior",
			Attributes: []abcitypes.EventAttribute{
				{Key: "height", Value: fmt.Sprintf("%d", record.Height), Index: true},
				{Key: "type", Value: fmt.Sprintf("%d", record.Type), Index: true},
				{Key: "validator", Value: record.Validator, Index: true},
			},
		})
	}
	return events
}

func validateEvidenceV2(
	input []abcitypes.Misbehavior,
	height int64,
	blockTime time.Time,
	lifecycle state.ValidatorLifecycle,
	policy ValidatorPolicy,
) ([]evidenceRecord, error) {
	if blockTime.IsZero() {
		return nil, errors.New("protocol version 2 requires a block time")
	}
	if len(input) > len(lifecycle.Validators) {
		return nil, errors.New("misbehavior count exceeds the validator identity set")
	}
	records := make([]evidenceRecord, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for index, item := range input {
		if item.Type != abcitypes.MisbehaviorType_DUPLICATE_VOTE && item.Type != abcitypes.MisbehaviorType_LIGHT_CLIENT_ATTACK {
			return nil, fmt.Errorf("misbehavior %d has an unsupported type", index)
		}
		if len(item.Validator.Address) != 20 {
			return nil, fmt.Errorf("misbehavior %d has an invalid validator address", index)
		}
		consensusAddress := hex.EncodeToString(item.Validator.Address)
		identity, exists := lifecycle.Validators[consensusAddress]
		if !exists {
			return nil, fmt.Errorf("misbehavior %d validator identity is unknown", index)
		}
		if item.Height <= 0 || item.Height >= height {
			return nil, fmt.Errorf("misbehavior %d height is outside committed history", index)
		}
		if !validatorIdentityActive(identity, item.Height) {
			return nil, fmt.Errorf("misbehavior %d validator was not active at the offence height", index)
		}
		totalVotingPower, err := validatorVotingPowerAtHeight(lifecycle, item.Height)
		if err != nil {
			return nil, err
		}
		if item.Validator.Power != identity.Power || item.TotalVotingPower != totalVotingPower {
			return nil, fmt.Errorf("misbehavior %d voting power does not match the historical validator set", index)
		}
		if item.Time.IsZero() || item.Time.After(blockTime) {
			return nil, fmt.Errorf("misbehavior %d time is invalid", index)
		}
		ageBlocks := height - item.Height
		ageDuration := blockTime.Sub(item.Time)
		if ageBlocks > policy.EvidenceMaxAgeNumBlocks && ageDuration > time.Duration(policy.EvidenceMaxAgeDurationNanos) {
			return nil, fmt.Errorf("misbehavior %d is older than the protocol retention window", index)
		}
		identityKey := state.ValidatorOffenceKey(identity.Account, item.Height)
		if _, duplicate := seen[identityKey]; duplicate {
			return nil, fmt.Errorf("misbehavior %d duplicates an earlier validator-height offence", index)
		}
		seen[identityKey] = struct{}{}
		records = append(records, evidenceRecord{
			Type: int32(item.Type), Validator: identity.Account, ConsensusAddress: consensusAddress,
			Power: item.Validator.Power, Height: item.Height, TimeUnix: item.Time.Unix(),
			TimeNanosecond: item.Time.Nanosecond(), TotalVotingPower: item.TotalVotingPower,
		})
	}
	sortEvidenceRecords(records)
	return records, nil
}

func applyEvidenceV2(
	store *state.Store,
	records []evidenceRecord,
	height int64,
	policy ValidatorPolicy,
) ([]evidenceOutcome, []abcitypes.ValidatorUpdate, error) {
	lifecycle, exists := store.ValidatorLifecycle()
	if !exists {
		return nil, nil, errors.New("protocol version 2 validator lifecycle is missing")
	}
	outcomes := make([]evidenceOutcome, 0, len(records))
	for _, record := range records {
		key := state.ValidatorOffenceKey(record.Validator, record.Height)
		if prior, consumed := lifecycle.Offences[key]; consumed {
			outcomes = append(outcomes, evidenceOutcome{
				record: record, slashBasisPoints: prior.SlashBasisPoints,
				removalHeight: prior.RemovalHeight,
			})
			continue
		}
		if len(lifecycle.Offences) >= state.MaxValidatorOffences {
			return nil, nil, fmt.Errorf("validator lifecycle exceeds %d offences", state.MaxValidatorOffences)
		}
		slashBasisPoints, err := evidenceSlashBasisPoints(record.Type, policy)
		if err != nil {
			return nil, nil, err
		}
		stake := store.StakeOf(record.Validator)
		slashAmount := slashAmountCeiling(stake, slashBasisPoints)
		if slashAmount > 0 {
			if err := store.SubStake(record.Validator, slashAmount); err != nil {
				return nil, nil, err
			}
		}
		removalHeight, err := nextEpochRemovalHeight(height, policy.EpochLength)
		if err != nil {
			return nil, nil, err
		}
		identity := lifecycle.Validators[record.ConsensusAddress]
		if identity.InactiveHeight == 0 || removalHeight < identity.InactiveHeight {
			identity.InactiveHeight = removalHeight
			lifecycle.Validators[record.ConsensusAddress] = identity
		} else {
			removalHeight = identity.InactiveHeight
		}
		lifecycle.Offences[key] = state.ValidatorOffence{
			Validator: record.Validator, Height: record.Height, Type: record.Type,
			ObservedHeight: height, SlashBasisPoints: slashBasisPoints,
			SlashAmount: slashAmount, RemovalHeight: removalHeight,
		}
		outcomes = append(outcomes, evidenceOutcome{
			record: record, newOffence: true, slashBasisPoints: slashBasisPoints,
			slashAmount: slashAmount, removalHeight: removalHeight,
		})
	}
	if err := validateNonEmptyScheduledValidatorSets(lifecycle); err != nil {
		return nil, nil, err
	}
	if err := store.SetValidatorLifecycle(lifecycle); err != nil {
		return nil, nil, err
	}
	updates, err := validatorUpdatesAtHeight(lifecycle, height)
	if err != nil {
		return nil, nil, err
	}
	return outcomes, updates, nil
}

func sortEvidenceRecords(records []evidenceRecord) {
	sort.Slice(records, func(left int, right int) bool {
		if records[left].Height != records[right].Height {
			return records[left].Height < records[right].Height
		}
		if records[left].Validator != records[right].Validator {
			return records[left].Validator < records[right].Validator
		}
		return records[left].Type < records[right].Type
	})
}

func validatorIdentityActive(identity state.ValidatorIdentity, height int64) bool {
	return height >= identity.ActiveHeight && (identity.InactiveHeight == 0 || height < identity.InactiveHeight)
}

func validatorVotingPowerAtHeight(lifecycle state.ValidatorLifecycle, height int64) (int64, error) {
	var total int64
	for _, identity := range lifecycle.Validators {
		if !validatorIdentityActive(identity, height) {
			continue
		}
		if identity.Power <= 0 || total > math.MaxInt64-identity.Power {
			return 0, errors.New("historical validator voting power is invalid")
		}
		total += identity.Power
	}
	if total <= 0 {
		return 0, errors.New("historical validator set is empty")
	}
	return total, nil
}

func evidenceSlashBasisPoints(evidenceType int32, policy ValidatorPolicy) (uint32, error) {
	switch abcitypes.MisbehaviorType(evidenceType) {
	case abcitypes.MisbehaviorType_DUPLICATE_VOTE:
		return policy.DuplicateVoteSlashBasisPoints, nil
	case abcitypes.MisbehaviorType_LIGHT_CLIENT_ATTACK:
		return policy.LightClientAttackSlashBasisPoints, nil
	default:
		return 0, errors.New("unsupported evidence type")
	}
}

func slashAmountCeiling(stake uint64, basisPoints uint32) uint64 {
	if stake == 0 || basisPoints == 0 {
		return 0
	}
	whole := (stake / 10_000) * uint64(basisPoints)
	remainderProduct := (stake % 10_000) * uint64(basisPoints)
	partial := remainderProduct / 10_000
	if remainderProduct%10_000 != 0 {
		partial++
	}
	return whole + partial
}

func nextEpochRemovalHeight(observedHeight int64, epochLength int64) (int64, error) {
	if observedHeight <= 0 || observedHeight > math.MaxInt64-2 || epochLength < 2 {
		return 0, errors.New("cannot schedule validator removal outside the height domain")
	}
	minimum := observedHeight + 2
	offset := minimum - 1
	epoch := offset / epochLength
	if offset%epochLength != 0 {
		epoch++
	}
	if epoch > (math.MaxInt64-1)/epochLength {
		return 0, errors.New("validator removal height overflows")
	}
	return epoch*epochLength + 1, nil
}

func validateNonEmptyScheduledValidatorSets(lifecycle state.ValidatorLifecycle) error {
	heights := make(map[int64]struct{})
	for _, identity := range lifecycle.Validators {
		if identity.InactiveHeight > 0 {
			heights[identity.InactiveHeight] = struct{}{}
		}
	}
	for height := range heights {
		if _, err := validatorVotingPowerAtHeight(lifecycle, height); err != nil {
			return fmt.Errorf("validator update at height %d would empty the validator set", height)
		}
	}
	return nil
}

func validatorUpdatesAtHeight(lifecycle state.ValidatorLifecycle, height int64) ([]abcitypes.ValidatorUpdate, error) {
	identities := make([]state.ValidatorIdentity, 0)
	for _, identity := range lifecycle.Validators {
		if identity.InactiveHeight == height+2 {
			identities = append(identities, identity)
		}
	}
	sort.Slice(identities, func(left int, right int) bool {
		return identities[left].ConsensusAddress < identities[right].ConsensusAddress
	})
	updates := make([]abcitypes.ValidatorUpdate, 0, len(identities))
	for _, identity := range identities {
		publicKey, err := hex.DecodeString(identity.PublicKey)
		if err != nil || len(publicKey) != 33 {
			return nil, errors.New("validator lifecycle contains an invalid public key")
		}
		updates = append(updates, abcitypes.UpdateValidator(publicKey, 0, cmtsecp256k1.KeyType))
	}
	return updates, nil
}

func validatorEpoch(height int64, epochLength int64) uint64 {
	if height <= 0 || epochLength <= 0 {
		return 0
	}
	return uint64((height - 1) / epochLength)
}

func evidenceOutcomeEvents(outcomes []evidenceOutcome) []abcitypes.Event {
	events := make([]abcitypes.Event, 0, len(outcomes))
	for _, outcome := range outcomes {
		events = append(events, abcitypes.Event{
			Type: "chainlab.validator_slash",
			Attributes: []abcitypes.EventAttribute{
				{Key: "height", Value: strconv.FormatInt(outcome.record.Height, 10), Index: true},
				{Key: "validator", Value: outcome.record.Validator, Index: true},
				{Key: "type", Value: strconv.FormatInt(int64(outcome.record.Type), 10), Index: true},
				{Key: "new_offence", Value: strconv.FormatBool(outcome.newOffence), Index: true},
				{Key: "slash_basis_points", Value: strconv.FormatUint(uint64(outcome.slashBasisPoints), 10)},
				{Key: "slash_amount", Value: strconv.FormatUint(outcome.slashAmount, 10)},
				{Key: "removal_height", Value: strconv.FormatInt(outcome.removalHeight, 10), Index: true},
			},
		})
	}
	return events
}
