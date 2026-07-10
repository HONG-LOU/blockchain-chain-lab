package node

import (
	"errors"
	"fmt"
	"math"

	"chainlab/internal/hash"
	"chainlab/internal/types"
)

const (
	// MaxTransactionBytes is the consensus-visible canonical transaction limit.
	MaxTransactionBytes = 2 * 1024 * 1024
	// MaxBlockBytes is the consensus-visible canonical block limit.
	MaxBlockBytes = 16 * 1024 * 1024
	// MaxFinalityCertificateBytes reserves space so every valid base block can
	// later carry a maximum-size quorum certificate.
	MaxFinalityCertificateBytes = 4 * 1024 * 1024
	MaxUncertifiedBlockBytes    = MaxBlockBytes - MaxFinalityCertificateBytes
	// MaxTransactionsPerBlock bounds validation work independently of block bytes.
	MaxTransactionsPerBlock = 4096
	// MaxFinalitySignaturesPerBlock bounds certificate verification and copying.
	MaxFinalitySignaturesPerBlock = types.MaxValidators
	// These bounds contain authenticated fork spam from an authorized but
	// Byzantine proposer without constraining canonical chain growth.
	MaxKnownBlocksPerHeight = 16
	MaxKnownSideBlocks      = 4096

	// This covers the header, signature, JSON field names, arrays, and separators.
	maxProducedBlockEnvelopeBytes = 64 * 1024
)

func canonicalTransactionSize(tx types.Transaction) (uint64, error) {
	rawSize, err := transactionRawContentSize(tx)
	if err != nil {
		return 0, err
	}
	if rawSize > MaxTransactionBytes {
		return 0, fmt.Errorf("transaction exceeds %d bytes", MaxTransactionBytes)
	}
	raw, err := hash.CanonicalBytes(tx)
	if err != nil {
		return 0, fmt.Errorf("encode canonical transaction: %w", err)
	}
	size := uint64(len(raw))
	if size > MaxTransactionBytes {
		return 0, fmt.Errorf("transaction exceeds %d bytes", MaxTransactionBytes)
	}
	return size, nil
}

func validateBlockSize(block types.Block) error {
	if err := validateBlockCheapBounds(block); err != nil {
		return err
	}
	baseBlock := block
	baseBlock.FinalityCertificate = nil
	baseSize, err := canonicalBlockSize(baseBlock)
	if err != nil {
		return err
	}
	if baseSize > MaxUncertifiedBlockBytes {
		return fmt.Errorf("block without finality certificate exceeds reserved base limit %d", MaxUncertifiedBlockBytes)
	}
	if block.FinalityCertificate != nil {
		certificateSize, err := canonicalFinalityCertificateSize(block.FinalityCertificate)
		if err != nil {
			return err
		}
		if certificateSize > MaxFinalityCertificateBytes {
			return fmt.Errorf("finality certificate exceeds %d bytes", MaxFinalityCertificateBytes)
		}
	}
	size, err := canonicalBlockSize(block)
	if err != nil {
		return err
	}
	if size > MaxBlockBytes {
		return fmt.Errorf("block exceeds %d bytes", MaxBlockBytes)
	}
	return nil
}

func validateBlockCheapBounds(block types.Block) error {
	if len(block.Transactions) > MaxTransactionsPerBlock {
		return fmt.Errorf("block transaction count exceeds %d", MaxTransactionsPerBlock)
	}
	if len(block.Receipts) > MaxTransactionsPerBlock {
		return fmt.Errorf("block receipt count exceeds %d", MaxTransactionsPerBlock)
	}
	if len(block.Receipts) != len(block.Transactions) {
		return errors.New("block receipts must match transaction count")
	}
	if block.FinalityCertificate != nil && len(block.FinalityCertificate.Signatures) > MaxFinalitySignaturesPerBlock {
		return fmt.Errorf("block finality signature count exceeds %d", MaxFinalitySignaturesPerBlock)
	}
	if err := validateBlockRawContentSize(block); err != nil {
		return err
	}
	return nil
}

