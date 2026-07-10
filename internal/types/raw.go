package types

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	chaincrypto "chainlab/internal/crypto"
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
	return DecodeRawTransactionForChain(raw, "")
}

func DecodeRawTransactionForChain(raw string, chainID string) (Transaction, error) {
	if !strings.HasPrefix(raw, "0x") {
		return Transaction{}, fmt.Errorf("raw transaction must be 0x-prefixed hex")
	}
	encoded, err := hex.DecodeString(strings.TrimPrefix(raw, "0x"))
	if err != nil {
		return Transaction{}, fmt.Errorf("invalid raw transaction hex")
	}
	if len(encoded) > 0 && encoded[0] == 0x02 {
		return decodeEthereumType2RawTransaction(encoded, chainID)
	}
	return decodeChainLabRawTransactionBytes(encoded)
}

func decodeChainLabRawTransactionBytes(encoded []byte) (Transaction, error) {
	var tx Transaction
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&tx); err != nil {
		return Transaction{}, fmt.Errorf("invalid raw transaction json")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Transaction{}, fmt.Errorf("invalid raw transaction json")
	}
	if err := validateRawTransaction(tx); err != nil {
		return Transaction{}, err
	}
	canonical, err := hash.CanonicalBytes(tx)
	if err != nil {
		return Transaction{}, fmt.Errorf("encode canonical raw transaction: %w", err)
	}
	if !bytes.Equal(encoded, canonical) {
		return Transaction{}, fmt.Errorf("raw transaction json is not canonically encoded")
	}
	return tx, nil
}

func decodeEthereumType2RawTransaction(encoded []byte, expectedChainID string) (Transaction, error) {
	item, rest, err := rlpDecodeItem(encoded[1:])
	if err != nil {
		return Transaction{}, err
	}
	if len(rest) != 0 {
		return Transaction{}, fmt.Errorf("ethereum type2 raw transaction has trailing bytes")
	}
	if !item.list || len(item.children) != 12 {
		return Transaction{}, fmt.Errorf("ethereum type2 transaction must contain 12 rlp fields")
	}

	chainNumberValue, err := rlpUint64(item.children[0], "chain id")
	if err != nil {
		return Transaction{}, err
	}
	if chainNumberValue == 0 {
		return Transaction{}, fmt.Errorf("ethereum type2 chain id must be positive")
	}
	chainID := ethereumInternalChainID(chainNumberValue)
	if expectedChainID != "" {
		expectedChainNumber, err := ethereumChainNumber(expectedChainID)
		if err != nil {
			return Transaction{}, err
		}
		if expectedChainNumber != chainNumberValue {
			return Transaction{}, fmt.Errorf("ethereum type2 chain id %d does not match node chain %q", chainNumberValue, expectedChainID)
		}
		chainID = expectedChainID
	}
	nonce, err := rlpUint64(item.children[1], "nonce")
	if err != nil {
		return Transaction{}, err
	}
	maxPriorityFeePerGas, err := rlpUint64(item.children[2], "max priority fee per gas")
	if err != nil {
		return Transaction{}, err
	}
	maxFeePerGas, err := rlpUint64(item.children[3], "max fee per gas")
	if err != nil {
		return Transaction{}, err
	}
	gasLimit, err := rlpUint64(item.children[4], "gas limit")
	if err != nil {
		return Transaction{}, err
	}
	to, err := rlpAddress(item.children[5])
	if err != nil {
		return Transaction{}, err
	}
	value, err := rlpUint64(item.children[6], "value")
	if err != nil {
		return Transaction{}, err
	}
	if item.children[7].list || len(item.children[7].raw) != 0 {
		return Transaction{}, fmt.Errorf("ethereum type2 raw transfers require empty data")
	}
	if !item.children[8].list || len(item.children[8].children) != 0 {
		return Transaction{}, fmt.Errorf("ethereum type2 raw transfers require an empty access list")
	}
	yParity, err := rlpUint64(item.children[9], "signature y parity")
	if err != nil {
		return Transaction{}, err
	}
	signature, err := ethereumCompactSignature(yParity, item.children[10], item.children[11])
	if err != nil {
		return Transaction{}, err
	}

	tx := Transaction{
		ChainID:              chainID,
		Type:                 TxTransfer,
		To:                   to,
		Nonce:                nonce,
		Value:                value,
		GasLimit:             gasLimit,
		MaxFeePerGas:         maxFeePerGas,
		MaxPriorityFeePerGas: maxPriorityFeePerGas,
		Signature:            signature,
		SignatureKind:        SignatureKindEthereumType2,
	}
	digest, err := EthereumType2SigningDigest(tx)
	if err != nil {
		return Transaction{}, err
	}
	from, err := chaincrypto.RecoverDigestAddress(digest, signature)
	if err != nil {
		return Transaction{}, fmt.Errorf("invalid ethereum type2 signature")
	}
	tx.From = from
	canonical, err := EthereumType2SignedBytes(tx)
	if err != nil {
		return Transaction{}, err
	}
	if !bytes.Equal(encoded, canonical) {
		return Transaction{}, fmt.Errorf("ethereum type2 transaction is not canonically encoded")
	}
	tx.EthereumRawHash = hash.KeccakHex(canonical)
	if err := validateRawTransaction(tx); err != nil {
		return Transaction{}, err
	}
	return tx, nil
}

