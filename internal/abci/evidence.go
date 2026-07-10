package abci

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"chainlab/internal/hash"

	abcitypes "github.com/cometbft/cometbft/abci/types"
)

type evidenceRecord struct {
	Type             int32  `json:"type"`
	Validator        string `json:"validator"`
	Power            int64  `json:"power"`
	Height           int64  `json:"height"`
	TimeUnix         int64  `json:"time_unix"`
	TimeNanosecond   int    `json:"time_nanosecond"`
	TotalVotingPower int64  `json:"total_voting_power"`
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
