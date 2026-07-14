package cometnode

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	"github.com/cosmos/gogoproto/proto"

	cmtcrypto "github.com/cometbft/cometbft/crypto"
	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	"github.com/cometbft/cometbft/libs/protoio"
	"github.com/cometbft/cometbft/privval"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
	cmttime "github.com/cometbft/cometbft/types/time"
)

const (
	validatorStepPropose   int8 = 1
	validatorStepPrevote   int8 = 2
	validatorStepPrecommit int8 = 3
)

var errValidatorStatePersistence = errors.New("private validator state persistence failed")

// durablePrivValidator preserves CometBFT's on-disk format and signing rules,
// while using ChainLab's retrying atomic replacement for mutable sign state.
type durablePrivValidator struct {
	mu        sync.Mutex
	key       privval.FilePVKey
	state     privval.FilePVLastSignState
	statePath string
}

func loadDurablePrivValidator(keyPath, statePath string) (*durablePrivValidator, error) {
	keyRaw, err := readRegularFile(keyPath, maxPrivateKeyBytes, "private validator key")
	if err != nil {
		return nil, err
	}
	var key privval.FilePVKey
	if err := cmtjson.Unmarshal(keyRaw, &key); err != nil {
		return nil, fmt.Errorf("decode private validator key: %w", err)
	}
	if key.PrivKey == nil {
		return nil, errors.New("private validator key is missing")
	}
	key.PubKey = key.PrivKey.PubKey()
	key.Address = key.PubKey.Address()

	stateRaw, err := readRegularFile(statePath, maxLastSignStateBytes, "private validator state")
	if err != nil {
		return nil, err
	}
	var state privval.FilePVLastSignState
	if err := cmtjson.Unmarshal(stateRaw, &state); err != nil {
		return nil, fmt.Errorf("decode private validator state: %w", err)
	}
	if state.Height < 0 || state.Round < 0 || state.Step < 0 || state.Step > validatorStepPrecommit {
		return nil, errors.New("private validator state has an invalid height/round/step")
	}
	return &durablePrivValidator{key: key, state: state, statePath: statePath}, nil
}

func (validator *durablePrivValidator) GetPubKey() (cmtcrypto.PubKey, error) {
	return validator.key.PubKey, nil
}

func (validator *durablePrivValidator) SignVote(chainID string, vote *cmtproto.Vote) error {
	validator.mu.Lock()
	defer validator.mu.Unlock()
	if vote == nil {
		return errors.New("error signing vote: vote is required")
	}
	if err := validator.signVote(chainID, vote); err != nil {
		return fmt.Errorf("error signing vote: %w", err)
	}
	return nil
}

func (validator *durablePrivValidator) SignProposal(chainID string, proposal *cmtproto.Proposal) error {
	validator.mu.Lock()
	defer validator.mu.Unlock()
	if proposal == nil {
		return errors.New("error signing proposal: proposal is required")
	}
	if err := validator.signProposal(chainID, proposal); err != nil {
		return fmt.Errorf("error signing proposal: %w", err)
	}
	return nil
}

func (validator *durablePrivValidator) signVote(chainID string, vote *cmtproto.Vote) error {
	step, err := durableVoteStep(vote.Type)
	if err != nil {
		return err
	}
	sameHRS, err := validator.state.CheckHRS(vote.Height, vote.Round, step)
	if err != nil {
		return err
	}
	signBytes := cmttypes.VoteSignBytes(chainID, vote)

	var extensionSignature []byte
	if vote.Type == cmtproto.PrecommitType && !cmttypes.ProtoBlockIDIsNil(&vote.BlockID) {
		extensionSignature, err = validator.key.PrivKey.Sign(cmttypes.VoteExtensionSignBytes(chainID, vote))
		if err != nil {
			return err
		}
	} else if len(vote.Extension) > 0 {
		return errors.New("unexpected vote extension outside a non-nil precommit")
	}

	if sameHRS {
		switch {
		case bytes.Equal(signBytes, validator.state.SignBytes):
			vote.Signature = append([]byte(nil), validator.state.Signature...)
		case votesOnlyDifferByTimestamp(validator.state.SignBytes, signBytes, vote):
			vote.Signature = append([]byte(nil), validator.state.Signature...)
		default:
			return errors.New("conflicting vote data")
		}
		vote.ExtensionSignature = extensionSignature
		return nil
	}

	signature, err := validator.key.PrivKey.Sign(signBytes)
	if err != nil {
		return err
	}
	if err := validator.persist(vote.Height, vote.Round, step, signBytes, signature); err != nil {
		return fmt.Errorf("%w while persisting signed vote: %v", errValidatorStatePersistence, err)
	}
	vote.Signature = signature
	vote.ExtensionSignature = extensionSignature
	return nil
}

