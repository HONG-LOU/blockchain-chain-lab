package abci

import (
	"context"
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
	if err := a.requireProtocolSupportLocked(req.Height, true); err != nil {
		return nil, err
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
	if err := a.requireProtocolSupportLocked(req.Height, true); err != nil {
		return &abcitypes.ResponseVerifyVoteExtension{Status: abcitypes.ResponseVerifyVoteExtension_REJECT}, nil
	}
	if _, err := a.validatorAccountLocked(req.ValidatorAddress, req.Height); err != nil {
		return &abcitypes.ResponseVerifyVoteExtension{Status: abcitypes.ResponseVerifyVoteExtension_REJECT}, nil
	}
	return &abcitypes.ResponseVerifyVoteExtension{Status: abcitypes.ResponseVerifyVoteExtension_ACCEPT}, nil
}
