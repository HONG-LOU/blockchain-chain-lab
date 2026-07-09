package examples_test

import (
	"testing"

	"chainlab/examples"
)

func TestRunDemo(t *testing.T) {
	summary, err := examples.RunDemo()
	if err != nil {
		t.Fatal(err)
	}
	if summary.Height < 1 {
		t.Fatalf("demo height = %d", summary.Height)
	}
	if summary.TransferReceiverBalance != 100 {
		t.Fatalf("receiver balance = %d", summary.TransferReceiverBalance)
	}
	if summary.CounterValue != "3" {
		t.Fatalf("counter value = %q", summary.CounterValue)
	}
	if summary.TokenReceiverBalance != "25" {
		t.Fatalf("token receiver balance = %q", summary.TokenReceiverBalance)
	}
	if summary.WASMValue != "world" {
		t.Fatalf("wasm value = %q", summary.WASMValue)
	}
	if summary.Stake != 200 {
		t.Fatalf("stake = %d", summary.Stake)
	}
	if summary.YesVotes != 200 {
		t.Fatalf("yes votes = %d", summary.YesVotes)
	}
	if summary.GovernanceParam != "majority" {
		t.Fatalf("governance param = %q", summary.GovernanceParam)
	}
	if summary.SponsoredReceiverBalance != 15 {
		t.Fatalf("sponsored receiver balance = %d", summary.SponsoredReceiverBalance)
	}
	if summary.SponsoredUserBalance != 0 {
		t.Fatalf("sponsored user balance = %d", summary.SponsoredUserBalance)
	}
	if summary.BatchReceiverBalance != 7 {
		t.Fatalf("batch receiver balance = %d", summary.BatchReceiverBalance)
	}
	if summary.BatchCounterValue != "2" {
		t.Fatalf("batch counter value = %q", summary.BatchCounterValue)
	}
	if summary.SmartAccountReceiverBalance != 42 {
		t.Fatalf("smart account receiver balance = %d", summary.SmartAccountReceiverBalance)
	}
	if summary.SmartAccountBalance != 7_958 {
		t.Fatalf("smart account balance = %d", summary.SmartAccountBalance)
	}
	if summary.SmartAccountNonce != 1 {
		t.Fatalf("smart account nonce = %d", summary.SmartAccountNonce)
	}
}
