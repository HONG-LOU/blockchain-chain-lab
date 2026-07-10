package node

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"chainlab/internal/contracts"
	"chainlab/internal/core"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

const (
	immutableSnapshotChainID     = "chainlab-read-snapshot"
	immutableSnapshotCodeID      = "counter.v1"
	immutableSnapshotBlobCount   = 80
	immutableSnapshotBlobBytes   = 4 * 1024
	immutableSnapshotReaderCount = 8
	immutableSnapshotGasLimit    = 8_000_000
	immutableSnapshotTimeout     = 20 * time.Second
)

type immutableSnapshotProbe struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (p immutableSnapshotProbe) Deploy(ctx contracts.Context, args map[string]string) ([]types.Event, error) {
	return nil, immutableSnapshotWriteVersion(ctx, args["version"])
}

func (p immutableSnapshotProbe) Call(ctx contracts.Context, method string, args map[string]string) ([]types.Event, error) {
	if method != "set" {
		return nil, fmt.Errorf("unknown immutable snapshot method %q", method)
	}
	return nil, immutableSnapshotWriteVersion(ctx, args["version"])
}

func (p immutableSnapshotProbe) Read(ctx contracts.Context, method string, _ map[string]string) (string, error) {
	switch method {
	case "mutate":
		if err := ctx.SetStorage("version", "corrupt"); err != nil {
			return "", err
		}
		return "", errors.New("read-only mutation unexpectedly succeeded")
	case "probe":
		version, err := ctx.GetStorage("version")
		if err != nil {
			return "", err
		}
		blob, err := immutableSnapshotBlob(version)
		if err != nil {
			return "", err
		}
		if p.entered != nil {
			select {
			case p.entered <- struct{}{}:
			default:
			}
		}
		if p.release != nil {
			<-p.release
		}
		for index := range immutableSnapshotBlobCount {
			key := fmt.Sprintf("blob:%03d", index)
			value, readErr := ctx.GetStorage(key)
			if readErr != nil {
				return "", readErr
			}
			if value != blob {
				return "", fmt.Errorf("mixed immutable snapshot at %s: version %q has blob prefix %q", key, version, immutableSnapshotPrefix(value))
			}
		}
		return version, nil
	default:
		return "", fmt.Errorf("unknown immutable snapshot read method %q", method)
	}
}

func TestReadContractPinsImmutableSnapshotWhileProducingBlock(t *testing.T) {
	key := immutableSnapshotKey(t)
	release := make(chan struct{})
	entered := make(chan struct{}, immutableSnapshotReaderCount)
	n := newImmutableSnapshotNode(t, key, immutableSnapshotProbe{entered: entered, release: release})
	contract := deployImmutableSnapshotContract(t, n, key, "old")

	update := immutableSnapshotSignedTx(t, key, types.Transaction{
		ChainID:  immutableSnapshotChainID,
		Type:     types.TxCall,
		From:     chaincrypto.AddressFromPrivateKey(key),
		To:       contract,
		Nonce:    1,
		GasLimit: immutableSnapshotGasLimit,
		GasPrice: 1,
		Payload:  map[string]string{"method": "set", "version": "new"},
	})
	if err := n.SubmitTx(update); err != nil {
		t.Fatal(err)
	}

	oldRoot := n.StateRoot()
	reads := startImmutableSnapshotReads(n, contract, immutableSnapshotReaderCount)
	waitForImmutableSnapshotReaders(t, entered, immutableSnapshotReaderCount)

	produced := make(chan immutableSnapshotBlockResult, 1)
	go func() {
		block, err := n.ProduceBlock()
		produced <- immutableSnapshotBlockResult{block: block, err: err}
	}()
	result := awaitImmutableSnapshotBlock(t, produced, release, "ProduceBlock")
	if result.err != nil {
		close(release)
		t.Fatalf("produce concurrent block: %v", result.err)
	}
	if result.block.Header.StateRoot == oldRoot || n.StateRoot() != result.block.Header.StateRoot {
		close(release)
		t.Fatalf("canonical root did not atomically advance: old=%s block=%s node=%s", oldRoot, result.block.Header.StateRoot, n.StateRoot())
	}

	close(release)
	assertImmutableSnapshotReads(t, reads, immutableSnapshotReaderCount, "old")
	assertImmutableSnapshotCurrentValue(t, n, contract, "new")
}

