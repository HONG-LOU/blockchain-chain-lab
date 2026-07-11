package node_test

import (
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/node"
	"chainlab/internal/types"
)

func TestNodeReorgReinsertsOrphanedTransactionAndPersistsIt(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	balances := map[string]uint64{alice: 1_000_000}
	baseConfig := node.Config{
		ChainID: "chainlab-reorg-reinsert", ProposerKey: key,
		GenesisBalance: balances, Validators: []string{alice},
		GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	}
	branchA, err := node.NewDevelopment(baseConfig)
	if err != nil {
		t.Fatal(err)
	}
	branchB, err := node.NewDevelopment(baseConfig)
	if err != nil {
		t.Fatal(err)
	}
	followerConfig := baseConfig
	followerConfig.Role = node.RoleValidator
	followerConfig.DataDir = t.TempDir()
	follower, err := node.New(followerConfig)
	if err != nil {
		t.Fatal(err)
	}

	orphaned := signedNodeTx(t, key, types.Transaction{
		ChainID: "chainlab-reorg-reinsert", Type: types.TxTransfer,
		From: alice, To: bob, Nonce: 0, Value: 100, GasLimit: 21_000, GasPrice: 1,
	})
	if err := branchA.SubmitTx(orphaned); err != nil {
		t.Fatal(err)
	}
	blockA1, err := branchA.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	blockB1, err := branchB.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	blockB2, err := branchB.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := follower.ImportBlock(blockA1); err != nil {
		t.Fatal(err)
	}
	if err := follower.ImportBlock(blockB1); err != nil {
		t.Fatal(err)
	}
	if err := follower.ImportBlock(blockB2); err != nil {
		t.Fatal(err)
	}
	if follower.Head().Hash() != blockB2.Hash() {
		t.Fatal("follower did not adopt the longer branch")
	}
	pool := follower.TxPool()
	if len(pool.Pending) != 1 || len(pool.Queued) != 0 || pool.Pending[0].Hash() != orphaned.Hash() {
		t.Fatalf("reorg transaction pool = %+v", pool)
	}
	if _, committed := follower.Transaction(orphaned.Hash()); committed {
		t.Fatal("orphaned transaction remained in the canonical transaction index")
	}
	closeTestNode(t, follower)

	restored, err := node.New(followerConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestNode(t, restored)
	pool = restored.TxPool()
	if len(pool.Pending) != 1 || pool.Pending[0].Hash() != orphaned.Hash() {
		t.Fatalf("restored reorg transaction pool = %+v", pool)
	}
	block, err := restored.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 1 || block.Transactions[0].Hash() != orphaned.Hash() {
		t.Fatalf("post-reorg block transactions = %+v", block.Transactions)
	}
	if got := restored.Account(bob).Balance; got != 100 {
		t.Fatalf("recipient balance after reinserted transaction = %d", got)
	}
}