func EthereumType2SigningDigest(tx Transaction) ([]byte, error) {
	payload, err := ethereumType2SigningPayload(tx)
	if err != nil {
		return nil, err
	}
	return hash.Keccak(payload), nil
}

func EthereumType2SignedBytes(tx Transaction) ([]byte, error) {
	to, err := ethereumAddressBytes(tx.To)
	if err != nil {
		return nil, err
	}
	chainNumber, err := ethereumChainNumber(tx.ChainID)
	if err != nil {
		return nil, err
	}
	signature, err := canonicalCompactSignatureBytes(tx.Signature)
	if err != nil {
		return nil, err
	}
	yParity := uint64(signature[0] - 27)
	r := bytes.TrimLeft(signature[1:33], "\x00")
	s := bytes.TrimLeft(signature[33:65], "\x00")
	if len(r) == 0 || len(s) == 0 {
		return nil, fmt.Errorf("ethereum type2 signature values must be positive")
	}
	payload := rlpEncodeList(
		rlpEncodeUint64(chainNumber),
		rlpEncodeUint64(tx.Nonce),
		rlpEncodeUint64(tx.MaxPriorityFeePerGas),
		rlpEncodeUint64(tx.MaxFeePerGas),
		rlpEncodeUint64(tx.GasLimit),
		rlpEncodeBytes(to),
		rlpEncodeUint64(tx.Value),
		rlpEncodeBytes(nil),
		rlpEncodeList(),
		rlpEncodeUint64(yParity),
		rlpEncodeBytes(r),
		rlpEncodeBytes(s),
	)
	return append([]byte{0x02}, payload...), nil
}

func EthereumType2TransactionHash(tx Transaction) (string, error) {
	encoded, err := EthereumType2SignedBytes(tx)
	if err != nil {
		return "", err
	}
	return hash.KeccakHex(encoded), nil
}

func canonicalCompactSignatureBytes(signature string) ([]byte, error) {
	if err := ValidateCanonicalSignature("signature", signature); err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(signature[2:])
	if err != nil {
		return nil, fmt.Errorf("signature is not canonically encoded")
	}
	if raw[0] != 27 && raw[0] != 28 {
		return nil, fmt.Errorf("ethereum type2 signature recovery id must be 0 or 1")
	}
	return raw, nil
}

func ethereumType2SigningPayload(tx Transaction) ([]byte, error) {
	to, err := ethereumAddressBytes(tx.To)
	if err != nil {
		return nil, err
	}
	chainNumber, err := ethereumChainNumber(tx.ChainID)
	if err != nil {
		return nil, err
	}
	payload := rlpEncodeList(
		rlpEncodeUint64(chainNumber),
		rlpEncodeUint64(tx.Nonce),
		rlpEncodeUint64(tx.MaxPriorityFeePerGas),
		rlpEncodeUint64(tx.MaxFeePerGas),
		rlpEncodeUint64(tx.GasLimit),
		rlpEncodeBytes(to),
		rlpEncodeUint64(tx.Value),
		rlpEncodeBytes(nil),
		rlpEncodeList(),
	)
	return append([]byte{0x02}, payload...), nil
}

