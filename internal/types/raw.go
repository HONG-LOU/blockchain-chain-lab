package types

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"chainlab/internal/hash"
)

func EncodeRawTransaction(tx Transaction) (string, error) {
	if err := validateRawTransaction(tx); err != nil {
		return "", err
	}
	encoded, err := hash.CanonicalBytes(tx)
	if err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(encoded), nil
}

func DecodeRawTransaction(raw string) (Transaction, error) {
	if !strings.HasPrefix(raw, "0x") {
		return Transaction{}, fmt.Errorf("raw transaction must be 0x-prefixed hex")
	}
	encoded, err := hex.DecodeString(strings.TrimPrefix(raw, "0x"))
	if err != nil {
		return Transaction{}, fmt.Errorf("invalid raw transaction hex")
	}
	var tx Transaction
	if err := json.Unmarshal(encoded, &tx); err != nil {
		return Transaction{}, fmt.Errorf("invalid raw transaction json")
	}
	if err := validateRawTransaction(tx); err != nil {
		return Transaction{}, err
	}
	return tx, nil
}

func validateRawTransaction(tx Transaction) error {
	if tx.ChainID == "" {
		return fmt.Errorf("raw transaction missing chain_id")
	}
	if tx.Type == "" {
		return fmt.Errorf("raw transaction missing type")
	}
	if strings.TrimSpace(tx.From) == "" {
		return fmt.Errorf("raw transaction missing from")
	}
	if tx.GasLimit == 0 {
		return fmt.Errorf("raw transaction missing gas_limit")
	}
	if strings.TrimSpace(tx.Signature) == "" && len(tx.Authorizations) == 0 {
		return fmt.Errorf("raw transaction missing signature")
	}
	for _, authorization := range tx.Authorizations {
		if strings.TrimSpace(authorization.Signer) == "" || strings.TrimSpace(authorization.Signature) == "" {
			return fmt.Errorf("raw transaction missing authorization signature")
		}
	}
	return nil
}
