package consensus

import (
	"errors"
	"fmt"
	"strings"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

type POA struct {
	validators   []string
	validatorSet map[string]struct{}
}

func NewPOA(validators []string) *POA {
	engine := &POA{validatorSet: make(map[string]struct{}, len(validators))}
	for _, validator := range validators {
		normalized := strings.ToLower(strings.TrimSpace(validator))
		if normalized == "" {
			continue
		}
		if _, ok := engine.validatorSet[normalized]; ok {
			continue
		}
		engine.validatorSet[normalized] = struct{}{}
		engine.validators = append(engine.validators, normalized)
	}
	return engine
}

func SignBlock(key chaincrypto.PrivateKey, block *types.Block) error {
	signature, err := chaincrypto.Sign(key, block.Header.SigningBytes())
	if err != nil {
		return err
	}
	block.Signature = signature
	return nil
}

func SignFinalityVote(key chaincrypto.PrivateKey, block types.Block) (types.FinalitySignature, error) {
	validator := chaincrypto.AddressFromPrivateKey(key)
	signature, err := chaincrypto.Sign(key, finalityVoteSigningBytes(block))
	if err != nil {
		return types.FinalitySignature{}, err
	}
	return types.FinalitySignature{
		Validator: validator,
		Signature: signature,
	}, nil
}

func VerifyFinalityVote(block types.Block, vote types.FinalitySignature) bool {
	validator := strings.ToLower(strings.TrimSpace(vote.Validator))
	if validator == "" {
		return false
	}
	return chaincrypto.Verify(validator, finalityVoteSigningBytes(block), vote.Signature)
}

func FinalityQuorumSize(validatorCount int) int {
	if validatorCount <= 0 {
		return 0
	}
	return validatorCount*2/3 + 1
}

func (p *POA) FinalityQuorum() int {
	return FinalityQuorumSize(len(p.validators))
}

func (p *POA) ValidateBlock(parent types.Block, block types.Block) error {
	proposer := strings.ToLower(block.Header.Proposer)
	if _, ok := p.validatorSet[proposer]; !ok {
		return errors.New("unauthorized proposer")
	}
	if block.Header.Height != parent.Header.Height+1 {
		return fmt.Errorf("bad height: got %d want %d", block.Header.Height, parent.Header.Height+1)
	}
	expected, ok := p.expectedProposer(block.Header.Height)
	if !ok {
		return errors.New("no validators configured")
	}
	if proposer != expected {
		return fmt.Errorf("unexpected proposer: got %s want %s", block.Header.Proposer, expected)
	}
	if block.Header.ParentHash != parent.Hash() {
		return errors.New("parent hash mismatch")
	}
	if block.Header.TxRoot != types.TransactionRoot(block.Transactions) {
		return errors.New("transaction root mismatch")
	}
	if block.Header.ReceiptRoot != types.ReceiptRoot(block.Receipts) {
		return errors.New("receipt root mismatch")
	}
	if block.Header.StateRoot == "" {
		return errors.New("state root is required")
	}
	if !chaincrypto.Verify(block.Header.Proposer, block.Header.SigningBytes(), block.Signature) {
		return errors.New("invalid block signature")
	}
	if block.FinalityCertificate != nil {
		if err := p.validateFinalityCertificate(block); err != nil {
			return err
		}
	}
	return nil
}

func (p *POA) validateFinalityCertificate(block types.Block) error {
	certificate := block.FinalityCertificate
	if certificate.ChainID != block.Header.ChainID {
		return errors.New("finality certificate chain id mismatch")
	}
	if certificate.Height != block.Header.Height {
		return errors.New("finality certificate height mismatch")
	}
	if certificate.BlockHash != block.Hash() {
		return errors.New("finality certificate block hash mismatch")
	}
	seen := make(map[string]struct{}, len(certificate.Signatures))
	for _, signature := range certificate.Signatures {
		validator := strings.ToLower(strings.TrimSpace(signature.Validator))
		if validator == "" {
			return errors.New("finality signature validator is required")
		}
		if _, ok := p.validatorSet[validator]; !ok {
			return errors.New("finality signature from non-validator")
		}
		if _, ok := seen[validator]; ok {
			return errors.New("duplicate finality signature")
		}
		if !VerifyFinalityVote(block, types.FinalitySignature{Validator: validator, Signature: signature.Signature}) {
			return errors.New("invalid finality signature")
		}
		seen[validator] = struct{}{}
	}
	if len(seen)*3 <= len(p.validators)*2 {
		return errors.New("finality certificate below quorum")
	}
	return nil
}

func finalityVoteSigningBytes(block types.Block) []byte {
	return types.FinalityVoteSigningBytes(block.Header.ChainID, block.Header.Height, block.Hash())
}

func (p *POA) expectedProposer(height uint64) (string, bool) {
	if height == 0 || len(p.validators) == 0 {
		return "", false
	}
	index := (height - 1) % uint64(len(p.validators))
	return p.validators[index], true
}