// canonicalBlockSize mirrors encoding/json's field order for types.Block while
// avoiding a second full-block allocation at the network ingress boundary.
func canonicalBlockSize(block types.Block) (uint64, error) {
	total := uint64(len(`{"header":`))
	header, err := hash.CanonicalBytes(block.Header)
	if err != nil {
		return 0, fmt.Errorf("encode canonical block header: %w", err)
	}
	if total, err = checkedSizeAdd(total, uint64(len(header))); err != nil {
		return 0, err
	}

	if len(block.Transactions) > 0 {
		if total, err = checkedSizeAdd(total, uint64(len(`,"transactions":[`))); err != nil {
			return 0, err
		}
		for index, tx := range block.Transactions {
			if index > 0 {
				if total, err = checkedSizeAdd(total, 1); err != nil {
					return 0, err
				}
			}
			txSize, err := canonicalTransactionSize(tx)
			if err != nil {
				return 0, fmt.Errorf("block transaction %d: %w", index, err)
			}
			if total, err = checkedSizeAdd(total, txSize); err != nil {
				return 0, err
			}
			if total > MaxBlockBytes {
				return total, nil
			}
		}
		if total, err = checkedSizeAdd(total, 1); err != nil {
			return 0, err
		}
	}

	if len(block.Receipts) > 0 {
		if total, err = checkedSizeAdd(total, uint64(len(`,"receipts":[`))); err != nil {
			return 0, err
		}
		for index, receipt := range block.Receipts {
			if index > 0 {
				if total, err = checkedSizeAdd(total, 1); err != nil {
					return 0, err
				}
			}
			raw, err := hash.CanonicalBytes(receipt)
			if err != nil {
				return 0, fmt.Errorf("encode canonical receipt %d: %w", index, err)
			}
			if total, err = checkedSizeAdd(total, uint64(len(raw))); err != nil {
				return 0, err
			}
			if total > MaxBlockBytes {
				return total, nil
			}
		}
		if total, err = checkedSizeAdd(total, 1); err != nil {
			return 0, err
		}
	}

	if block.Signature != "" {
		if total, err = checkedSizeAdd(total, uint64(len(`,"signature":`))); err != nil {
			return 0, err
		}
		raw, err := hash.CanonicalBytes(block.Signature)
		if err != nil {
			return 0, fmt.Errorf("encode canonical block signature: %w", err)
		}
		if total, err = checkedSizeAdd(total, uint64(len(raw))); err != nil {
			return 0, err
		}
	}

	if block.FinalityCertificate != nil {
		if total, err = checkedSizeAdd(total, uint64(len(`,"finality_certificate":`))); err != nil {
			return 0, err
		}
		certificateSize, err := canonicalFinalityCertificateSize(block.FinalityCertificate)
		if err != nil {
			return 0, err
		}
		if total, err = checkedSizeAdd(total, certificateSize); err != nil {
			return 0, err
		}
	}

	return checkedSizeAdd(total, 1)
}

func canonicalFinalityCertificateSize(certificate *types.FinalityCertificate) (uint64, error) {
	prefix, err := hash.CanonicalBytes(struct {
		ChainID   string `json:"chain_id"`
		Height    uint64 `json:"height"`
		BlockHash string `json:"block_hash"`
	}{
		ChainID:   certificate.ChainID,
		Height:    certificate.Height,
		BlockHash: certificate.BlockHash,
	})
	if err != nil {
		return 0, fmt.Errorf("encode canonical finality certificate: %w", err)
	}
	if len(prefix) == 0 {
		return 0, errors.New("canonical finality certificate prefix is empty")
	}
	total := uint64(len(prefix) - 1)
	if total, err = checkedSizeAdd(total, uint64(len(`,"signatures":`))); err != nil {
		return 0, err
	}
	if certificate.Signatures == nil {
		if total, err = checkedSizeAdd(total, uint64(len("null"))); err != nil {
			return 0, err
		}
		return checkedSizeAdd(total, 1)
	}
	if total, err = checkedSizeAdd(total, 1); err != nil {
		return 0, err
	}
	for index, signature := range certificate.Signatures {
		if index > 0 {
			if total, err = checkedSizeAdd(total, 1); err != nil {
				return 0, err
			}
		}
		raw, err := hash.CanonicalBytes(signature)
		if err != nil {
			return 0, fmt.Errorf("encode canonical finality signature %d: %w", index, err)
		}
		if total, err = checkedSizeAdd(total, uint64(len(raw))); err != nil {
			return 0, err
		}
		if total > MaxBlockBytes {
			return total, nil
		}
	}
	if total, err = checkedSizeAdd(total, 1); err != nil {
		return 0, err
	}
	return checkedSizeAdd(total, 1)
}

