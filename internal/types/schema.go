package types

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"

	chaincrypto "chainlab/internal/crypto"
)

var secp256k1HalfOrder = mustBigInt("7fffffffffffffffffffffffffffffff5d576e7357a4501ddfe92f46681b20a0")

func ValidateChainID(value string) error {
	if value == "" || len(value) > MaxChainIDBytes {
		return fmt.Errorf("chain id must contain between 1 and %d bytes", MaxChainIDBytes)
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return errors.New("chain id contains a non-canonical character")
	}
	return nil
}

func ValidateCanonicalHash(field string, value string) error {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return fmt.Errorf("%s is not a canonical 32-byte hash", field)
	}
	if _, err := hex.DecodeString(value[2:]); err != nil {
		return fmt.Errorf("%s is not a canonical 32-byte hash", field)
	}
	return nil
}

func ValidateCanonicalSignature(field string, value string) error {
	if len(value) != 132 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return fmt.Errorf("%s is not a canonical compact signature", field)
	}
	raw, err := hex.DecodeString(value[2:])
	if err != nil || len(raw) != 65 || raw[0] < 27 || raw[0] > 30 {
		return fmt.Errorf("%s is not a canonical compact signature", field)
	}
	if new(big.Int).SetBytes(raw[33:65]).Cmp(secp256k1HalfOrder) > 0 {
		return fmt.Errorf("%s is not low-s", field)
	}
	return nil
}

func mustBigInt(encoded string) *big.Int {
	value, ok := new(big.Int).SetString(encoded, 16)
	if !ok {
		panic("invalid secp256k1 constant")
	}
	return value
}

func ValidateReceiptSchema(receipt Receipt, expectedTxHash string) error {
	if err := ValidateCanonicalHash("receipt transaction hash", receipt.TxHash); err != nil {
		return err
	}
	if receipt.TxHash != expectedTxHash {
		return errors.New("receipt transaction hash mismatch")
	}
	if receipt.Error != "" {
		return errors.New("receipt error text is not consensus data")
	}
	if receipt.Success {
		if receipt.FailureCode != "" {
			return errors.New("successful receipt cannot include failure code")
		}
	} else if !validReceiptFailureCode(receipt.FailureCode) {
		return errors.New("failed receipt requires a recognized failure code")
	}
	if receipt.FeePayer != "" {
		payer, err := chaincrypto.NormalizeAddress(receipt.FeePayer)
		if err != nil || payer != receipt.FeePayer {
			return errors.New("receipt fee payer is not canonically encoded")
		}
	}
	if receipt.ContractAddress != "" {
		address, err := chaincrypto.NormalizeAddress(receipt.ContractAddress)
		if err != nil || address != receipt.ContractAddress {
			return errors.New("receipt contract address is not canonically encoded")
		}
	}
	if receipt.GasUsed != 0 && receipt.BaseFeePerGas > math.MaxUint64/receipt.GasUsed {
		return errors.New("receipt base fee overflow")
	}
	if receipt.BaseFeeBurned != receipt.GasUsed*receipt.BaseFeePerGas {
		return errors.New("receipt base fee breakdown mismatch")
	}
	if receipt.EffectiveGasPrice < receipt.BaseFeePerGas {
		return errors.New("receipt effective gas price is below base fee")
	}
	priorityPerGas := receipt.EffectiveGasPrice - receipt.BaseFeePerGas
	if receipt.GasUsed != 0 && priorityPerGas > math.MaxUint64/receipt.GasUsed {
		return errors.New("receipt priority fee overflow")
	}
	if receipt.PriorityFeePaid != receipt.GasUsed*priorityPerGas {
		return errors.New("receipt priority fee breakdown mismatch")
	}
	for _, event := range receipt.Events {
		if event.Type == "" {
			return errors.New("receipt event type is required")
		}
		for key := range event.Attributes {
			if key == "" {
				return errors.New("receipt event attribute key is required")
			}
		}
	}
	return nil
}

func validReceiptFailureCode(code string) bool {
	switch code {
	case ReceiptFailureExecutionReverted, ReceiptFailureOutOfGas, ReceiptFailureContractTrap, ReceiptFailureResourceLimit:
		return true
	default:
		return false
	}
}
