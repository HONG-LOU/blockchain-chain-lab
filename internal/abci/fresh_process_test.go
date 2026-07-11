package abci

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"

	chainproof "chainlab/pkg/proof"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

const abciVectorPrefix = "CHAINLAB_ABCI_VECTOR="
const abciV3VectorPrefix = "CHAINLAB_ABCI_V3_VECTOR="
const abciV4VectorPrefix = "CHAINLAB_ABCI_V4_VECTOR="
const abciV5VectorPrefix = "CHAINLAB_ABCI_V5_VECTOR="

const expectedChainLabABCIVectorV1 = `{"app_hash":"fea027d18fb4869ea5bb4c54b585b3a3a5a98a47d3573f9347f4b86caa84f6c1","result":{"code":0,"data":"eyJ0eF9oYXNoIjoiMHg3OWUxNTgzOTU3NjE5N2M5MDcyNjY4YWE5MzAxNGI5N2U4ZjQxYjc4Mjc3OTZhNjlhNDk3YzYzZWQxOTExZDY5Iiwic3VjY2VzcyI6dHJ1ZSwiZ2FzX3VzZWQiOjIxMDAwLCJiYXNlX2ZlZV9wZXJfZ2FzIjoxLCJlZmZlY3RpdmVfZ2FzX3ByaWNlIjoxLCJiYXNlX2ZlZV9idXJuZWQiOjIxMDAwLCJldmVudHMiOlt7InR5cGUiOiJ0cmFuc2ZlciIsImF0dHJpYnV0ZXMiOnsiZnJvbSI6IjB4N2U1ZjQ1NTIwOTFhNjkxMjVkNWRmY2I3YjhjMjY1OTAyOTM5NWJkZiIsInRvIjoiMHhiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiIn19XX0=","log":"","info":"","gas_wanted":"21000","gas_used":"21000","events":[{"type":"transfer","attributes":[{"key":"from","value":"0x7e5f4552091a69125d5dfcb7b8c2659029395bdf","index":true},{"key":"to","value":"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","index":true}]}],"codespace":""},"commitment":{"protocol":"chainlab-v1","chain_id":"chainlab-abci-vector","height":1,"block_hash":"0x0101010101010101010101010101010101010101010101010101010101010101","proposer":"0x7e5f4552091a69125d5dfcb7b8c2659029395bdf","gas_limit":30000000,"gas_used":21000,"base_fee_per_gas":1,"next_base_fee_per_gas":1,"tx_root":"0x925be028adc6a0b20444aecd210b45e693c7876f3ddcb45b693eb33f42404ce8","receipt_root":"0xef2f4cabdab121aeeada33c560d0ff0ad90acfc003db44e672ef87d48a39286c","evidence_root":"0x518674ab2b227e5f11e9084f615d57663cde47bce1ba168b4c19c7ee22a73d70","state_root":"0x9871ab27e2c03fd3fb2f4dd15bb81bef8c56849dbef284e674e86713f2a93b26"}}`

const expectedChainLabABCIVectorV3 = `{"app_hash":"f34f75650bebf1d0fb62ea65a4324552e88a13422f6fd25f8b3cd274dff3009c","tx_root":"0xae390418354ddb7a321eaad9939ef0c1217e63c951f4cf7b01c5f68b05aee39a","receipt_root":"0x51032fa373bbfb061f55c519ca4eaf81b06841dce2719973a2a1a214a5e51a31","state_root":"0xc511ef27ce9ecee91850eab2e21399eb2a5a630c66f9e4bfa9ea750b221daba9","transaction_depth":0,"receipt_depth":0,"state_depth":3,"state_key":"account:MHhiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJi"}`

const expectedChainLabABCIVectorV4 = `{"app_hash":"cf2f646c6ffb05f4b382c04ee7315e011b2caa832c73b7744b7ee2d206568f84","tx_root":"0xae96b32f349ab8e5445567c55f57ba93d57cd12403ead76562828d40d5bd9d44","receipt_root":"0xa56fae7db2952abba0ce0ddfa075c9256c6ca2e70e44b0708e8e42f17b6b1f2f","state_root":"0xe0de12dfcf4f48d46f52712365de32fe82f84723c0ea8add9c72ee4748cb018f","membership_siblings":2,"non_membership_siblings":2,"membership_key":"account:MHhiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJi"}`