func (validator *durablePrivValidator) signProposal(chainID string, proposal *cmtproto.Proposal) error {
	sameHRS, err := validator.state.CheckHRS(proposal.Height, proposal.Round, validatorStepPropose)
	if err != nil {
		return err
	}
	signBytes := cmttypes.ProposalSignBytes(chainID, proposal)
	if sameHRS {
		switch {
		case bytes.Equal(signBytes, validator.state.SignBytes):
			proposal.Signature = append([]byte(nil), validator.state.Signature...)
		case proposalsOnlyDifferByTimestamp(validator.state.SignBytes, signBytes, proposal):
			proposal.Signature = append([]byte(nil), validator.state.Signature...)
		default:
			return errors.New("conflicting proposal data")
		}
		return nil
	}
	signature, err := validator.key.PrivKey.Sign(signBytes)
	if err != nil {
		return err
	}
	if err := validator.persist(proposal.Height, proposal.Round, validatorStepPropose, signBytes, signature); err != nil {
		return fmt.Errorf("%w while persisting signed proposal: %v", errValidatorStatePersistence, err)
	}
	proposal.Signature = signature
	return nil
}

func (validator *durablePrivValidator) persist(height int64, round int32, step int8, signBytes, signature []byte) error {
	next := privval.FilePVLastSignState{
		Height: height, Round: round, Step: step,
		SignBytes: cmtbytes.HexBytes(append([]byte(nil), signBytes...)),
		Signature: append([]byte(nil), signature...),
	}
	raw, err := cmtjson.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := writeSignStateAtomically(validator.statePath, raw, 0o600); err != nil {
		return err
	}
	validator.state = next
	return nil
}

func durableVoteStep(voteType cmtproto.SignedMsgType) (int8, error) {
	switch voteType {
	case cmtproto.PrevoteType:
		return validatorStepPrevote, nil
	case cmtproto.PrecommitType:
		return validatorStepPrecommit, nil
	default:
		return 0, fmt.Errorf("unknown vote type %v", voteType)
	}
}

func votesOnlyDifferByTimestamp(lastBytes, newBytes []byte, vote *cmtproto.Vote) bool {
	var lastVote, newVote cmtproto.CanonicalVote
	if protoio.UnmarshalDelimited(lastBytes, &lastVote) != nil || protoio.UnmarshalDelimited(newBytes, &newVote) != nil {
		return false
	}
	lastTime := lastVote.Timestamp
	now := cmttime.Now()
	lastVote.Timestamp = now
	newVote.Timestamp = now
	if !proto.Equal(&newVote, &lastVote) {
		return false
	}
	vote.Timestamp = lastTime
	return true
}

func proposalsOnlyDifferByTimestamp(lastBytes, newBytes []byte, proposal *cmtproto.Proposal) bool {
	var lastProposal, newProposal cmtproto.CanonicalProposal
	if protoio.UnmarshalDelimited(lastBytes, &lastProposal) != nil || protoio.UnmarshalDelimited(newBytes, &newProposal) != nil {
		return false
	}
	lastTime := lastProposal.Timestamp
	now := cmttime.Now()
	lastProposal.Timestamp = now
	newProposal.Timestamp = now
	if !proto.Equal(&newProposal, &lastProposal) {
		return false
	}
	proposal.Timestamp = lastTime
	return true
}