func ethereumInternalChainID(chainNumberValue uint64) string {
	if chainNumberValue == 31337 {
		return "chainlab-local"
	}
	return fmt.Sprintf("0x%x", chainNumberValue)
}

func ethereumChainNumber(chainID string) (uint64, error) {
	if chainID == "chainlab-local" {
		return 31337, nil
	}
	if len(chainID) <= 2 || !strings.HasPrefix(chainID, "0x") || chainID != strings.ToLower(chainID) {
		return 0, fmt.Errorf("ethereum type2 requires chainlab-local or canonical positive 0x chain id")
	}
	value, err := strconv.ParseUint(chainID[2:], 16, 64)
	if err != nil || value == 0 || fmt.Sprintf("0x%x", value) != chainID {
		return 0, fmt.Errorf("ethereum type2 requires chainlab-local or canonical positive 0x chain id")
	}
	return value, nil
}

func ethereumCompactSignature(yParity uint64, rItem rlpItem, sItem rlpItem) (string, error) {
	if yParity > 1 {
		return "", fmt.Errorf("ethereum type2 signature y parity must be 0 or 1")
	}
	if rItem.list || sItem.list {
		return "", fmt.Errorf("ethereum type2 signature r and s must be byte strings")
	}
	if len(rItem.raw) == 0 || len(rItem.raw) > 32 || len(sItem.raw) == 0 || len(sItem.raw) > 32 {
		return "", fmt.Errorf("ethereum type2 signature r and s must fit 32 bytes")
	}
	compact := make([]byte, 65)
	compact[0] = byte(27 + yParity)
	copy(compact[1+32-len(rItem.raw):33], rItem.raw)
	copy(compact[33+32-len(sItem.raw):65], sItem.raw)
	return "0x" + hex.EncodeToString(compact), nil
}

type rlpItem struct {
	raw      []byte
	children []rlpItem
	list     bool
}

func rlpDecodeItem(input []byte) (rlpItem, []byte, error) {
	if len(input) == 0 {
		return rlpItem{}, nil, fmt.Errorf("empty rlp input")
	}
	prefix := input[0]
	switch {
	case prefix < 0x80:
		return rlpItem{raw: []byte{prefix}}, input[1:], nil
	case prefix <= 0xb7:
		length := int(prefix - 0x80)
		if len(input)-1 < length {
			return rlpItem{}, nil, fmt.Errorf("short rlp string")
		}
		return rlpItem{raw: append([]byte(nil), input[1:1+length]...)}, input[1+length:], nil
	case prefix <= 0xbf:
		lengthOfLength := int(prefix - 0xb7)
		length, err := rlpPayloadLength(input[1:], lengthOfLength)
		if err != nil {
			return rlpItem{}, nil, err
		}
		start := 1 + lengthOfLength
		if len(input)-start < length {
			return rlpItem{}, nil, fmt.Errorf("short long rlp string")
		}
		return rlpItem{raw: append([]byte(nil), input[start:start+length]...)}, input[start+length:], nil
	case prefix <= 0xf7:
		length := int(prefix - 0xc0)
		if len(input)-1 < length {
			return rlpItem{}, nil, fmt.Errorf("short rlp list")
		}
		children, err := rlpDecodeListPayload(input[1 : 1+length])
		if err != nil {
			return rlpItem{}, nil, err
		}
		return rlpItem{list: true, children: children}, input[1+length:], nil
	default:
		lengthOfLength := int(prefix - 0xf7)
		length, err := rlpPayloadLength(input[1:], lengthOfLength)
		if err != nil {
			return rlpItem{}, nil, err
		}
		start := 1 + lengthOfLength
		if len(input)-start < length {
			return rlpItem{}, nil, fmt.Errorf("short long rlp list")
		}
		children, err := rlpDecodeListPayload(input[start : start+length])
		if err != nil {
			return rlpItem{}, nil, err
		}
		return rlpItem{list: true, children: children}, input[start+length:], nil
	}
}

