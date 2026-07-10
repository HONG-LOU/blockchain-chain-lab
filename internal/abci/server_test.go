package abci

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"chainlab/internal/types"

	abcicli "github.com/cometbft/cometbft/abci/client"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtlog "github.com/cometbft/cometbft/libs/log"
)

func TestSocketServerRunsAuthoritativeLifecycleOverWire(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	address := availableTCPAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- Serve(ctx, address, fixture.genesis, cmtlog.NewNopLogger())
	}()

	client := connectSocketClient(t, address)
	defer func() {
		if err := client.Stop(); err != nil {
			t.Errorf("stop ABCI client: %v", err)
		}
		cancel()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Errorf("serve ABCI: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("ABCI server did not stop")
		}
	}()

	info, err := client.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if info.LastBlockHeight != 0 || len(info.LastBlockAppHash) != 32 {
		t.Fatalf("initial info = %+v", info)
	}
	if _, err := client.InitChain(context.Background(), fixture.initRequest()); err != nil {
		t.Fatal(err)
	}
	tx := signFixtureTransaction(t, fixture.key, types.Transaction{
		ChainID:  fixture.genesis.ChainID,
		Type:     types.TxTransfer,
		From:     fixture.account,
		To:       "0x1111111111111111111111111111111111111111",
		Nonce:    0,
		Value:    1,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	raw := rawFixtureTransaction(t, tx)
	checked, err := client.CheckTx(context.Background(), &abcitypes.RequestCheckTx{Tx: raw})
	if err != nil || checked.Code != CodeOK {
		t.Fatalf("check tx = %+v err=%v", checked, err)
	}
	proposal := &abcitypes.RequestPrepareProposal{
		Txs:             [][]byte{raw},
		MaxTxBytes:      1_000_000,
		Height:          1,
		ProposerAddress: fixture.proposerAddress,
	}
	prepared, err := client.PrepareProposal(context.Background(), proposal)
	if err != nil || len(prepared.Txs) != 1 {
		t.Fatalf("prepare proposal = %+v err=%v", prepared, err)
	}
	processed, err := client.ProcessProposal(context.Background(), &abcitypes.RequestProcessProposal{
		Txs:             prepared.Txs,
		Hash:            blockHash(1),
		Height:          1,
		ProposerAddress: fixture.proposerAddress,
	})
	if err != nil || processed.Status != abcitypes.ResponseProcessProposal_ACCEPT {
		t.Fatalf("process proposal = %+v err=%v", processed, err)
	}
	finalized, err := client.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Txs:             prepared.Txs,
		Hash:            blockHash(1),
		Height:          1,
		ProposerAddress: fixture.proposerAddress,
	})
	if err != nil || len(finalized.AppHash) != 32 || len(finalized.TxResults) != 1 {
		t.Fatalf("finalize block = %+v err=%v", finalized, err)
	}
	if _, err := client.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	query, err := client.Query(context.Background(), &abcitypes.RequestQuery{Path: "/app"})
	if err != nil || query.Code != CodeOK || query.Height != 1 {
		t.Fatalf("query = %+v err=%v", query, err)
	}
	var commitment applicationCommitment
	if err := json.Unmarshal(query.Value, &commitment); err != nil {
		t.Fatal(err)
	}
	if commitment.Height != 1 || commitment.StateRoot == fixture.app.committed.commitment.StateRoot {
		t.Fatalf("commitment = %+v", commitment)
	}
}

func TestParseGenesisDocumentRequiresCanonicalBytes(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	if _, err := ParseGenesisDocument(fixture.genesisBytes); err != nil {
		t.Fatal(err)
	}
	nonCanonical := append([]byte("\n"), fixture.genesisBytes...)
	if _, err := ParseGenesisDocument(nonCanonical); err == nil || !strings.Contains(err.Error(), "not canonically encoded") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func availableTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := "tcp://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func connectSocketClient(t *testing.T, address string) abcicli.Client {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		client, err := abcicli.NewClient(address, "socket", true)
		if err == nil {
			err = client.Start()
		}
		if err == nil {
			return client
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("ABCI server %s did not accept connections", address)
	return nil
}
