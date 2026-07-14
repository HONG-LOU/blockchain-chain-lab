package cometnode

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	cmtjson "github.com/cometbft/cometbft/libs/json"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/privval"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

func TestDurablePrivValidatorPersistsEverySigningStepAndReusesSignature(t *testing.T) {
	root := filepath.Join(t.TempDir(), "network")
	network, err := InitializeNetwork(NetworkConfig{
		OutputRoot: root, ChainID: "chainlab-durable-validator", ValidatorCount: 1,
		ABCIBasePort: 26658, RPCBasePort: 26670, P2PBasePort: 26680,
	})
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, network.Nodes[0].Home)
	document, err := LoadNodeDocument(home)
	if err != nil {
		t.Fatal(err)
	}
	config, err := BuildConfig(home, document)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := loadDurablePrivValidator(config.PrivValidatorKeyFile(), config.PrivValidatorStateFile())
	if err != nil {
		t.Fatal(err)
	}
	blockID := cmtproto.BlockID{
		Hash:          bytes.Repeat([]byte{1}, 32),
		PartSetHeader: cmtproto.PartSetHeader{Total: 1, Hash: bytes.Repeat([]byte{2}, 32)},
	}
	proposal := &cmtproto.Proposal{
		Type: cmtproto.ProposalType, Height: 1, Round: 0, PolRound: -1,
		BlockID: blockID, Timestamp: time.Now().UTC(),
	}
	if err := validator.SignProposal(document.ChainID, proposal); err != nil {
		t.Fatal(err)
	}
	assertPersistedSignStep(t, config.PrivValidatorStateFile(), validatorStepPropose)

	prevote := &cmtproto.Vote{
		Type: cmtproto.PrevoteType, Height: 1, Round: 0,
		BlockID: blockID, Timestamp: time.Now().UTC(),
	}
	if err := validator.SignVote(document.ChainID, prevote); err != nil {
		t.Fatal(err)
	}
	assertPersistedSignStep(t, config.PrivValidatorStateFile(), validatorStepPrevote)

	precommit := &cmtproto.Vote{
		Type: cmtproto.PrecommitType, Height: 1, Round: 0,
		BlockID: blockID, Timestamp: time.Now().UTC(),
	}
	if err := validator.SignVote(document.ChainID, precommit); err != nil {
		t.Fatal(err)
	}
	assertPersistedSignStep(t, config.PrivValidatorStateFile(), validatorStepPrecommit)
	signature := append([]byte(nil), precommit.Signature...)
	timestamp := precommit.Timestamp

	reloaded, err := loadDurablePrivValidator(config.PrivValidatorKeyFile(), config.PrivValidatorStateFile())
	if err != nil {
		t.Fatal(err)
	}
	repeated := &cmtproto.Vote{
		Type: cmtproto.PrecommitType, Height: 1, Round: 0,
		BlockID: blockID, Timestamp: timestamp.Add(time.Second),
	}
	if err := reloaded.SignVote(document.ChainID, repeated); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(repeated.Signature, signature) || !repeated.Timestamp.Equal(timestamp) {
		t.Fatalf("repeated signature/timestamp changed: signature_equal=%t timestamp=%s want=%s", bytes.Equal(repeated.Signature, signature), repeated.Timestamp, timestamp)
	}
}

