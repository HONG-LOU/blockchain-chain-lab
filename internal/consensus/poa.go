package consensus

import (
	"errors"
	"fmt"
	"math"
	"strings"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

type POA struct {
	validators   []string
	validatorSet map[string]struct{}
	schemaErr    error
}

// MaxBlockTimeStepSeconds prevents a proposer from exhausting the signed
// timestamp domain in one block. Production time comes from CometBFT rounds.
const MaxBlockTimeStepSeconds int64 = 60 * 60

func NewPOA(validators []string) *POA {
	engine := &POA{validatorSet: make(map[string]struct{}, len(validators))}
	engine.schemaErr = ValidateValidatorSet(validators)
	for _, validator := range validators {
		normalized, err := chaincrypto.NormalizeAddress(validator)
		if err != nil || normalized != validator {
			if engine.schemaErr == nil {
				engine.schemaErr = errors.New("validator set contains non-canonical address")
			}
			continue
		}
		if _, ok := engine.validatorSet[normalized]; ok {
			if engine.schemaErr == nil {
				engine.schemaErr = errors.New("validator set contains duplicate address")
			}
			continue
		}
		engine.validatorSet[normalized] = struct{}{}
		engine.validators = append(engine.validators, normalized)
	}
	return engine
}

func ValidateValidatorSet(validators []string) error {
	if len(validators) == 0 {
		return errors.New("validator set is empty")
	}
	if len(validators) > types.MaxValidators {
		return fmt.Errorf("validator set exceeds %d entries", types.MaxValidators)
	}
	seen := make(map[string]struct{}, len(validators))
	for _, validator := range validators {
		normalized, err := chaincrypto.NormalizeAddress(validator)
		if err != nil || normalized != validator {
			return errors.New("validator set contains non-canonical address")
		}
		if _, exists := seen[validator]; exists {
			return errors.New("validator set contains duplicate address")
		}
		seen[validator] = struct{}{}
	}
	return nil
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
		ChainID:   block.Header.ChainID,
		Height:    block.Header.Height,
		BlockHash: block.Hash(),
		Validator: validator,
		Signature: signature,
	}, nil
}

func VerifyFinalityVote(block types.Block, vote types.FinalitySignature) bool {
	if vote.ChainID != block.Header.ChainID || vote.Height != block.Header.Height || vote.BlockHash != block.Hash() {
		return false
	}
	if err := types.ValidateCanonicalHash("finality vote block hash", vote.BlockHash); err != nil {
		return false
	}
	validator, err := chaincrypto.NormalizeAddress(vote.Validator)
	if err != nil || validator != vote.Validator {
		return false
	}
	if err := types.ValidateCanonicalSignature("finality signature", vote.Signature); err != nil {
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
	if p.schemaErr != nil {
		return p.schemaErr
	}
	if block.Header.ChainID == "" || block.Header.ChainID != strings.TrimSpace(block.Header.ChainID) || block.Header.ChainID != parent.Header.ChainID {
		return errors.New("block chain id mismatch or non-canonical encoding")
	}
	proposer, err := chaincrypto.NormalizeAddress(block.Header.Proposer)
	if err != nil || proposer != block.Header.Proposer {
		return errors.New("block proposer is not canonically encoded")
	}
	if _, ok := p.validatorSet[proposer]; !ok {
		return errors.New("unauthorized proposer")
	}
	if block.Header.Height != parent.Header.Height+1 {
		return fmt.Errorf("bad height: got %d want %d", block.Header.Height, parent.Header.Height+1)
	}
	if block.Header.TimeUnix <= parent.Header.TimeUnix {
		return errors.New("block time must increase")
	}
	if parent.Header.TimeUnix > math.MaxInt64-MaxBlockTimeStepSeconds ||
		block.Header.TimeUnix > parent.Header.TimeUnix+MaxBlockTimeStepSeconds {
		return fmt.Errorf("block time step exceeds %d seconds", MaxBlockTimeStepSeconds)
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
	if block.Header.GasLimit == 0 || block.Header.GasUsed > block.Header.GasLimit {
		return errors.New("invalid block gas fields")
	}
	if block.Header.BaseFeePerGas == 0 {
		return errors.New("block base fee is required")
	}
	if err := types.ValidateCanonicalHash("transaction root", block.Header.TxRoot); err != nil {
		return err
	}
	if err := types.ValidateCanonicalHash("receipt root", block.Header.ReceiptRoot); err != nil {
		return err
	}
	if err := types.ValidateCanonicalHash("state root", block.Header.StateRoot); err != nil {
		return err
	}
	if err := ValidateBlockSignature(block); err != nil {
		return err
	}
	if len(block.Transactions) != len(block.Receipts) {
		return errors.New("block receipts must match transaction count")
	}
	if block.Header.TxRoot != types.TransactionRoot(block.Transactions) {
		return errors.New("transaction root mismatch")
	}
	if block.Header.ReceiptRoot != types.ReceiptRoot(block.Receipts) {
		return errors.New("receipt root mismatch")
	}
	var receiptGasUsed uint64
	for index, receipt := range block.Receipts {
		if receipt.BaseFeePerGas != block.Header.BaseFeePerGas {
			return fmt.Errorf("receipt %d base fee mismatch", index)
		}
		if err := types.ValidateReceiptSchema(receipt, block.Transactions[index].Hash()); err != nil {
			return fmt.Errorf("receipt %d: %w", index, err)
		}
		if math.MaxUint64-receiptGasUsed < receipt.GasUsed {
			return errors.New("receipt gas used overflow")
		}
		receiptGasUsed += receipt.GasUsed
	}
	if receiptGasUsed != block.Header.GasUsed {
		return errors.New("block gas used does not match receipts")
	}
	if block.FinalityCertificate != nil {
		if err := p.validateFinalityCertificate(block); err != nil {
			return err
		}
	}
	return nil
}

func ValidateBlockSignature(block types.Block) error {
	proposer, err := chaincrypto.NormalizeAddress(block.Header.Proposer)
	if err != nil || proposer != block.Header.Proposer {
		return errors.New("block proposer is not canonically encoded")
	}
	if err := types.ValidateCanonicalSignature("block signature", block.Signature); err != nil {
		return err
	}
	if !chaincrypto.Verify(block.Header.Proposer, block.Header.SigningBytes(), block.Signature) {
		return errors.New("invalid block signature")
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
	previousValidator := ""
	for _, signature := range certificate.Signatures {
		if signature.ChainID != certificate.ChainID || signature.Height != certificate.Height || signature.BlockHash != certificate.BlockHash {
			return errors.New("finality signature envelope mismatch")
		}
		validator, err := chaincrypto.NormalizeAddress(signature.Validator)
		if err != nil || validator != signature.Validator {
			return errors.New("finality signature validator is not canonically encoded")
		}
		if _, ok := p.validatorSet[validator]; !ok {
			return errors.New("finality signature from non-validator")
		}
		if _, ok := seen[validator]; ok {
			return errors.New("duplicate finality signature")
		}
		if previousValidator != "" && validator <= previousValidator {
			return errors.New("finality signatures are not strictly sorted")
		}
		if !VerifyFinalityVote(block, signature) {
			return errors.New("invalid finality signature")
		}
		seen[validator] = struct{}{}
		previousValidator = validator
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
