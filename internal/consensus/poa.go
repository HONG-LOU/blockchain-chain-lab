package consensus

import (
	"errors"
	"fmt"
	"strings"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

type POA struct {
	validators map[string]struct{}
}

func NewPOA(validators []string) *POA {
	engine := &POA{validators: make(map[string]struct{}, len(validators))}
	for _, validator := range validators {
		engine.validators[strings.ToLower(validator)] = struct{}{}
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

func (p *POA) ValidateBlock(parent types.Block, block types.Block) error {
	proposer := strings.ToLower(block.Header.Proposer)
	if _, ok := p.validators[proposer]; !ok {
		return errors.New("unauthorized proposer")
	}
	if block.Header.Height != parent.Header.Height+1 {
		return fmt.Errorf("bad height: got %d want %d", block.Header.Height, parent.Header.Height+1)
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
	return nil
}
