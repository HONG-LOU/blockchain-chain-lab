package core_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"chainlab/internal/contracts"
	"chainlab/internal/core"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

const nativeVectorPrefix = "CHAINLAB_NATIVE_VECTOR="

const expectedChainLabProtocolNativeVectorV1 = `{"deploy":{"tx_hash":"0x82537ed521c4ef25b2018553e6fa0c1fbab6b2f15e7da7e205c5ae467b525534","success":true,"gas_used":82356,"effective_gas_price":1,"priority_fee_paid":82356,"events":[{"type":"contract.deployed","attributes":{"address":"0xd59da67bb7119dcaf92c7e2d5dcb850a130664ac","code_id":"counter.v1","creator":"0x7e5f4552091a69125d5dfcb7b8c2659029395bdf"}},{"type":"counter.initialized","attributes":{"count":"2"}}],"contract_address":"0xd59da67bb7119dcaf92c7e2d5dcb850a130664ac"},"call":{"tx_hash":"0x05a75f2099f6a0cfdfce033764d0d7d9b2bce464f8c09c7c055bc9e2858ae048","success":true,"gas_used":51453,"effective_gas_price":1,"priority_fee_paid":51453,"events":[{"type":"counter.incremented","attributes":{"count":"5"}}]},"failure":{"tx_hash":"0x7cba5b32204657e4168db813d5e772da1cef0197d29ccf438378410c54690156","success":false,"failure_code":"execution_reverted","gas_used":50566,"effective_gas_price":1,"priority_fee_paid":50566},"value":"5","read_gas":613,"root":"0xcfcb00d82f2a3c34bc0eae8b78d9f9b93bcc2f071e11f1da540f8d7449f0c8e1","metering_version":"chainlab-native-v1"}`

func TestNativeExecutionMatchesAcrossFreshProcesses(t *testing.T) {
	if os.Getenv("CHAINLAB_NATIVE_VECTOR_CHILD") == "1" {
		encoded, err := json.Marshal(executeDeterministicNativeVector(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Log(nativeVectorPrefix + string(encoded))
		return
	}
	first := runNativeVectorProcess(t)
	second := runNativeVectorProcess(t)
	if first != second {
		t.Fatalf("fresh-process native vectors differ:\nfirst:  %s\nsecond: %s", first, second)
	}
	if first != expectedChainLabProtocolNativeVectorV1 {
		t.Fatalf("chainlab-native-v1 protocol vector changed:\n got: %s\nwant: %s", first, expectedChainLabProtocolNativeVectorV1)
	}
}

type deterministicNativeVector struct {
	Deploy          types.Receipt `json:"deploy"`
	Call            types.Receipt `json:"call"`
	Failure         types.Receipt `json:"failure"`
	Value           string        `json:"value"`
	ReadGas         uint64        `json:"read_gas"`
	Root            string        `json:"root"`
	MeteringVersion string        `json:"metering_version"`
}

func executeDeterministicNativeVector(t *testing.T) deterministicNativeVector {
	t.Helper()
	key, err := chaincrypto.PrivateKeyFromHex(strings.Repeat("0", 63) + "1")
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	store := state.NewStore()
	store.SetBalance(sender, 10_000_000)
	runtime := contracts.NewRuntimeWithDefaults()
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", runtime)
	deploy := signedTx(t, key, types.Transaction{
		ChainID: "chainlab-local", Type: types.TxDeploy, From: sender,
		Nonce: 0, GasLimit: 100_000, GasPrice: 1,
		Payload: map[string]string{"code_id": "counter.v1", "initial": "2"},
	})
	deployReceipt, err := executor.Execute(store, deploy)
	if err != nil {
		t.Fatal(err)
	}
	call := signedTx(t, key, types.Transaction{
		ChainID: "chainlab-local", Type: types.TxCall, From: sender, To: deployReceipt.ContractAddress,
		Nonce: 1, GasLimit: 60_000, GasPrice: 1,
		Payload: map[string]string{"method": "increment", "amount": "3"},
	})
	callReceipt, err := executor.Execute(store, call)
	if err != nil {
		t.Fatal(err)
	}
	failure := signedTx(t, key, types.Transaction{
		ChainID: "chainlab-local", Type: types.TxCall, From: sender, To: deployReceipt.ContractAddress,
		Nonce: 2, GasLimit: 60_000, GasPrice: 1,
		Payload: map[string]string{"method": "increment", "amount": "bad"},
	})
	failureReceipt, err := executor.Execute(store, failure)
	if err != nil {
		t.Fatal(err)
	}
	if failureReceipt.Success || failureReceipt.FailureCode != types.ReceiptFailureExecutionReverted {
		t.Fatalf("failure receipt = %+v", failureReceipt)
	}
	value, readGas, err := runtime.ReadMetered(store, deployReceipt.ContractAddress, sender, "get", nil, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	return deterministicNativeVector{
		Deploy:          deployReceipt,
		Call:            callReceipt,
		Failure:         failureReceipt,
		Value:           value,
		ReadGas:         readGas,
		Root:            store.Root(),
		MeteringVersion: contracts.NativeMeteringVersion,
	}
}

func runNativeVectorProcess(t *testing.T) string {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestNativeExecutionMatchesAcrossFreshProcesses$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), "CHAINLAB_NATIVE_VECTOR_CHILD=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native vector subprocess failed: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if marker := strings.Index(line, nativeVectorPrefix); marker >= 0 {
			return strings.TrimSpace(line[marker+len(nativeVectorPrefix):])
		}
	}
	t.Fatalf("native vector subprocess returned no vector:\n%s", output)
	return ""
}
