package desktop

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chainlab/internal/hash"
)

const desktopProcessHelperEnv = "CHAINLAB_DESKTOP_PROCESS_HELPER"

func TestDesktopProcessHelper(t *testing.T) {
	root := os.Getenv(desktopProcessHelperEnv)
	if root == "" {
		return
	}
	if err := Run(context.Background(), root, os.Stdout); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRuntimeBindsCanonicalControlRecordToConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), "desktop")
	config, err := Initialize(root, ProfileDesktopSolo, "chainlab-runtime-record")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newRuntime(config.ControlURL)
	if err != nil {
		t.Fatal(err)
	}
	writeRuntime := func(value Runtime) {
		t.Helper()
		raw, err := hash.CanonicalBytes(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, RuntimePath), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRuntime(runtime)
	if _, err := loadRuntime(root); err != nil {
		t.Fatal(err)
	}
	runtime.ControlURL = "http://127.0.0.1:12345"
	writeRuntime(runtime)
	if _, err := loadRuntime(root); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("changed control URL error = %v", err)
	}
	runtime.ControlURL = config.ControlURL
	runtime.ControlToken = strings.Repeat("z", 64)
	writeRuntime(runtime)
	if _, err := loadRuntime(root); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("invalid token error = %v", err)
	}
}

func TestDesktopLifecycleTransferAndRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "desktop")
	config, err := Initialize(root, ProfileDesktopSolo, "chainlab-desktop-lifecycle")
	if err != nil {
		t.Fatal(err)
	}

	firstProcess, firstResult := startTestDesktop(t, root)
	first := waitForTestStatus(t, root, firstResult, 30*time.Second)
	if first.ChainID != config.ChainID || first.Height < 1 {
		t.Fatalf("first status = %+v", first)
	}
	if err := Run(context.Background(), root, io.Discard); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second owner error = %v", err)
	}

	recipient := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	transferContext, transferCancel := context.WithTimeout(context.Background(), 15*time.Second)
	transfer, err := Transfer(transferContext, root, recipient, 12_345)
	transferCancel()
	if err != nil {
		t.Fatal(err)
	}
	if !transfer.Receipt.Success || transfer.TransactionHash != transfer.Receipt.TxHash || transfer.CometHash == transfer.TransactionHash {
		t.Fatalf("transfer = %+v", transfer)
	}
	stopTestDesktop(t, root, firstProcess, firstResult)
	if _, err := os.Stat(filepath.Join(root, RuntimePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime file after stop: %v", err)
	}

	secondProcess, secondResult := startTestDesktop(t, root)
	second := waitForTestStatus(t, root, secondResult, 30*time.Second)
	if second.ChainID != first.ChainID || second.Height <= first.Height {
		t.Fatalf("restart status = %+v, first = %+v", second, first)
	}
	queryContext, queryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	receipt, err := Receipt(queryContext, root, transfer.CometHash)
	queryCancel()
	if err != nil {
		t.Fatal(err)
	}
	if receipt.TransactionHash != transfer.TransactionHash || receipt.Height != transfer.Height || !receipt.Receipt.Success {
		t.Fatalf("restored receipt = %+v, want %+v", receipt, transfer)
	}
	accountContext, accountCancel := context.WithTimeout(context.Background(), 5*time.Second)
	account, err := Account(accountContext, root, recipient)
	accountCancel()
	if err != nil {
		t.Fatal(err)
	}
	if account.Balance != 12_345 {
		t.Fatalf("restored account = %+v", account)
	}
	stopTestDesktop(t, root, secondProcess, secondResult)
}

func TestDesktopForcedTerminationRecoversStaleRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "desktop")
	if _, err := Initialize(root, ProfileDesktopSolo, "chainlab-desktop-forced-recovery"); err != nil {
		t.Fatal(err)
	}
	firstProcess, firstResult := startTestDesktop(t, root)
	first := waitForTestStatus(t, root, firstResult, 30*time.Second)
	if err := firstProcess.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := <-firstResult; err == nil {
		t.Fatal("forced process termination must not report a clean exit")
	}
	if _, err := os.Stat(filepath.Join(root, RuntimePath)); err != nil {
		t.Fatalf("forced termination should leave runtime evidence: %v", err)
	}

	secondProcess, secondResult := startTestDesktop(t, root)
	second := waitForTestStatus(t, root, secondResult, 30*time.Second)
	if second.ChainID != first.ChainID || second.Height < first.Height {
		t.Fatalf("recovered status = %+v, first = %+v", second, first)
	}
	if second.Height == first.Height && (second.BlockHash != first.BlockHash || second.AppHash != first.AppHash) {
		t.Fatalf("same-height recovery changed committed hashes: recovered=%+v first=%+v", second, first)
	}
	stopTestDesktop(t, root, secondProcess, secondResult)
}

func startTestDesktop(t *testing.T, root string) (*exec.Cmd, <-chan error) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestDesktopProcessHelper$")
	command.Env = append(os.Environ(), desktopProcessHelperEnv+"="+root)
	logFile, err := os.Create(filepath.Join(root, "desktop-test.log"))
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	result := make(chan error, 1)
	go func() {
		result <- command.Wait()
		_ = logFile.Close()
	}()
	return command, result
}

func waitForTestStatus(t *testing.T, root string, result <-chan error, timeout time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		select {
		case err := <-result:
			log, _ := os.ReadFile(filepath.Join(root, "desktop-test.log"))
			t.Fatalf("desktop stopped before becoming ready: %v\n%s", err, log)
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		status, err := CurrentStatus(ctx, root)
		cancel()
		if err == nil && status.Status == "running" && status.Height > 0 {
			return status
		}
		last = err
		time.Sleep(100 * time.Millisecond)
	}
	log, _ := os.ReadFile(filepath.Join(root, "desktop-test.log"))
	t.Fatalf("desktop did not become ready: %v\n%s", last, log)
	return Status{}
}

func stopTestDesktop(t *testing.T, root string, command *exec.Cmd, result <-chan error) {
	t.Helper()
	ctx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := Stop(ctx, root)
	stopCancel()
	if err != nil {
		_ = command.Process.Kill()
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("desktop did not stop cleanly")
	}
}