func TestReadContractPinsImmutableSnapshotAcrossImportedReorg(t *testing.T) {
	key := immutableSnapshotKey(t)
	producerA := newImmutableSnapshotNode(t, key, immutableSnapshotProbe{})
	producerB := newImmutableSnapshotNode(t, key, immutableSnapshotProbe{})
	release := make(chan struct{})
	entered := make(chan struct{}, immutableSnapshotReaderCount)
	follower := newImmutableSnapshotNode(t, key, immutableSnapshotProbe{entered: entered, release: release})

	contract, common := deployImmutableSnapshotContractAndBlock(t, producerA, key, "old")
	if err := producerB.ImportBlock(common); err != nil {
		t.Fatalf("import common deployment into branch B: %v", err)
	}
	if err := follower.ImportBlock(common); err != nil {
		t.Fatalf("import common deployment into follower: %v", err)
	}

	blockA2, err := producerA.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	update := immutableSnapshotSignedTx(t, key, types.Transaction{
		ChainID:  immutableSnapshotChainID,
		Type:     types.TxCall,
		From:     chaincrypto.AddressFromPrivateKey(key),
		To:       contract,
		Nonce:    1,
		GasLimit: immutableSnapshotGasLimit,
		GasPrice: 1,
		Payload:  map[string]string{"method": "set", "version": "new"},
	})
	if err := producerB.SubmitTx(update); err != nil {
		t.Fatal(err)
	}
	blockB2, err := producerB.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	blockB3, err := producerB.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := follower.ImportBlock(blockA2); err != nil {
		t.Fatalf("adopt branch A: %v", err)
	}
	if err := follower.ImportBlock(blockB2); err != nil {
		t.Fatalf("import equal-height side branch B: %v", err)
	}
	if follower.Head().Hash() != blockA2.Hash() {
		t.Fatal("equal-height side branch unexpectedly replaced the canonical head")
	}

	oldRoot := follower.StateRoot()
	reads := startImmutableSnapshotReads(follower, contract, immutableSnapshotReaderCount)
	waitForImmutableSnapshotReaders(t, entered, immutableSnapshotReaderCount)

	imported := make(chan error, 1)
	go func() {
		imported <- follower.ImportBlock(blockB3)
	}()
	select {
	case importErr := <-imported:
		if importErr != nil {
			close(release)
			t.Fatalf("import longer branch while reads are paused: %v", importErr)
		}
	case <-time.After(immutableSnapshotTimeout):
		close(release)
		<-imported
		t.Fatal("ImportBlock was blocked by paused ReadContract calls; Node.mu is held across contract execution")
	}
	if follower.Head().Hash() != blockB3.Hash() || follower.StateRoot() == oldRoot {
		close(release)
		t.Fatalf("longer branch did not atomically replace canonical state: head=%s root=%s old=%s", follower.Head().Hash(), follower.StateRoot(), oldRoot)
	}

	close(release)
	assertImmutableSnapshotReads(t, reads, immutableSnapshotReaderCount, "old")
	assertImmutableSnapshotCurrentValue(t, follower, contract, "new")
}

func TestReadContractRejectsNativeMutationWithoutChangingCanonicalRoot(t *testing.T) {
	key := immutableSnapshotKey(t)
	n := newImmutableSnapshotNode(t, key, immutableSnapshotProbe{})
	contract := deployImmutableSnapshotContract(t, n, key, "old")
	root := n.StateRoot()
	head := n.Head()

	if _, err := n.ReadContract(chaincrypto.AddressFromPrivateKey(key), contract, "mutate", nil); !errors.Is(err, contracts.ErrReadOnlyContract) {
		t.Fatalf("read-only native mutation error = %v, want %v", err, contracts.ErrReadOnlyContract)
	}
	if got := n.StateRoot(); got != root {
		t.Fatalf("read-only native mutation changed canonical root: got %s want %s", got, root)
	}
	if got := n.Head(); got.Hash() != head.Hash() || got.Header.StateRoot != root {
		t.Fatalf("read-only native mutation changed canonical head: got %+v want %+v", got.Header, head.Header)
	}
	assertImmutableSnapshotCurrentValue(t, n, contract, "old")
}

type immutableSnapshotReadResult struct {
	value string
	err   error
}

type immutableSnapshotBlockResult struct {
	block types.Block
	err   error
}

