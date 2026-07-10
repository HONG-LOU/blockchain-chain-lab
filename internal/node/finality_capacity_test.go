package node

import (
	"fmt"
	"math"
	"strings"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/types"
)

func TestNodeConfigCapacityBoundaries(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	chainID := strings.Repeat("a", types.MaxChainIDBytes)
	maximum := finalityCapacityValidators(proposer, types.MaxValidators)
	n, err := NewDevelopment(Config{ChainID: chainID, ProposerKey: key, Validators: maximum, GenesisTimeUnix: DeterministicDevGenesisTimeUnix})
	if err != nil {
		t.Fatalf("maximum node config rejected: %v", err)
	}
	if got := len(n.Validators()); got != types.MaxValidators {
		t.Fatalf("node validator count = %d", got)
	}

	overflow := append(append([]string(nil), maximum...), finalityCapacityUnusedValidator(maximum))
	rejected, err := NewDevelopment(Config{ChainID: chainID, ProposerKey: key, Validators: overflow, GenesisTimeUnix: DeterministicDevGenesisTimeUnix})
	if err == nil || rejected != nil || !strings.Contains(err.Error(), fmt.Sprintf("exceeds %d", types.MaxValidators)) {
		t.Fatalf("overflow node config accepted: node=%v error=%v", rejected, err)
	}

	for _, invalidChainID := range []string{
		strings.Repeat("a", types.MaxChainIDBytes+1),
		"chain/lab",
	} {
		rejected, err := NewDevelopment(Config{ChainID: invalidChainID, ProposerKey: key, GenesisTimeUnix: DeterministicDevGenesisTimeUnix})
		if err == nil || rejected != nil {
			t.Fatalf("invalid chain id %q accepted: node=%v error=%v", invalidChainID, rejected, err)
		}
	}
}

func TestMaximumFinalityCertificateFitsReservedBlockCapacity(t *testing.T) {
	chainID := strings.Repeat("z", types.MaxChainIDBytes)
	base := finalityCapacityBaseBlock(chainID)
	initialSize, err := canonicalBlockSize(base)
	if err != nil {
		t.Fatal(err)
	}
	if initialSize >= MaxUncertifiedBlockBytes {
		t.Fatalf("base fixture overhead = %d, reserved base limit = %d", initialSize, MaxUncertifiedBlockBytes)
	}
	paddingBytes := MaxUncertifiedBlockBytes - initialSize
	base.Receipts[0].Events[0].Attributes["padding"] = strings.Repeat("x", int(paddingBytes))
	base.Header.ReceiptRoot = types.ReceiptRoot(base.Receipts)
	baseSize, err := canonicalBlockSize(base)
	if err != nil {
		t.Fatal(err)
	}
	if baseSize != MaxUncertifiedBlockBytes {
		t.Fatalf("calibrated base size = %d, want %d", baseSize, MaxUncertifiedBlockBytes)
	}

	certificate := maximumFinalityCapacityCertificate(chainID, base.Header.Height, base.Hash())
	certificateSize, err := canonicalFinalityCertificateSize(certificate)
	if err != nil {
		t.Fatal(err)
	}
	canonicalCertificate, err := hash.CanonicalBytes(certificate)
	if err != nil {
		t.Fatal(err)
	}
	if certificateSize != uint64(len(canonicalCertificate)) {
		t.Fatalf("certificate size = %d, canonical bytes = %d", certificateSize, len(canonicalCertificate))
	}
	if len(certificate.Signatures) != types.MaxValidators {
		t.Fatalf("certificate signature count = %d", len(certificate.Signatures))
	}
	if certificateSize >= MaxFinalityCertificateBytes {
		t.Fatalf("maximum certificate size = %d, reserve = %d", certificateSize, MaxFinalityCertificateBytes)
	}

	certified := base
	certified.FinalityCertificate = certificate
	if err := validateBlockSize(certified); err != nil {
		t.Fatalf("reserved base plus maximum certificate rejected: %v", err)
	}
	certifiedSize, err := canonicalBlockSize(certified)
	if err != nil {
		t.Fatal(err)
	}
	if certifiedSize > MaxBlockBytes {
		t.Fatalf("certified block size = %d, block limit = %d", certifiedSize, MaxBlockBytes)
	}
	t.Logf("maximum certificate = %d bytes; certified boundary block = %d bytes", certificateSize, certifiedSize)

	overflowBase := base
	overflowBase.Receipts = append([]types.Receipt(nil), base.Receipts...)
	overflowBase.Receipts[0].Events = append([]types.Event(nil), base.Receipts[0].Events...)
	overflowBase.Receipts[0].Events[0].Attributes = map[string]string{
		"padding": strings.Repeat("x", int(paddingBytes)+1),
	}
	overflowBase.Header.ReceiptRoot = types.ReceiptRoot(overflowBase.Receipts)
	overflowBase.FinalityCertificate = maximumFinalityCapacityCertificate(chainID, overflowBase.Header.Height, overflowBase.Hash())
	if err := validateBlockSize(overflowBase); err == nil || !strings.Contains(err.Error(), "reserved base limit") {
		t.Fatalf("base reserve overflow error = %v", err)
	}
}

