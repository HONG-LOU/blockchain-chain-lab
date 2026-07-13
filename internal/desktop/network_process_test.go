package desktop

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"chainlab/internal/cometnode"
	chaincrypto "chainlab/internal/crypto"
	chaintypes "chainlab/internal/types"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
)

func TestHomeValidatorQuorumRecoveryAndObserverBroadcast(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping isolated five-process desktop network in short mode")
	}
	base := availablePortBlock(t, 30)
	parent := t.TempDir()
	roots := make([]string, 4)
	identities := make([]PublicIdentity, 4)
	for index := range roots {
		roots[index] = filepath.Join(parent, "validator-"+strconv.Itoa(index))
		identity, err := GenerateIdentity(
			roots[index], cometnode.RoleValidator, "validator-"+strconv.Itoa(index),
			"127.0.0.1:"+strconv.Itoa(base+index),
		)
		if err != nil {
			t.Fatal(err)
		}
		identities[index] = identity
	}
	invitationPath := filepath.Join(parent, "invitation.json")
	if _, err := CreateInvitation(invitationPath, "chainlab-home-process", "Home process", identities, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for index, root := range roots {
		if _, err := Join(root, invitationPath, processPorts(base, index)); err != nil {
			t.Fatal(err)
		}
	}

	commands := make([]*exec.Cmd, 4)
	results := make([]<-chan error, 4)
	for index, root := range roots {
		commands[index], results[index] = startTestDesktop(t, root)
	}
	statuses := make([]Status, 4)
	for index, root := range roots {
		statuses[index] = waitForTestStatus(t, root, results[index], 60*time.Second)
	}
	waitForPeerCounts(t, roots, []int{3, 3, 3, 3}, 30*time.Second)

	recipient := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	transferContext, transferCancel := context.WithTimeout(context.Background(), 30*time.Second)
	firstTransfer, err := Transfer(transferContext, roots[0], recipient, 100)
	transferCancel()
	if err != nil {
		t.Fatal(err)
	}
	waitForAccountBalance(t, roots, recipient, 100, 30*time.Second)

	stopTestDesktop(t, roots[2], commands[2], results[2])
	stopTestDesktop(t, roots[3], commands[3], results[3])
	time.Sleep(2 * time.Second)
	stable := []Status{currentTestStatus(t, roots[0]), currentTestStatus(t, roots[1])}
	raw := nextSignedTransfer(t, roots[0], recipient, 200)
	submitContext, submitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	queuedHash, err := SubmitRaw(submitContext, roots[0], raw)
	submitCancel()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(12 * time.Second)
	for index := range 2 {
		status := currentTestStatus(t, roots[index])
		if status.Height != stable[index].Height || status.AppHash != stable[index].AppHash {
			t.Fatalf("2-of-4 node %d advanced: stable=%+v current=%+v", index, stable[index], status)
		}
	}
	waitForAccountBalance(t, roots[:2], recipient, 100, 2*time.Second)

	commands[2], results[2] = startTestDesktop(t, roots[2])
	_ = waitForTestStatus(t, roots[2], results[2], 60*time.Second)
	receiptContext, receiptCancel := context.WithTimeout(context.Background(), 30*time.Second)
	queuedReceipt, err := waitForReceiptByHash(receiptContext, roots[0], queuedHash)
	receiptCancel()
	if err != nil || !queuedReceipt.Receipt.Success || queuedReceipt.Height <= firstTransfer.Height {
		t.Fatalf("queued receipt = %+v err=%v", queuedReceipt, err)
	}
	waitForAccountBalance(t, roots[:3], recipient, 300, 30*time.Second)

	commands[3], results[3] = startTestDesktop(t, roots[3])
	_ = waitForTestStatus(t, roots[3], results[3], 60*time.Second)
	waitForAccountBalance(t, roots, recipient, 300, 30*time.Second)
	waitForPeerCounts(t, roots, []int{3, 3, 3, 3}, 30*time.Second)

	observerRoot := filepath.Join(parent, "observer")
	if _, err := GenerateIdentity(observerRoot, cometnode.RoleObserver, "observer-0", "127.0.0.1:"+strconv.Itoa(base+4)); err != nil {
		t.Fatal(err)
	}
	if _, err := Join(observerRoot, invitationPath, processPorts(base, 4)); err != nil {
		t.Fatal(err)
	}
	observerCommand, observerResult := startTestDesktop(t, observerRoot)
	observerStatus := waitForTestStatus(t, observerRoot, observerResult, 60*time.Second)
	if observerStatus.Profile != ProfileObserver {
		t.Fatalf("observer status = %+v", observerStatus)
	}
	waitForAccountBalance(t, []string{observerRoot}, recipient, 300, 30*time.Second)
	for _, path := range []string{
		filepath.Join(observerRoot, identityHomePath, "config", "priv_validator_key.json"),
		filepath.Join(observerRoot, identityHomePath, "data", "priv_validator_state.json"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("observer has signing material at %s: %v", path, err)
		}
	}

	observerRaw := nextSignedTransfer(t, roots[0], recipient, 300)
	broadcastContext, broadcastCancel := context.WithTimeout(context.Background(), 30*time.Second)
	observerReceipt, err := BroadcastRaw(broadcastContext, observerRoot, observerRaw)
	broadcastCancel()
	if err != nil || !observerReceipt.Receipt.Success {
		t.Fatalf("observer broadcast receipt = %+v err=%v", observerReceipt, err)
	}
	waitForAccountBalance(t, append(append([]string(nil), roots...), observerRoot), recipient, 600, 30*time.Second)

	stopTestDesktop(t, observerRoot, observerCommand, observerResult)
	for index := range roots {
		stopTestDesktop(t, roots[index], commands[index], results[index])
	}
}