func newImmutableSnapshotNode(t *testing.T, key chaincrypto.PrivateKey, probe immutableSnapshotProbe) *Node {
	t.Helper()
	address := chaincrypto.AddressFromPrivateKey(key)
	n, err := NewDevelopment(Config{
		ChainID:        immutableSnapshotChainID,
		ProposerKey:    key,
		Validators:     []string{address},
		GenesisBalance: map[string]uint64{address: 100_000_000}, GenesisTimeUnix: DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := contracts.NewRuntime()
	if err := runtime.Register(immutableSnapshotCodeID, probe); err != nil {
		t.Fatal(err)
	}
	runtime.Seal()
	n.runtime = runtime
	n.executor = core.NewExecutor(immutableSnapshotChainID, address, runtime)
	return n
}

func immutableSnapshotKey(t *testing.T) chaincrypto.PrivateKey {
	t.Helper()
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func deployImmutableSnapshotContract(t *testing.T, n *Node, key chaincrypto.PrivateKey, version string) string {
	t.Helper()
	contract, _ := deployImmutableSnapshotContractAndBlock(t, n, key, version)
	return contract
}

func deployImmutableSnapshotContractAndBlock(t *testing.T, n *Node, key chaincrypto.PrivateKey, version string) (string, types.Block) {
	t.Helper()
	tx := immutableSnapshotSignedTx(t, key, types.Transaction{
		ChainID:  immutableSnapshotChainID,
		Type:     types.TxDeploy,
		From:     chaincrypto.AddressFromPrivateKey(key),
		Nonce:    0,
		GasLimit: immutableSnapshotGasLimit,
		GasPrice: 1,
		Payload:  map[string]string{"code_id": immutableSnapshotCodeID, "version": version},
	})
	if err := n.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Receipts) != 1 || !block.Receipts[0].Success || block.Receipts[0].ContractAddress == "" {
		t.Fatalf("snapshot contract deployment receipt = %+v", block.Receipts)
	}
	return block.Receipts[0].ContractAddress, block
}

func immutableSnapshotSignedTx(t *testing.T, key chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	signature, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = signature
	return tx
}

func immutableSnapshotWriteVersion(ctx contracts.Context, version string) error {
	blob, err := immutableSnapshotBlob(version)
	if err != nil {
		return err
	}
	if err := ctx.SetStorage("version", version); err != nil {
		return err
	}
	for index := range immutableSnapshotBlobCount {
		if err := ctx.SetStorage(fmt.Sprintf("blob:%03d", index), blob); err != nil {
			return err
		}
	}
	return nil
}

func immutableSnapshotBlob(version string) (string, error) {
	switch version {
	case "old":
		return strings.Repeat("o", immutableSnapshotBlobBytes), nil
	case "new":
		return strings.Repeat("n", immutableSnapshotBlobBytes), nil
	default:
		return "", fmt.Errorf("invalid immutable snapshot version %q", version)
	}
}

func immutableSnapshotPrefix(value string) string {
	if len(value) > 16 {
		return value[:16]
	}
	return value
}

func startImmutableSnapshotReads(n *Node, contract string, count int) <-chan immutableSnapshotReadResult {
	results := make(chan immutableSnapshotReadResult, count)
	for range count {
		go func() {
			value, err := n.ReadContract(n.proposer, contract, "probe", nil)
			results <- immutableSnapshotReadResult{value: value, err: err}
		}()
	}
	return results
}

func waitForImmutableSnapshotReaders(t *testing.T, entered <-chan struct{}, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		select {
		case <-entered:
		case <-time.After(immutableSnapshotTimeout):
			t.Fatalf("only %d/%d ReadContract calls reached the pinned snapshot", index, count)
		}
	}
}

func awaitImmutableSnapshotBlock(t *testing.T, results <-chan immutableSnapshotBlockResult, release chan struct{}, operation string) immutableSnapshotBlockResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(immutableSnapshotTimeout):
		close(release)
		<-results
		t.Fatalf("%s was blocked by paused ReadContract calls; Node.mu is held across contract execution", operation)
		return immutableSnapshotBlockResult{}
	}
}

func assertImmutableSnapshotReads(t *testing.T, results <-chan immutableSnapshotReadResult, count int, want string) {
	t.Helper()
	for index := 0; index < count; index++ {
		select {
		case result := <-results:
			if result.err != nil || result.value != want {
				t.Fatalf("pinned read %d = %q, %v; want %q from one immutable state version", index, result.value, result.err, want)
			}
		case <-time.After(immutableSnapshotTimeout):
			t.Fatalf("pinned read %d/%d did not complete", index+1, count)
		}
	}
}

func assertImmutableSnapshotCurrentValue(t *testing.T, n *Node, contract string, want string) {
	t.Helper()
	value, err := n.ReadContract(n.proposer, contract, "probe", nil)
	if err != nil || value != want {
		t.Fatalf("current snapshot read = %q, %v; want %q", value, err, want)
	}
}