func finalityCapacityBaseBlock(chainID string) types.Block {
	compactSignature := "0x1b" + strings.Repeat("01", 64)
	tx := types.Transaction{
		ChainID:   chainID,
		Type:      types.TxTransfer,
		From:      "0x1111111111111111111111111111111111111111",
		To:        "0x2222222222222222222222222222222222222222",
		GasLimit:  21_000,
		GasPrice:  1,
		Signature: compactSignature,
	}
	receipt := types.Receipt{
		TxHash:            tx.Hash(),
		Success:           true,
		GasUsed:           1,
		BaseFeePerGas:     1,
		EffectiveGasPrice: 1,
		BaseFeeBurned:     1,
		Events: []types.Event{{
			Type:       "capacity",
			Attributes: map[string]string{"padding": ""},
		}},
	}
	transactions := []types.Transaction{tx}
	receipts := []types.Receipt{receipt}
	return types.Block{
		Header: types.BlockHeader{
			ChainID:       chainID,
			Height:        math.MaxUint64,
			ParentHash:    "0x" + strings.Repeat("3", 64),
			TimeUnix:      math.MaxInt64,
			Proposer:      tx.From,
			GasLimit:      types.DefaultBlockGasLimit,
			GasUsed:       1,
			BaseFeePerGas: types.InitialBaseFeePerGas,
			TxRoot:        types.TransactionRoot(transactions),
			ReceiptRoot:   types.ReceiptRoot(receipts),
			StateRoot:     "0x" + strings.Repeat("4", 64),
		},
		Transactions: transactions,
		Receipts:     receipts,
		Signature:    compactSignature,
	}
}

func maximumFinalityCapacityCertificate(chainID string, height uint64, blockHash string) *types.FinalityCertificate {
	compactSignature := "0x1b" + strings.Repeat("01", 64)
	signatures := make([]types.FinalitySignature, types.MaxValidators)
	for index := range signatures {
		signatures[index] = types.FinalitySignature{
			ChainID:   chainID,
			Height:    height,
			BlockHash: blockHash,
			Validator: fmt.Sprintf("0x%040x", index+1),
			Signature: compactSignature,
		}
	}
	return &types.FinalityCertificate{
		ChainID:    chainID,
		Height:     height,
		BlockHash:  blockHash,
		Signatures: signatures,
	}
}

func finalityCapacityValidators(proposer string, count int) []string {
	validators := make([]string, 0, count)
	validators = append(validators, proposer)
	for sequence := 1; len(validators) < count; sequence++ {
		candidate := fmt.Sprintf("0x%040x", sequence)
		if candidate != proposer {
			validators = append(validators, candidate)
		}
	}
	return validators
}

func finalityCapacityUnusedValidator(validators []string) string {
	used := make(map[string]struct{}, len(validators))
	for _, validator := range validators {
		used[validator] = struct{}{}
	}
	for sequence := len(validators) + 1; ; sequence++ {
		candidate := fmt.Sprintf("0x%040x", sequence)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}
