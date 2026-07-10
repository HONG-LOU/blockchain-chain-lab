package abci

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	abcitypes "github.com/cometbft/cometbft/abci/types"
)

func TestEvidenceRootIsOrderIndependentAndStrict(t *testing.T) {
	firstAddress := []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	secondAddress := []byte{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2}
	validators := map[string]string{
		hex.EncodeToString(firstAddress):  "0x1111111111111111111111111111111111111111",
		hex.EncodeToString(secondAddress): "0x2222222222222222222222222222222222222222",
	}
	first := abcitypes.Misbehavior{
		Type:      abcitypes.MisbehaviorType_DUPLICATE_VOTE,
		Validator: abcitypes.Validator{Address: firstAddress, Power: 1},
		Height:    2, Time: time.Unix(2, 3).UTC(), TotalVotingPower: 2,
	}
	second := abcitypes.Misbehavior{
		Type:      abcitypes.MisbehaviorType_LIGHT_CLIENT_ATTACK,
		Validator: abcitypes.Validator{Address: secondAddress, Power: 1},
		Height:    3, Time: time.Unix(3, 4).UTC(), TotalVotingPower: 2,
	}
	forward, err := validateEvidence([]abcitypes.Misbehavior{first, second}, 4, validators)
	if err != nil {
		t.Fatal(err)
	}
	reverse, err := validateEvidence([]abcitypes.Misbehavior{second, first}, 4, validators)
	if err != nil {
		t.Fatal(err)
	}
	if evidenceRoot(forward) != evidenceRoot(reverse) {
		t.Fatalf("evidence roots differ: %s != %s", evidenceRoot(forward), evidenceRoot(reverse))
	}
	if len(evidenceEvents(forward)) != 2 {
		t.Fatalf("evidence event count = %d", len(evidenceEvents(forward)))
	}

	duplicate := first
	duplicate.Time = time.Unix(9, 0).UTC()
	if _, err := validateEvidence([]abcitypes.Misbehavior{first, duplicate}, 4, validators); err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("duplicate evidence error = %v", err)
	}
	badPower := first
	badPower.TotalVotingPower = 3
	if _, err := validateEvidence([]abcitypes.Misbehavior{badPower}, 4, validators); err == nil || !strings.Contains(err.Error(), "voting power") {
		t.Fatalf("bad power error = %v", err)
	}
}