func TestDurablePrivValidatorDoesNotAdvanceMemoryWhenPersistenceFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "network")
	network, err := InitializeNetwork(NetworkConfig{
		OutputRoot: root, ChainID: "chainlab-validator-persist-failure", ValidatorCount: 1,
		ABCIBasePort: 26658, RPCBasePort: 26670, P2PBasePort: 26680,
	})
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, network.Nodes[0].Home)
	document, err := LoadNodeDocument(home)
	if err != nil {
		t.Fatal(err)
	}
	config, err := BuildConfig(home, document)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := loadDurablePrivValidator(config.PrivValidatorKeyFile(), config.PrivValidatorStateFile())
	if err != nil {
		t.Fatal(err)
	}
	validator.statePath = filepath.Join(t.TempDir(), "missing", "state.json")
	vote := &cmtproto.Vote{Type: cmtproto.PrevoteType, Height: 1, Round: 0, Timestamp: time.Now().UTC()}
	if err := validator.SignVote(document.ChainID, vote); err == nil {
		t.Fatal("expected private validator persistence failure")
	}
	if validator.state.Height != 0 || validator.state.Round != 0 || validator.state.Step != 0 || len(vote.Signature) != 0 {
		t.Fatalf("failed persistence advanced state: state=%+v signature=%X", validator.state, vote.Signature)
	}
}

func TestRenameSignStateRetriesOnlyTransientFailures(t *testing.T) {
	transient := errors.New("transient sharing violation")
	calls := 0
	sleeps := 0
	err := renameSignStateWithRetry("source", "destination", func(string, string) error {
		calls++
		if calls < 4 {
			return transient
		}
		return nil
	}, func(err error) bool {
		return errors.Is(err, transient)
	}, func(time.Duration) {
		sleeps++
	})
	if err != nil || calls != 4 || sleeps != 3 {
		t.Fatalf("retry result err=%v calls=%d sleeps=%d", err, calls, sleeps)
	}

	permanent := errors.New("permanent")
	calls = 0
	err = renameSignStateWithRetry("source", "destination", func(string, string) error {
		calls++
		return permanent
	}, func(error) bool { return false }, func(time.Duration) { t.Fatal("slept for permanent error") })
	if !errors.Is(err, permanent) || calls != 1 {
		t.Fatalf("permanent result err=%v calls=%d", err, calls)
	}
}

func TestConsensusFailureLoggerReportsDerivedLoggerPanic(t *testing.T) {
	for _, message := range []string{consensusFailureMessage, voteSigningFailureMessage, proposalSigningFailureMessage} {
		t.Run(message, func(t *testing.T) {
			logger, failures := newConsensusFailureLogger(testNopLogger{})
			injected := errors.New("injected failure")
			if message != consensusFailureMessage {
				injected = fmt.Errorf("%w: injected failure", errValidatorStatePersistence)
			}
			logger.With("module", "consensus").Error(message, "err", injected)
			select {
			case err := <-failures:
				if err == nil || !bytes.Contains([]byte(err.Error()), []byte("injected failure")) {
					t.Fatalf("consensus failure = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("consensus failure was not reported")
			}
		})
	}
	logger, failures := newConsensusFailureLogger(testNopLogger{})
	logger.Error(voteSigningFailureMessage, "err", errors.New("step regression"))
	select {
	case err := <-failures:
		t.Fatalf("ordinary error reported as fatal: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestRenameSignStateStopsAfterBoundedTransientFailures(t *testing.T) {
	transient := errors.New("persistent sharing violation")
	calls := 0
	err := renameSignStateWithRetry("source", "destination", func(string, string) error {
		calls++
		return transient
	}, func(error) bool { return true }, func(time.Duration) {})
	if !errors.Is(err, transient) || calls != signStateRenameAttempts {
		t.Fatalf("bounded retry err=%v calls=%d want=%d", err, calls, signStateRenameAttempts)
	}
}

type testNopLogger struct{}

func (testNopLogger) Debug(string, ...any) {}
func (testNopLogger) Info(string, ...any)  {}
func (testNopLogger) Warn(string, ...any)  {}
func (testNopLogger) Error(string, ...any) {}
func (logger testNopLogger) With(...any) cmtlog.Logger {
	return logger
}

func assertPersistedSignStep(t *testing.T, path string, step int8) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state privval.FilePVLastSignState
	if err := cmtjson.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.Height != 1 || state.Round != 0 || state.Step != step || len(state.Signature) == 0 || len(state.SignBytes) == 0 {
		t.Fatalf("persisted sign state = %+v, want step %d", state, step)
	}
}
