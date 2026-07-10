package core_test

import (
	"encoding/hex"
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

const wasmVectorPrefix = "CHAINLAB_WASM_VECTOR="

const expectedChainLabProtocolWASMVectorV1 = `{"upload":{"tx_hash":"0x8eb5835d1c18023d1268817b57ded4a9320ad13fc9c5362afeafb088256b65d4","success":true,"gas_used":121548,"effective_gas_price":1,"priority_fee_paid":121548,"events":[{"type":"wasm.code_uploaded","attributes":{"code_id":"wasm:0x63f727dda512449753c2f079a3e75cb603689bfacf593160d76de1431fde1709","creator":"0x7e5f4552091a69125d5dfcb7b8c2659029395bdf","size":"387"}}],"code_id":"wasm:0x63f727dda512449753c2f079a3e75cb603689bfacf593160d76de1431fde1709"},"deploy":{"tx_hash":"0x88fac7461fcbeb1a9660e97f06e007547077a6a823cfff40a634ce30461fe568","success":true,"gas_used":85990,"effective_gas_price":1,"priority_fee_paid":85990,"events":[{"type":"contract.deployed","attributes":{"address":"0x9fa7a62072c3a941cce7093e4c1e8955b20da96f","code_id":"wasm:0x63f727dda512449753c2f079a3e75cb603689bfacf593160d76de1431fde1709","creator":"0x7e5f4552091a69125d5dfcb7b8c2659029395bdf"}},{"type":"wasm.echo.initialized","attributes":{"value":"hello"}}],"contract_address":"0x9fa7a62072c3a941cce7093e4c1e8955b20da96f"},"call":{"tx_hash":"0x90d9a9bd745261ac3e6da4abfc32ee6cfe09840797ce8efcbf1dfa1cf442a45a","success":true,"gas_used":55944,"effective_gas_price":1,"priority_fee_paid":55944,"events":[{"type":"wasm.echoed","attributes":{"value":"world"}}]},"value":"world","read_gas":5807,"root":"0x9e97b6fb98beb82fd6de5ed02728cb82b34597d838149c89f2ecd2496427d12d","metering_version":"chainlab-wasm-v1","runtime_version":"wasmtime-go-v46.0.1"}`

func TestWASMExecutionMatchesAcrossFreshProcesses(t *testing.T) {
	if os.Getenv("CHAINLAB_WASM_VECTOR_CHILD") == "1" {
		vector := executeDeterministicWASMVector(t)
		encoded, err := json.Marshal(vector)
		if err != nil {
			t.Fatal(err)
		}
		t.Log(wasmVectorPrefix + string(encoded))
		return
	}

	first := runWASMVectorProcess(t)
	second := runWASMVectorProcess(t)
	if first != second {
		t.Fatalf("fresh-process WASM vectors differ:\nfirst:  %s\nsecond: %s", first, second)
	}
	if first != expectedChainLabProtocolWASMVectorV1 {
		t.Fatalf("chainlab-wasm-v1 protocol vector changed:\n got: %s\nwant: %s", first, expectedChainLabProtocolWASMVectorV1)
	}
}

type deterministicWASMVector struct {
	Upload          types.Receipt `json:"upload"`
	Deploy          types.Receipt `json:"deploy"`
	Call            types.Receipt `json:"call"`
	Value           string        `json:"value"`
	ReadGas         uint64        `json:"read_gas"`
	Root            string        `json:"root"`
	MeteringVersion string        `json:"metering_version"`
	RuntimeVersion  string        `json:"runtime_version"`
}

func executeDeterministicWASMVector(t *testing.T) deterministicWASMVector {
	t.Helper()
	key, err := chaincrypto.PrivateKeyFromHex(strings.Repeat("0", 63) + "1")
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	store := state.NewStore()
	store.SetBalance(alice, 10_000_000)
	runtime := contracts.NewRuntimeWithDefaults()
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", runtime)
	bytecode := contracts.WasmEchoCode()

	upload := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxWASMUpload,
		From:     alice,
		Nonce:    0,
		GasLimit: core.EstimateWASMUploadGas(bytecode),
		GasPrice: 1,
		Payload:  map[string]string{"bytecode": "0x" + hex.EncodeToString(bytecode)},
	})
	uploadReceipt, err := executor.Execute(store, upload)
	if err != nil {
		t.Fatal(err)
	}

	deploy := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    1,
		GasLimit: 100_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": uploadReceipt.CodeID,
			"message": "hello",
		},
	})
	deployReceipt, err := executor.Execute(store, deploy)
	if err != nil {
		t.Fatal(err)
	}

	call := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       deployReceipt.ContractAddress,
		Nonce:    2,
		GasLimit: 100_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method":  "set",
			"message": "world",
		},
	})
	callReceipt, err := executor.Execute(store, call)
	if err != nil {
		t.Fatal(err)
	}
	value, readGas, err := runtime.ReadMetered(store, deployReceipt.ContractAddress, alice, "get", nil, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	return deterministicWASMVector{
		Upload:          uploadReceipt,
		Deploy:          deployReceipt,
		Call:            callReceipt,
		Value:           value,
		ReadGas:         readGas,
		Root:            store.Root(),
		MeteringVersion: contracts.WASMMeteringVersion,
		RuntimeVersion:  contracts.WASMRuntimeVersion,
	}
}

func runWASMVectorProcess(t *testing.T) string {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestWASMExecutionMatchesAcrossFreshProcesses$", "-test.count=1", "-test.v")
	command.Env = append(os.Environ(), "CHAINLAB_WASM_VECTOR_CHILD=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("WASM vector subprocess failed: %v\n%s", err, output)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if marker := strings.Index(line, wasmVectorPrefix); marker >= 0 {
			return strings.TrimSpace(line[marker+len(wasmVectorPrefix):])
		}
	}
	t.Fatalf("WASM vector subprocess returned no vector:\n%s", output)
	return ""
}
