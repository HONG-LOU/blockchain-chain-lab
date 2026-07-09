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
}