func processPorts(base int, index int) LocalPorts {
	return LocalPorts{ABCI: base + 5 + index, RPC: base + 10 + index, Control: base + 15 + index, Explorer: base + 20 + index}
}

func availablePortBlock(t *testing.T, count int) int {
	t.Helper()
	for attempts := 0; attempts < 100; attempts++ {
		seed, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		base := seed.Addr().(*net.TCPAddr).Port
		_ = seed.Close()
		if base+count >= 65535 {
			continue
		}
		listeners := make([]net.Listener, 0, count)
		available := true
		for offset := 0; offset < count; offset++ {
			listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(base+offset))
			if err != nil {
				available = false
				break
			}
			listeners = append(listeners, listener)
		}
		for _, listener := range listeners {
			_ = listener.Close()
		}
		if available {
			return base
		}
	}
	t.Fatal("could not reserve a local port block")
	return 0
}

func waitForPeerCounts(t *testing.T, roots []string, expected []int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		matched := true
		for index, root := range roots {
			config, err := Load(root)
			if err != nil {
				matched = false
				break
			}
			client, err := rpchttp.New(tcpHTTPURL(config.RPCAddress), "/websocket")
			if err != nil {
				matched = false
				break
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			info, err := client.NetInfo(ctx)
			cancel()
			if err != nil || len(info.Peers) != expected[index] {
				matched = false
				break
			}
		}
		if matched {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("peer counts did not converge")
}

func waitForAccountBalance(t *testing.T, roots []string, address string, balance uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		matched := true
		for _, root := range roots {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			account, err := Account(ctx, root, address)
			cancel()
			if err != nil || account.Balance != balance {
				matched = false
				break
			}
		}
		if matched {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("account %s did not converge to balance %d", address, balance)
}

func currentTestStatus(t *testing.T, root string) Status {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	status, err := CurrentStatus(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func nextSignedTransfer(t *testing.T, root string, recipient string, value uint64) string {
	t.Helper()
	config, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	key, err := loadSoloKey(root, config)
	if err != nil {
		t.Fatal(err)
	}
	from := chaincrypto.AddressFromPrivateKey(key)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	account, err := Account(ctx, root, from)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	transaction := chaintypes.Transaction{
		ChainID: config.ChainID, Type: chaintypes.TxTransfer, From: from, To: recipient,
		Nonce: account.Nonce, Value: value, GasLimit: 21_000, GasPrice: 1,
	}
	transaction.Signature, err = chaincrypto.Sign(key, transaction.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := chaintypes.EncodeRawTransaction(transaction)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func waitForReceiptByHash(ctx context.Context, root string, hash string) (TransferResult, error) {
	for {
		result, err := Receipt(ctx, root, hash)
		if err == nil {
			return result, nil
		}
		select {
		case <-ctx.Done():
			return TransferResult{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