func validateBlockRawContentSize(block types.Block) error {
	total, err := addStringSizes(0,
		block.Header.ChainID,
		block.Header.ParentHash,
		block.Header.Proposer,
		block.Header.TxRoot,
		block.Header.ReceiptRoot,
		block.Header.StateRoot,
	)
	if err != nil {
		return err
	}
	for _, tx := range block.Transactions {
		txSize, sizeErr := transactionRawContentSize(tx)
		if sizeErr != nil {
			return sizeErr
		}
		if txSize > MaxTransactionBytes {
			return fmt.Errorf("transaction exceeds %d bytes", MaxTransactionBytes)
		}
		if total, err = checkedSizeAdd(total, txSize); err != nil {
			return err
		}
		if total > MaxBlockBytes {
			return fmt.Errorf("block exceeds %d bytes", MaxBlockBytes)
		}
	}
	for _, receipt := range block.Receipts {
		receiptSize, sizeErr := receiptRawContentSize(receipt)
		if sizeErr != nil {
			return sizeErr
		}
		if total, err = checkedSizeAdd(total, receiptSize); err != nil {
			return err
		}
		if total > MaxBlockBytes {
			return fmt.Errorf("block exceeds %d bytes", MaxBlockBytes)
		}
	}
	if total, err = addStringSizes(total, block.Signature); err != nil {
		return err
	}
	if certificate := block.FinalityCertificate; certificate != nil {
		if total, err = addStringSizes(total, certificate.ChainID, certificate.BlockHash); err != nil {
			return err
		}
		if total, err = checkedSizeAdd(total, uint64(len(certificate.Signatures))); err != nil {
			return err
		}
		for _, signature := range certificate.Signatures {
			if total, err = addStringSizes(total, signature.ChainID, signature.BlockHash, signature.Validator, signature.Signature); err != nil {
				return err
			}
		}
	}
	if total > MaxBlockBytes {
		return fmt.Errorf("block exceeds %d bytes", MaxBlockBytes)
	}
	return nil
}

func transactionRawContentSize(tx types.Transaction) (uint64, error) {
	total, err := addStringSizes(0,
		tx.ChainID,
		string(tx.Type),
		tx.From,
		tx.Signer,
		tx.To,
		tx.Paymaster,
		tx.Signature,
		tx.PaymasterSignature,
		tx.SignatureKind,
		tx.EthereumRawHash,
	)
	if err != nil {
		return 0, err
	}
	if total, err = checkedSizeAdd(total, uint64(len(tx.Payload))); err != nil {
		return 0, err
	}
	for key, value := range tx.Payload {
		if total, err = addStringSizes(total, key, value); err != nil {
			return 0, err
		}
	}
	if total, err = checkedSizeAdd(total, uint64(len(tx.Batch))); err != nil {
		return 0, err
	}
	for _, operation := range tx.Batch {
		if total, err = addStringSizes(total, string(operation.Type), operation.To); err != nil {
			return 0, err
		}
		if total, err = checkedSizeAdd(total, uint64(len(operation.Payload))); err != nil {
			return 0, err
		}
		for key, value := range operation.Payload {
			if total, err = addStringSizes(total, key, value); err != nil {
				return 0, err
			}
		}
	}
	if total, err = checkedSizeAdd(total, uint64(len(tx.Authorizations))); err != nil {
		return 0, err
	}
	for _, authorization := range tx.Authorizations {
		if total, err = addStringSizes(total, authorization.Signer, authorization.Signature); err != nil {
			return 0, err
		}
	}
	return total, nil
}

func receiptRawContentSize(receipt types.Receipt) (uint64, error) {
	total, err := addStringSizes(0,
		receipt.TxHash,
		receipt.FailureCode,
		receipt.Error,
		receipt.FeePayer,
		receipt.CodeID,
		receipt.ProposalID,
		receipt.ContractAddress,
	)
	if err != nil {
		return 0, err
	}
	if total, err = checkedSizeAdd(total, uint64(len(receipt.Events))); err != nil {
		return 0, err
	}
	for _, event := range receipt.Events {
		if total, err = addStringSizes(total, event.Type); err != nil {
			return 0, err
		}
		if total, err = checkedSizeAdd(total, uint64(len(event.Attributes))); err != nil {
			return 0, err
		}
		for key, value := range event.Attributes {
			if total, err = addStringSizes(total, key, value); err != nil {
				return 0, err
			}
		}
	}
	return total, nil
}

func addStringSizes(total uint64, values ...string) (uint64, error) {
	var err error
	for _, value := range values {
		total, err = checkedSizeAdd(total, uint64(len(value)))
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func checkedSizeAdd(left uint64, right uint64) (uint64, error) {
	if math.MaxUint64-left < right {
		return 0, errors.New("canonical byte size overflow")
	}
	return left + right, nil
}