const expectedChainLabABCIVectorV5 = `{"app_hash":"0e6c2ce770f5bbb99a569efff49cb0a537716222ef62a85385e1ef57e2ff3895","state_root":"0x16117897c99a44157f0450049cd300189076c1b63f6ba4c0e0253d660bac6946","previous_state_root":"0xefc35e8f5a1552ba6dd032051a385d294a5d60d038e851221c4e1f12ae6f55f3","validator_root":"0x6d773db43c4b9e8cfb0390e3c36f31865ff5fb5cfc9d6c7fac8d51908ea29dc4","offence_count":1,"legacy_retained":true}`

func TestABCIExecutionMatchesAcrossFreshProcesses(t *testing.T) {
	if os.Getenv("CHAINLAB_ABCI_VECTOR_CHILD") == "1" {
		encoded, err := json.Marshal(executeDeterministicABCIVector(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Log(abciVectorPrefix + string(encoded))
		return
	}
	first := runABCIVectorProcess(t)
	second := runABCIVectorProcess(t)
	if first != second {
		t.Fatalf("fresh-process ABCI vectors differ:\nfirst:  %s\nsecond: %s", first, second)
	}
	if first != expectedChainLabABCIVectorV1 {
		t.Fatalf("chainlab-v1 ABCI vector changed:\n got: %s\nwant: %s", first, expectedChainLabABCIVectorV1)
	}
}

func TestABCIV3ProofRootsMatchAcrossFreshProcesses(t *testing.T) {
	if os.Getenv("CHAINLAB_ABCI_V3_VECTOR_CHILD") == "1" {
		encoded, err := json.Marshal(executeDeterministicABCIV3Vector(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Log(abciV3VectorPrefix + string(encoded))
		return
	}
	first := runABCIV3VectorProcess(t)
	second := runABCIV3VectorProcess(t)
	if first != second {
		t.Fatalf("fresh-process v3 vectors differ:\nfirst:  %s\nsecond: %s", first, second)
	}
	if first != expectedChainLabABCIVectorV3 {
		t.Fatalf("chainlab-v3 ABCI vector changed:\n got: %s\nwant: %s", first, expectedChainLabABCIVectorV3)
	}
}

func TestABCIV4SparseRootsMatchAcrossFreshProcesses(t *testing.T) {
	if os.Getenv("CHAINLAB_ABCI_V4_VECTOR_CHILD") == "1" {
		encoded, err := json.Marshal(executeDeterministicABCIV4Vector(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Log(abciV4VectorPrefix + string(encoded))
		return
	}
	first := runABCIV4VectorProcess(t)
	second := runABCIV4VectorProcess(t)
	if first != second {
		t.Fatalf("fresh-process v4 vectors differ:\nfirst:  %s\nsecond: %s", first, second)
	}
	if first != expectedChainLabABCIVectorV4 {
		t.Fatalf("chainlab-v4 ABCI vector changed:\n got: %s\nwant: %s", first, expectedChainLabABCIVectorV4)
	}
}

func TestABCIV5OffenceCompactionMatchesAcrossFreshProcesses(t *testing.T) {
	if os.Getenv("CHAINLAB_ABCI_V5_VECTOR_CHILD") == "1" {
		encoded, err := json.Marshal(executeDeterministicABCIV5Vector(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Log(abciV5VectorPrefix + string(encoded))
		return
	}
	first := runABCIV5VectorProcess(t)
	second := runABCIV5VectorProcess(t)
	if first != second {
		t.Fatalf("fresh-process v5 vectors differ:\nfirst:  %s\nsecond: %s", first, second)
	}
	if first != expectedChainLabABCIVectorV5 {
		t.Fatalf("chainlab-v5 ABCI vector changed:\n got: %s\nwant: %s", first, expectedChainLabABCIVectorV5)
	}
}

type deterministicABCIVector struct {
	AppHash    string                  `json:"app_hash"`
	Result     *abcitypes.ExecTxResult `json:"result"`
	Commitment applicationCommitment   `json:"commitment"`
}

type deterministicABCIV3Vector struct {
	AppHash          string `json:"app_hash"`
	TxRoot           string `json:"tx_root"`
	ReceiptRoot      string `json:"receipt_root"`
	StateRoot        string `json:"state_root"`
	TransactionDepth int    `json:"transaction_depth"`
	ReceiptDepth     int    `json:"receipt_depth"`
	StateDepth       int    `json:"state_depth"`
	StateKey         string `json:"state_key"`
}

type deterministicABCIV4Vector struct {
	AppHash               string `json:"app_hash"`
	TxRoot                string `json:"tx_root"`
	ReceiptRoot           string `json:"receipt_root"`
	StateRoot             string `json:"state_root"`
	MembershipSiblings    int    `json:"membership_siblings"`
	NonMembershipSiblings int    `json:"non_membership_siblings"`
	MembershipKey         string `json:"membership_key"`
}

type deterministicABCIV5Vector struct {
	AppHash           string `json:"app_hash"`
	StateRoot         string `json:"state_root"`
	PreviousStateRoot string `json:"previous_state_root"`
	ValidatorRoot     string `json:"validator_root"`
	OffenceCount      int    `json:"offence_count"`
	LegacyRetained    bool   `json:"legacy_retained"`
}

func executeDeterministicABCIVector(t *testing.T) deterministicABCIVector {
	t.Helper()
	key, err := chaincrypto.PrivateKeyFromHex(strings.Repeat("0", 63) + "1")
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	store := state.NewStore()
	store.SetBalance(sender, 1_000_000)
	if err := store.SetValidators([]string{sender}); err != nil {
		t.Fatal(err)
	}
	genesis, err := NewGenesisDocument("chainlab-abci-vector", types.DefaultBlockGasLimit, store)
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewApplication(Config{Genesis: genesis})
	if err != nil {
		t.Fatal(err)
	}
	compressed := key.PubKey().SerializeCompressed()
	genesisBytes, err := genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.InitChain(context.Background(), &abcitypes.RequestInitChain{
		ChainId:         genesis.ChainID,
		ConsensusParams: consensusParams(genesis.BlockGasLimit),
		Validators:      []abcitypes.ValidatorUpdate{abcitypes.UpdateValidator(compressed, 1, cmtsecp256k1.KeyType)},
		AppStateBytes:   genesisBytes,
		InitialHeight:   1,
	}); err != nil {
		t.Fatal(err)
	}
	tx := signFixtureTransaction(t, key, types.Transaction{
		ChainID: genesis.ChainID, Type: types.TxTransfer, From: sender,
		To: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Nonce: 0, Value: 9, GasLimit: 21_000, GasPrice: 1,
	})
	response, err := application.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Txs: [][]byte{rawFixtureTransaction(t, tx)}, Hash: blockHash(1), Height: 1,
		ProposerAddress: cloneBytes(cmtsecp256k1.PubKey(compressed).Address()),
	})
	if err != nil {
		t.Fatal(err)
	}
	return deterministicABCIVector{
		AppHash:    hex.EncodeToString(response.AppHash),
		Result:     response.TxResults[0],
		Commitment: application.candidate.state.commitment,
	}
}

func executeDeterministicABCIV3Vector(t *testing.T) deterministicABCIV3Vector {
	t.Helper()
	key, err := chaincrypto.PrivateKeyFromHex(strings.Repeat("0", 63) + "1")
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := state.NewStore()
	store.SetBalance(sender, 1_000_000)
	if err := store.AddStake(sender, 10_000); err != nil {
		t.Fatal(err)
	}
	if err := store.SetValidators([]string{sender}); err != nil {
		t.Fatal(err)
	}
	policy := DefaultValidatorPolicy()
	policy.EpochLength = 4
	genesis, err := NewGenesisDocumentV2("chainlab-abci-v3-vector", types.DefaultBlockGasLimit, store, policy)
	if err != nil {
		t.Fatal(err)
	}
	genesis.Upgrades = []ProtocolUpgrade{{Height: 2, Protocol: ProtocolVersionV3}}
	application, err := NewApplication(Config{Genesis: genesis, MaxAppVersion: AppVersionV3})
	if err != nil {
		t.Fatal(err)
	}
	compressed := key.PubKey().SerializeCompressed()
	genesisBytes, err := genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	params := consensusParams(genesis.BlockGasLimit)
	params.Evidence.MaxAgeNumBlocks = policy.EvidenceMaxAgeNumBlocks
	params.Evidence.MaxAgeDuration = time.Duration(policy.EvidenceMaxAgeDurationNanos)
	params.Version = &cmtproto.VersionParams{App: AppVersionV2}
	if _, err := application.InitChain(context.Background(), &abcitypes.RequestInitChain{
		ChainId: genesis.ChainID, ConsensusParams: params,
		Validators:    []abcitypes.ValidatorUpdate{abcitypes.UpdateValidator(compressed, 1, cmtsecp256k1.KeyType)},
		AppStateBytes: genesisBytes, InitialHeight: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(1), Height: 1, Time: upgradeBlockTime(1),
		ProposerAddress: cloneBytes(cmtsecp256k1.PubKey(compressed).Address()),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	tx := signFixtureTransaction(t, key, types.Transaction{
		ChainID: genesis.ChainID, Type: types.TxTransfer, From: sender,
		To: receiver, Nonce: 0, Value: 9, GasLimit: 21_000, GasPrice: 1,
	})
	response, err := application.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Txs: [][]byte{rawFixtureTransaction(t, tx)}, Hash: blockHash(2), Height: 2,
		Time: upgradeBlockTime(2), ProposerAddress: cloneBytes(cmtsecp256k1.PubKey(compressed).Address()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	txProof := proofEnvelopeForVector(t, application, "/proof/transaction", "0")
	receiptProof := proofEnvelopeForVector(t, application, "/proof/receipt", "0")
	stateProof := proofEnvelopeForVector(t, application, "/proof/account", receiver)
	return deterministicABCIV3Vector{
		AppHash: hex.EncodeToString(response.AppHash), TxRoot: application.committed.commitment.TxRoot,
		ReceiptRoot: application.committed.commitment.ReceiptRoot, StateRoot: application.committed.commitment.StateRoot,
		TransactionDepth: len(txProof.Proof.Siblings), ReceiptDepth: len(receiptProof.Proof.Siblings),
		StateDepth: len(stateProof.Proof.Siblings), StateKey: stateProof.Key,
	}
}

func executeDeterministicABCIV4Vector(t *testing.T) deterministicABCIV4Vector {
	t.Helper()
	key, err := chaincrypto.PrivateKeyFromHex(strings.Repeat("0", 63) + "1")
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	missing := "0x1111111111111111111111111111111111111111"
	store := state.NewStore()
	store.SetBalance(sender, 1_000_000)
	if err := store.AddStake(sender, 10_000); err != nil {
		t.Fatal(err)
	}
	if err := store.SetValidators([]string{sender}); err != nil {
		t.Fatal(err)
	}
	policy := DefaultValidatorPolicy()
	policy.EpochLength = 4
	genesis, err := NewGenesisDocumentV2("chainlab-abci-v4-vector", types.DefaultBlockGasLimit, store, policy)
	if err != nil {
		t.Fatal(err)
	}
	genesis.Upgrades = []ProtocolUpgrade{
		{Height: 2, Protocol: ProtocolVersionV3},
		{Height: 4, Protocol: ProtocolVersionV4},
	}
	application, err := NewApplication(Config{Genesis: genesis, MaxAppVersion: AppVersionV4})
	if err != nil {
		t.Fatal(err)
	}
	compressed := key.PubKey().SerializeCompressed()
	genesisBytes, err := genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	params := consensusParams(genesis.BlockGasLimit)
	params.Evidence.MaxAgeNumBlocks = policy.EvidenceMaxAgeNumBlocks
	params.Evidence.MaxAgeDuration = time.Duration(policy.EvidenceMaxAgeDurationNanos)
	params.Version = &cmtproto.VersionParams{App: AppVersionV2}
	if _, err := application.InitChain(context.Background(), &abcitypes.RequestInitChain{
		ChainId: genesis.ChainID, ConsensusParams: params,
		Validators:    []abcitypes.ValidatorUpdate{abcitypes.UpdateValidator(compressed, 1, cmtsecp256k1.KeyType)},
		AppStateBytes: genesisBytes, InitialHeight: 1,
	}); err != nil {
		t.Fatal(err)
	}
	for height := int64(1); height <= 3; height++ {
		if _, err := application.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
			Hash: blockHash(byte(height)), Height: height, Time: upgradeBlockTime(height),
			ProposerAddress: cloneBytes(cmtsecp256k1.PubKey(compressed).Address()),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := application.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
			t.Fatal(err)
		}
	}
	tx := signFixtureTransaction(t, key, types.Transaction{
		ChainID: genesis.ChainID, Type: types.TxTransfer, From: sender,
		To: receiver, Nonce: 0, Value: 9, GasLimit: 21_000, GasPrice: 1,
	})
	response, err := application.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Txs: [][]byte{rawFixtureTransaction(t, tx)}, Hash: blockHash(4), Height: 4,
		Time: upgradeBlockTime(4), ProposerAddress: cloneBytes(cmtsecp256k1.PubKey(compressed).Address()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	membership := sparseEnvelopeForVector(t, application, "/proof/account", receiver)
	nonMembership := sparseEnvelopeForVector(t, application, "/proof/account", missing)
	return deterministicABCIV4Vector{
		AppHash: hex.EncodeToString(response.AppHash), TxRoot: application.committed.commitment.TxRoot,
		ReceiptRoot:           application.committed.commitment.ReceiptRoot,
		StateRoot:             application.committed.commitment.StateRoot,
		MembershipSiblings:    len(membership.Proof.Siblings),
		NonMembershipSiblings: len(nonMembership.Proof.Siblings), MembershipKey: membership.Key,
	}
}

func executeDeterministicABCIV5Vector(t *testing.T) deterministicABCIV5Vector {
	t.Helper()
	keys := make([]chaincrypto.PrivateKey, 2)
	accounts := make([]string, 2)
	addresses := make([][]byte, 2)
	updates := make([]abcitypes.ValidatorUpdate, 2)
	store := state.NewStore()
	for index := range keys {
		key, err := chaincrypto.PrivateKeyFromHex(strings.Repeat("0", 63) + strconv.Itoa(index+1))
		if err != nil {
			t.Fatal(err)
		}
		compressed := key.PubKey().SerializeCompressed()
		account := chaincrypto.AddressFromPrivateKey(key)
		keys[index] = key
		accounts[index] = account
		addresses[index] = cloneBytes(cmtsecp256k1.PubKey(compressed).Address())
		updates[index] = abcitypes.UpdateValidator(compressed, 1, cmtsecp256k1.KeyType)
		store.SetBalance(account, 1_000_000)
		if err := store.AddStake(account, 10_000); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetValidators(accounts); err != nil {
		t.Fatal(err)
	}
	policy := ValidatorPolicy{
		EpochLength: 100, DuplicateVoteSlashBasisPoints: 500,
		LightClientAttackSlashBasisPoints: 10_000,
		EvidenceMaxAgeNumBlocks:           1,
		EvidenceMaxAgeDurationNanos:       int64(time.Second),
	}
	genesis, err := NewGenesisDocumentV2("chainlab-abci-v5-vector", types.DefaultBlockGasLimit, store, policy)
	if err != nil {
		t.Fatal(err)
	}
	genesis.Upgrades = []ProtocolUpgrade{
		{Height: 2, Protocol: ProtocolVersionV3},
		{Height: 3, Protocol: ProtocolVersionV4},
		{Height: 4, Protocol: ProtocolVersionV5},
	}
	application, err := NewApplication(Config{Genesis: genesis, MaxAppVersion: AppVersionV5})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	genesisBytes, err := genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	params := consensusParams(genesis.BlockGasLimit)
	params.Evidence.MaxAgeNumBlocks = policy.EvidenceMaxAgeNumBlocks
	params.Evidence.MaxAgeDuration = time.Duration(policy.EvidenceMaxAgeDurationNanos)
	params.Version = &cmtproto.VersionParams{App: AppVersionV2}
	if _, err := application.InitChain(context.Background(), &abcitypes.RequestInitChain{
		ChainId: genesis.ChainID, ConsensusParams: params, Validators: updates,
		AppStateBytes: genesisBytes, InitialHeight: 1,
	}); err != nil {
		t.Fatal(err)
	}
	for height := int64(1); height <= 4; height++ {
		var evidence []abcitypes.Misbehavior
		if height == 2 {
			evidence = deterministicV5Evidence(addresses[1], 1)
		}
		if height == 4 {
			evidence = deterministicV5Evidence(addresses[1], 3)
		}
		if _, err := application.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
			Hash: blockHash(byte(height)), Height: height, Time: validatorV2BlockTime(height),
			ProposerAddress: addresses[0], Misbehavior: evidence,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := application.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
			t.Fatal(err)
		}
	}
	previousStateRoot := application.committed.commitment.StateRoot
	response, err := application.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(5), Height: 5, Time: validatorV2BlockTime(5), ProposerAddress: addresses[0],
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	lifecycle, exists := application.committed.store.ValidatorLifecycle()
	if !exists {
		t.Fatal("v5 vector validator lifecycle is missing")
	}
	legacy, legacyRetained := lifecycle.Offences[state.ValidatorOffenceKey(accounts[1], 1)]
	legacyRetained = legacyRetained && !legacy.EvidenceTimePresent
	return deterministicABCIV5Vector{
		AppHash: hex.EncodeToString(response.AppHash), StateRoot: application.committed.commitment.StateRoot,
		PreviousStateRoot: previousStateRoot, ValidatorRoot: application.committed.commitment.ValidatorRoot,
		OffenceCount: len(lifecycle.Offences), LegacyRetained: legacyRetained,
	}
}

func deterministicV5Evidence(address []byte, height int64) []abcitypes.Misbehavior {
	return []abcitypes.Misbehavior{{
		Type:      abcitypes.MisbehaviorType_DUPLICATE_VOTE,
		Validator: abcitypes.Validator{Address: cloneBytes(address), Power: 1},
		Height:    height, Time: validatorV2BlockTime(height), TotalVotingPower: 2,
	}}
}

func proofEnvelopeForVector(t *testing.T, application *Application, path string, data string) chainproof.Envelope {
	t.Helper()
	response, err := application.Query(context.Background(), &abcitypes.RequestQuery{Path: path, Data: []byte(data)})
	if err != nil || response.Code != CodeOK {
		t.Fatalf("proof vector query response=%+v err=%v", response, err)
	}
	var envelope chainproof.Envelope
	if err := json.Unmarshal(response.Value, &envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := chainproof.VerifyEnvelope(
		envelope.Root, envelope.Height, envelope.Kind, envelope.Key, envelope,
	); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func sparseEnvelopeForVector(
	t *testing.T,
	application *Application,
	path string,
	data string,
) chainproof.SparseEnvelope {
	t.Helper()
	response, err := application.Query(context.Background(), &abcitypes.RequestQuery{Path: path, Data: []byte(data)})
	if err != nil || response.Code != CodeOK {
		t.Fatalf("sparse proof vector query response=%+v err=%v", response, err)
	}
	var envelope chainproof.SparseEnvelope
	if err := json.Unmarshal(response.Value, &envelope); err != nil {
		t.Fatal(err)
	}
	if _, _, err := chainproof.VerifySparseEnvelope(
		envelope.Root, envelope.Height, envelope.Kind, envelope.Key, envelope,
	); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func runABCIVectorProcess(t *testing.T) string {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestABCIExecutionMatchesAcrossFreshProcesses$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), "CHAINLAB_ABCI_VECTOR_CHILD=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("ABCI vector subprocess failed: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if marker := strings.Index(line, abciVectorPrefix); marker >= 0 {
			return strings.TrimSpace(line[marker+len(abciVectorPrefix):])
		}
	}
	t.Fatalf("ABCI vector subprocess returned no vector:\n%s", output)
	return ""
}

func runABCIV3VectorProcess(t *testing.T) string {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestABCIV3ProofRootsMatchAcrossFreshProcesses$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), "CHAINLAB_ABCI_V3_VECTOR_CHILD=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("ABCI v3 vector subprocess failed: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if marker := strings.Index(line, abciV3VectorPrefix); marker >= 0 {
			return strings.TrimSpace(line[marker+len(abciV3VectorPrefix):])
		}
	}
	t.Fatalf("ABCI v3 vector subprocess returned no vector:\n%s", output)
	return ""
}

func runABCIV4VectorProcess(t *testing.T) string {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestABCIV4SparseRootsMatchAcrossFreshProcesses$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), "CHAINLAB_ABCI_V4_VECTOR_CHILD=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("ABCI v4 vector subprocess failed: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if index := strings.Index(line, abciV4VectorPrefix); index >= 0 {
			return strings.TrimSpace(line[index+len(abciV4VectorPrefix):])
		}
	}
	t.Fatalf("ABCI v4 vector subprocess did not emit vector:\n%s", output)
	return ""
}

func runABCIV5VectorProcess(t *testing.T) string {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestABCIV5OffenceCompactionMatchesAcrossFreshProcesses$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), "CHAINLAB_ABCI_V5_VECTOR_CHILD=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("ABCI v5 vector subprocess failed: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if index := strings.Index(line, abciV5VectorPrefix); index >= 0 {
			return strings.TrimSpace(line[index+len(abciV5VectorPrefix):])
		}
	}
	t.Fatalf("ABCI v5 vector subprocess did not emit vector:\n%s", output)
	return ""
}