func rlpPayloadLength(input []byte, lengthOfLength int) (int, error) {
	if lengthOfLength == 0 || lengthOfLength > 8 || len(input) < lengthOfLength {
		return 0, fmt.Errorf("invalid rlp length")
	}
	var length uint64
	for _, b := range input[:lengthOfLength] {
		length = length<<8 | uint64(b)
	}
	if length > uint64(math.MaxInt) {
		return 0, fmt.Errorf("rlp payload is too large")
	}
	return int(length), nil
}

func rlpDecodeListPayload(payload []byte) ([]rlpItem, error) {
	items := make([]rlpItem, 0)
	for len(payload) > 0 {
		item, rest, err := rlpDecodeItem(payload)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		payload = rest
	}
	return items, nil
}

func rlpUint64(item rlpItem, field string) (uint64, error) {
	if item.list {
		return 0, fmt.Errorf("%s must be an rlp string", field)
	}
	if len(item.raw) > 8 {
		return 0, fmt.Errorf("%s does not fit uint64", field)
	}
	if len(item.raw) > 1 && item.raw[0] == 0 {
		return 0, fmt.Errorf("%s has a leading zero", field)
	}
	var value uint64
	for _, b := range item.raw {
		value = value<<8 | uint64(b)
	}
	return value, nil
}

func rlpAddress(item rlpItem) (string, error) {
	if item.list {
		return "", fmt.Errorf("ethereum type2 destination must be an address")
	}
	if len(item.raw) == 0 {
		return "", fmt.Errorf("ethereum type2 contract creation is not supported")
	}
	if len(item.raw) != 20 {
		return "", fmt.Errorf("ethereum type2 destination must be 20 bytes")
	}
	return "0x" + hex.EncodeToString(item.raw), nil
}

func rlpEncodeList(items ...[]byte) []byte {
	payloadLen := 0
	for _, item := range items {
		payloadLen += len(item)
	}
	output := rlpEncodeLength(0xc0, payloadLen)
	for _, item := range items {
		output = append(output, item...)
	}
	return output
}

func rlpEncodeUint64(value uint64) []byte {
	if value == 0 {
		return rlpEncodeBytes(nil)
	}
	var raw [8]byte
	i := len(raw)
	for value > 0 {
		i--
		raw[i] = byte(value)
		value >>= 8
	}
	return rlpEncodeBytes(raw[i:])
}

func rlpEncodeBytes(raw []byte) []byte {
	if len(raw) == 1 && raw[0] < 0x80 {
		return append([]byte(nil), raw...)
	}
	output := rlpEncodeLength(0x80, len(raw))
	output = append(output, raw...)
	return output
}

func rlpEncodeLength(offset byte, length int) []byte {
	if length <= 55 {
		return []byte{offset + byte(length)}
	}
	var raw [8]byte
	i := len(raw)
	value := length
	for value > 0 {
		i--
		raw[i] = byte(value)
		value >>= 8
	}
	output := []byte{offset + 55 + byte(len(raw)-i)}
	output = append(output, raw[i:]...)
	return output
}

func ethereumAddressBytes(address string) ([]byte, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(address, "0x"))
	if err != nil {
		return nil, fmt.Errorf("invalid ethereum address")
	}
	if len(raw) != 20 {
		return nil, fmt.Errorf("ethereum address must be 20 bytes")
	}
	return raw, nil
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
	if tx.Signature != "" {
		if err := ValidateCanonicalSignature("raw transaction signature", tx.Signature); err != nil {
			return err
		}
	}
	if tx.PaymasterSignature != "" {
		if err := ValidateCanonicalSignature("raw paymaster signature", tx.PaymasterSignature); err != nil {
			return err
		}
	}
	for _, authorization := range tx.Authorizations {
		if strings.TrimSpace(authorization.Signer) == "" || strings.TrimSpace(authorization.Signature) == "" {
			return fmt.Errorf("raw transaction missing authorization signature")
		}
		if err := ValidateCanonicalSignature("raw authorization signature", authorization.Signature); err != nil {
			return err
		}
	}
	return nil
}
