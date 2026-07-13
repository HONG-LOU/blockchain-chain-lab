package desktop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDesktopBackupRestoreAndIntegrityRejection(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "live")
	config, err := Initialize(root, ProfileDesktopSolo, "chainlab-backup-test")
	if err != nil {
		t.Fatal(err)
	}
	command, result := startTestDesktop(t, root)
	_ = waitForTestStatus(t, root, result, 30*time.Second)
	if err := Backup(root, filepath.Join(parent, "running.zip")); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("running backup error = %v", err)
	}
	recipient := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	transferContext, transferCancel := context.WithTimeout(context.Background(), 15*time.Second)
	transfer, err := Transfer(transferContext, root, recipient, 77)
	transferCancel()
	if err != nil {
		t.Fatal(err)
	}
	before := currentTestStatus(t, root)
	stopTestDesktop(t, root, command, result)

	backupPath := filepath.Join(parent, "chainlab-backup.zip")
	if err := Backup(root, backupPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatal(err)
	}
	backupStatus := loadBackupStatus(root)
	if backupStatus == nil || backupStatus.ArchiveBytes <= 0 || len(backupStatus.ArchiveSHA256) != 64 {
		t.Fatalf("backup status = %+v", backupStatus)
	}
	if err := Backup(root, filepath.Join(root, "inside.zip")); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("inside backup error = %v", err)
	}
	if _, err := Restore(backupPath, filepath.Join(parent, "wrong-chain"), "other-chain"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong-chain restore error = %v", err)
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	restoredRoot := root
	restored, err := Restore(backupPath, restoredRoot, config.ChainID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ChainID != config.ChainID || restored.Profile != config.Profile {
		t.Fatalf("restored config = %+v", restored)
	}
	if _, err := Restore(backupPath, restoredRoot, config.ChainID); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing destination error = %v", err)
	}

	restoredCommand, restoredResult := startTestDesktop(t, restoredRoot)
	after := waitForTestStatus(t, restoredRoot, restoredResult, 30*time.Second)
	if after.ChainID != before.ChainID || after.Height < before.Height {
		t.Fatalf("restored status = %+v, before = %+v", after, before)
	}
	accountContext, accountCancel := context.WithTimeout(context.Background(), 5*time.Second)
	account, err := Account(accountContext, restoredRoot, recipient)
	accountCancel()
	if err != nil || account.Balance != 77 {
		t.Fatalf("restored account = %+v err=%v", account, err)
	}
	receiptContext, receiptCancel := context.WithTimeout(context.Background(), 5*time.Second)
	receipt, err := Receipt(receiptContext, restoredRoot, transfer.CometHash)
	receiptCancel()
	if err != nil || receipt.TransactionHash != transfer.TransactionHash {
		t.Fatalf("restored receipt = %+v err=%v", receipt, err)
	}
	stopTestDesktop(t, restoredRoot, restoredCommand, restoredResult)

	raw, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 0xff
	corruptPath := filepath.Join(parent, "corrupt.zip")
	if err := os.WriteFile(corruptPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(corruptPath, filepath.Join(parent, "corrupt-restore"), config.ChainID); err == nil {
		t.Fatal("corrupt backup should fail closed")
	}
}
