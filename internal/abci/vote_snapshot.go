package abci

import (
	"context"
	"encoding/hex"
	"errors"

	abcitypes "github.com/cometbft/cometbft/abci/types"
)

func (a *Application) ExtendVote(_ context.Context, req *abcitypes.RequestExtendVote) (*abcitypes.ResponseExtendVote, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.requireReadyLocked(); err != nil {
		return nil, err
	}
	if req == nil || req.Height != a.committed.commitment.Height+1 || len(req.Hash) != 32 {
		return nil, errors.New("vote extension height is out of sequence")
	}
	if _, err := a.proposerLocked(req.ProposerAddress, req.Height); err != nil {
		return nil, err
	}
	return &abcitypes.ResponseExtendVote{}, nil
}

func (a *Application) VerifyVoteExtension(_ context.Context, req *abcitypes.RequestVerifyVoteExtension) (*abcitypes.ResponseVerifyVoteExtension, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.requireReadyLocked(); err != nil {
		return nil, err
	}
	if req == nil || req.Height != a.committed.commitment.Height+1 || len(req.Hash) != 32 || len(req.VoteExtension) != 0 {
		return &abcitypes.ResponseVerifyVoteExtension{Status: abcitypes.ResponseVerifyVoteExtension_REJECT}, nil
	}
	if _, exists := a.proposers[hex.EncodeToString(req.ValidatorAddress)]; !exists {
		return &abcitypes.ResponseVerifyVoteExtension{Status: abcitypes.ResponseVerifyVoteExtension_REJECT}, nil
	}
	if a.genesis.Protocol == ProtocolVersionV2 {
		identity, known := a.committed.store.ValidatorIdentityByConsensusAddress(hex.EncodeToString(req.ValidatorAddress))
		if !known || !validatorIdentityActive(identity, req.Height) {
			return &abcitypes.ResponseVerifyVoteExtension{Status: abcitypes.ResponseVerifyVoteExtension_REJECT}, nil
		}
	}
	if len(req.ValidatorAddress) != 20 {
		return &abcitypes.ResponseVerifyVoteExtension{Status: abcitypes.ResponseVerifyVoteExtension_REJECT}, nil
	}
	return &abcitypes.ResponseVerifyVoteExtension{Status: abcitypes.ResponseVerifyVoteExtension_ACCEPT}, nil
}
