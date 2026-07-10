package abci

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
)

const abciVectorPrefix = "CHAINLAB_ABCI_VECTOR="

const expectedChainLabABCIVectorV1 = `{"app_hash":"fea027d18fb4869ea5bb4c54b585b3a3a5a98a47d3573f9347f4b86caa84f6c1","result":{"code":0,"data":"eyJ0eF9oYXNoIjoiMHg3OWUxNTgzOTU3NjE5N2M5MDcyNjY4YWE5MzAxNGI5N2U4ZjQxYjc4Mjc3OTZhNjlhNDk3YzYzZWQxOTExZDY5Iiwic3VjY2VzcyI6dHJ1ZSwiZ2FzX3VzZWQiOjIxMDAwLCJiYXNlX2ZlZV9wZXJfZ2FzIjoxLCJlZmZlY3RpdmVfZ2FzX3ByaWNlIjoxLCJiYXNlX2ZlZV9idXJuZWQiOjIxMDAwLCJldmVudHMiOlt7InR5cGUiOiJ0cmFuc2ZlciIsImF0dHJpYnV0ZXMiOnsiZnJvbSI6IjB4N2U1ZjQ1NTIwOTFhNjkxMjVkNWRmY2I3YjhjMjY1OTAyOTM5NWJkZiIsInRvIjoiMHhiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiIn19XX0=","log":"","info":"","gas_wanted":"21000","gas_used":"21000","events":[{"type":"transfer","attributes":[{"key":"from","value":"0x7e5f4552091a69125d5dfcb7b8c2659029395bdf","index":true},{"key":"to","value":"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","index":true}]}],"codespace":""},"commitment":{"protocol":"chainlab-v1","chain_id":"chainlab-abci-vector","height":1,"block_hash":"0x0101010101010101010101010101010101010101010101010101010101010101","proposer":"0x7e5f4552091a69125d5dfcb7b8c2659029395bdf","gas_limit":30000000,"gas_used":21000,"base_fee_per_gas":1,"next_base_fee_per_gas":1,"tx_root":"0x925be028adc6a0b20444aecd210b45e693c7876f3ddcb45b693eb33f42404ce8","receipt_root":"0xef2f4cabdab121aeeada33c560d0ff0ad90acfc003db44e672ef87d48a39286c","evidence_root":"0x518674ab2b227e5f11e9084f615d57663cde47bce1ba168b4c19c7ee22a73d70","state_root":"0x9871ab27e2c03fd3fb2f4dd15bb81bef8c56849dbef284e674e86713f2a93b26"}}`

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

type deterministicABCIVector struct {
	AppHash    string                  `json:"app_hash"`
	Result     *abcitypes.ExecTxResult `json:"result"`
	Commitment applicationCommitment   `json:"commitment"`
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
