package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"chainlab/internal/consensus"
	"chainlab/internal/contracts"
	"chainlab/internal/core"
	"chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/node"
	chainrpc "chainlab/internal/rpc"
	"chainlab/internal/types"
)

func TestCreateGenesisFilesSeparatesValidatorSecret(t *testing.T) {
	dir := t.TempDir()
	genesisPath := filepath.Join(dir, "genesis.json")
	keyPath := filepath.Join(dir, "validator-key.json")
	genesis, keyFile, err := createGenesisFiles(genesisPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if genesis.ChainID != "chainlab-local" || genesis.GenesisTimeUnix <= 0 {
		t.Fatalf("genesis identity = %+v", genesis)
	}
	if len(genesis.Validators) != 1 || genesis.Validators[0] != keyFile.Address {
		t.Fatalf("genesis validators = %#v, key address = %q", genesis.Validators, keyFile.Address)
	}
	genesisRaw, err := os.ReadFile(genesisPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(genesisRaw, []byte("private_key")) || bytes.Contains(genesisRaw, []byte(keyFile.PrivateKey)) {
		t.Fatal("shared genesis contains validator secret material")
	}
	loadedGenesis, err := readGenesisFile(genesisPath)
	if err != nil {
		t.Fatal(err)
	}
	if loadedGenesis.Validators[0] != keyFile.Address {
		t.Fatalf("loaded validator = %q", loadedGenesis.Validators[0])
	}
	loadedKey, err := readValidatorKeyFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := crypto.AddressFromPrivateKey(loadedKey); got != keyFile.Address {
		t.Fatalf("loaded key address = %q", got)
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{genesisPath, keyPath} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("%s permissions = %#o", path, got)
			}
		}
	}
}

func TestRunKeygenCommandWritesExclusiveSecretFile(t *testing.T) {
	if err := runKeygenCommand(nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "requires --out") {
		t.Fatalf("missing output error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "validator-key.json")
	var out bytes.Buffer
	if err := runKeygenCommand([]string{"--out", path}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "private_key") {
		t.Fatal("keygen output contains private key")
	}
	key, err := readValidatorKeyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), crypto.AddressFromPrivateKey(key)) {
		t.Fatalf("keygen output = %s", out.String())
	}
	if err := runKeygenCommand([]string{"--out", path}, &bytes.Buffer{}); err == nil || !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite error = %v", err)
	}
}

func TestRunInitCommandRequiresOutputAndCreatesDefaultKeyFile(t *testing.T) {
	if err := runInitCommand(nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "requires --out") {
		t.Fatalf("missing output error = %v", err)
	}
	genesisPath := filepath.Join(t.TempDir(), "network.json")
	var out bytes.Buffer
	if err := runInitCommand([]string{"--out", genesisPath}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "private_key") {
		t.Fatal("init output contains validator secret material")
	}
	if _, err := os.Stat(defaultValidatorKeyPath(genesisPath)); err != nil {
		t.Fatalf("default validator key file: %v", err)
	}

	explicitDir := t.TempDir()
	explicitGenesisPath := filepath.Join(explicitDir, "genesis.json")
	explicitKeyPath := filepath.Join(explicitDir, "node-key.json")
	out.Reset()
	if err := runInitCommand([]string{
		"--out", explicitGenesisPath,
		"--key-out", explicitKeyPath,
	}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(explicitKeyPath); err != nil {
		t.Fatalf("explicit validator key file: %v", err)
	}
	if strings.Contains(out.String(), "private_key") {
		t.Fatal("init output contains validator secret material")
	}
}

func TestCreateGenesisFilesNeverOverwritesAndRollsBackPartialCreation(t *testing.T) {
	dir := t.TempDir()
	genesisPath := filepath.Join(dir, "genesis.json")
	keyPath := filepath.Join(dir, "validator-key.json")
	if _, _, err := createGenesisFiles(genesisPath, keyPath); err != nil {
		t.Fatal(err)
	}
	genesisBefore, err := os.ReadFile(genesisPath)
	if err != nil {
		t.Fatal(err)
	}
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := createGenesisFiles(genesisPath, keyPath); err == nil {
		t.Fatal("second init overwrote existing artifacts")
	}
	genesisAfter, _ := os.ReadFile(genesisPath)
	keyAfter, _ := os.ReadFile(keyPath)
	if !bytes.Equal(genesisBefore, genesisAfter) || !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("failed init changed existing artifacts")
	}

	rollbackDir := t.TempDir()
	rollbackGenesis := filepath.Join(rollbackDir, "genesis.json")
	existingKey := filepath.Join(rollbackDir, "validator-key.json")
	sentinel := []byte("existing validator key")
	if err := os.WriteFile(existingKey, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := createGenesisFiles(rollbackGenesis, existingKey); err == nil {
		t.Fatal("init unexpectedly replaced existing validator key")
	}
	if _, err := os.Stat(rollbackGenesis); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial genesis was not cleaned up: %v", err)
	}
	keyAfter, err = os.ReadFile(existingKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(keyAfter, sentinel) {
		t.Fatal("rollback changed pre-existing validator key")
	}

	samePath := filepath.Join(t.TempDir(), "artifact.json")
	if _, _, err := createGenesisFiles(samePath, samePath); err == nil {
		t.Fatal("same genesis and key path was accepted")
	}
	if _, err := os.Stat(samePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("same-path rejection created a file: %v", err)
	}
}

func TestReadGenesisFileEnforcesCanonicalArtifact(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validator := crypto.AddressFromPrivateKey(key)
	base := `{
		"chain_id":"chainlab-local",
		"genesis_time_unix":1700000000,
		"validators":["` + validator + `"],
		"balances":{"` + validator + `":1}
	}`
	tests := []struct {
		name string
		raw  string
	}{
		{name: "missing time", raw: strings.Replace(base, `"genesis_time_unix":1700000000,`, "", 1)},
		{name: "unknown field", raw: strings.Replace(base, `"balances":`, `"unknown":true,"balances":`, 1)},
		{name: "legacy private key", raw: strings.Replace(base, `"balances":`, `"private_key":"secret","balances":`, 1)},
		{name: "case variant", raw: strings.Replace(base, `"chain_id"`, `"Chain_ID"`, 1)},
		{name: "duplicate chain id", raw: strings.Replace(base, `"chain_id":"chainlab-local"`, `"chain_id":"chainlab-local","chain_id":"other"`, 1)},
		{name: "duplicate balance address", raw: strings.Replace(base, `"`+validator+`":1`, `"`+validator+`":1,"`+validator+`":2`, 1)},
		{name: "empty validators", raw: strings.Replace(base, `["`+validator+`"]`, `[]`, 1)},
		{name: "null balances", raw: strings.Replace(base, `{"`+validator+`":1}`, `null`, 1)},
		{name: "trailing value", raw: base + `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "genesis.json")
			writeRawTestFile(t, path, []byte(test.raw), 0o600)
			if _, err := readGenesisFile(path); err == nil {
				t.Fatal("invalid genesis was accepted")
			}
		})
	}

	emptyBalances := strings.Replace(base, `{"`+validator+`":1}`, `{}`, 1)
	path := filepath.Join(t.TempDir(), "genesis.json")
	writeRawTestFile(t, path, []byte(emptyBalances), 0o600)
	loaded, err := readGenesisFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Balances) != 0 {
		t.Fatalf("empty balances = %#v", loaded.Balances)
	}
}

func TestReadValidatorKeyFileEnforcesCanonicalArtifact(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := ValidatorKeyFile{
		Address:    crypto.AddressFromPrivateKey(key),
		PrivateKey: crypto.PrivateKeyToHex(key),
	}
	validPath := filepath.Join(t.TempDir(), "validator-key.json")
	writeJSONTestFile(t, validPath, keyFile, 0o600)
	loaded, err := readValidatorKeyFile(validPath)
	if err != nil {
		t.Fatal(err)
	}
	if crypto.AddressFromPrivateKey(loaded) != keyFile.Address {
		t.Fatal("loaded validator key changed identity")
	}

	base := `{"address":"` + keyFile.Address + `","private_key":"` + keyFile.PrivateKey + `"}`
	tests := []struct {
		name string
		raw  string
	}{
		{name: "unknown field", raw: strings.Replace(base, `}`, `,"unknown":true}`, 1)},
		{name: "case variant", raw: strings.Replace(base, `"private_key"`, `"Private_Key"`, 1)},
		{name: "duplicate private key", raw: strings.Replace(base, `}`, `,"private_key":"`+keyFile.PrivateKey+`"}`, 1)},
		{name: "mismatched address", raw: strings.Replace(base, keyFile.Address, crypto.AddressFromPrivateKey(otherKey), 1)},
		{name: "non-canonical private key", raw: strings.Replace(base, keyFile.PrivateKey, strings.TrimPrefix(keyFile.PrivateKey, "0x"), 1)},
		{name: "zero private key", raw: strings.Replace(base, keyFile.PrivateKey, "0x"+strings.Repeat("0", 64), 1)},
		{name: "trailing value", raw: base + `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "validator-key.json")
			writeRawTestFile(t, path, []byte(test.raw), 0o600)
			if _, err := readValidatorKeyFile(path); err == nil {
				t.Fatal("invalid validator key file was accepted")
			}
		})
	}
	if runtime.GOOS != "windows" {
		path := filepath.Join(t.TempDir(), "validator-key.json")
		writeJSONTestFile(t, path, keyFile, 0o644)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readValidatorKeyFile(path); err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("insecure key permissions error = %v", err)
		}
	}
}

func TestGenesisAndValidatorKeyReadersBoundSizeAndDepth(t *testing.T) {
	oversizedGenesis := filepath.Join(t.TempDir(), "genesis.json")
	file, err := os.OpenFile(oversizedGenesis, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxGenesisFileBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readGenesisFile(oversizedGenesis); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized genesis error = %v", err)
	}

	oversizedKey := filepath.Join(t.TempDir(), "validator-key.json")
	file, err = os.OpenFile(oversizedKey, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxValidatorKeyFileBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readValidatorKeyFile(oversizedKey); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized key error = %v", err)
	}

	deep := strings.Repeat("[", maxJSONArtifactDepth+1) + "0" + strings.Repeat("]", maxJSONArtifactDepth+1)
	deepGenesis := `{"chain_id":"chainlab-local","genesis_time_unix":1700000000,"validators":` + deep + `,"balances":{}}`
	deepPath := filepath.Join(t.TempDir(), "genesis.json")
	writeRawTestFile(t, deepPath, []byte(deepGenesis), 0o600)
	if _, err := readGenesisFile(deepPath); err == nil || !strings.Contains(err.Error(), "maximum JSON depth") {
		t.Fatalf("deep genesis error = %v", err)
	}
}

func TestBuildNodeConfigUsesSharedGenesisAndSeparateKeys(t *testing.T) {
	keyA, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := crypto.AddressFromPrivateKey(keyA)
	validatorB := crypto.AddressFromPrivateKey(keyB)
	genesis := GenesisFile{
		ChainID:         "chainlab-local",
		GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
		Validators:      []string{validatorA, validatorB},
		Balances:        map[string]uint64{},
	}
	dir := t.TempDir()
	genesisPath := filepath.Join(dir, "genesis.json")
	keyAPath := filepath.Join(dir, "validator-a.json")
	keyBPath := filepath.Join(dir, "validator-b.json")
	writeJSONTestFile(t, genesisPath, genesis, 0o600)
	writeJSONTestFile(t, keyAPath, validatorKeyFileForTest(keyA), 0o600)
	writeJSONTestFile(t, keyBPath, validatorKeyFileForTest(keyB), 0o600)

	configA, err := buildNodeConfig(nodeOptions{GenesisPath: genesisPath, KeyPath: keyAPath})
	if err != nil {
		t.Fatal(err)
	}
	configB, err := buildNodeConfig(nodeOptions{GenesisPath: genesisPath, KeyPath: keyBPath})
	if err != nil {
		t.Fatal(err)
	}
	if len(configA.GenesisBalance) != 0 || len(configB.GenesisBalance) != 0 {
		t.Fatal("empty shared balances were replaced with local defaults")
	}
	if configA.Role != node.RoleValidator || configB.Role != node.RoleValidator {
		t.Fatalf("default roles = %q and %q, want validator", configA.Role, configB.Role)
	}
	nodeA, err := node.New(configA)
	if err != nil {
		t.Fatal(err)
	}
	nodeB, err := node.New(configB)
	if err != nil {
		t.Fatal(err)
	}
	if nodeA.Head().Hash() != nodeB.Head().Hash() {
		t.Fatalf("shared genesis hashes differ: %s != %s", nodeA.Head().Hash(), nodeB.Head().Hash())
	}
}

func TestBuildNodeConfigKeySourcesAndSharedGenesisRequirements(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, options := range []nodeOptions{
		{DataDir: t.TempDir()},
		{Peers: []string{"http://127.0.0.1:8548"}},
	} {
		if _, err := buildNodeConfig(options); err == nil || !strings.Contains(err.Error(), "shared genesis") {
			t.Fatalf("build config error = %v", err)
		}
	}
	for _, options := range []nodeOptions{
		{PrivateKeyHex: crypto.PrivateKeyToHex(key)},
		{PrivateKeyHex: "0x" + strings.Repeat("0", 64)},
		{PrivateKeyHex: crypto.PrivateKeyToHex(key), KeyPath: filepath.Join(t.TempDir(), "missing-key.json")},
		{PrivateKeyHex: crypto.PrivateKeyToHex(key), DataDir: t.TempDir()},
	} {
		if _, err := buildNodeConfig(options); err == nil || !strings.Contains(err.Error(), "--key-file instead of --private-key") {
			t.Fatalf("raw private key error = %v", err)
		}
	}

	dir := t.TempDir()
	genesisPath := filepath.Join(dir, "genesis.json")
	keyPath := filepath.Join(dir, "validator-key.json")
	genesis, _, err := createGenesisFiles(genesisPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	config, err := buildNodeConfig(nodeOptions{GenesisPath: genesisPath, KeyPath: keyPath, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if config.DataDir != dataDir || config.ChainID != genesis.ChainID {
		t.Fatalf("node config = %+v", config)
	}
	if config.Role != node.RoleValidator {
		t.Fatalf("default role = %q, want validator", config.Role)
	}
	if _, err := buildNodeConfig(nodeOptions{GenesisPath: genesisPath}); err == nil || !strings.Contains(err.Error(), "--key-file") {
		t.Fatalf("missing key source error = %v", err)
	}

	observerDataDir := t.TempDir()
	observerConfig, err := buildNodeConfig(nodeOptions{
		Role:        string(node.RoleObserver),
		GenesisPath: genesisPath,
		DataDir:     observerDataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observerConfig.Role != node.RoleObserver || observerConfig.ProposerKey != nil || observerConfig.DataDir != observerDataDir {
		t.Fatalf("observer config = %+v", observerConfig)
	}
	observer, err := node.New(observerConfig)
	if err != nil {
		t.Fatalf("construct observer from CLI config: %v", err)
	}
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Errorf("close observer: %v", err)
		}
	})
	if _, err := buildNodeConfig(nodeOptions{
		Role:        string(node.RoleObserver),
		GenesisPath: genesisPath,
		KeyPath:     keyPath,
	}); err == nil || !strings.Contains(err.Error(), "must not configure --key-file") {
		t.Fatalf("observer key-file error = %v", err)
	}
	if _, err := buildNodeConfig(nodeOptions{Role: "archive", GenesisPath: genesisPath}); err == nil || !strings.Contains(err.Error(), "unsupported node role") {
		t.Fatalf("unknown role error = %v", err)
	}
}

func TestBuildNodeConfigRejectsKeyOutsideGenesisValidatorSet(t *testing.T) {
	validatorKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	outsiderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	genesisPath := filepath.Join(t.TempDir(), "genesis.json")
	outsiderKeyPath := filepath.Join(t.TempDir(), "outsider-key.json")
	writeJSONTestFile(t, genesisPath, GenesisFile{
		ChainID:         "chainlab-local",
		GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
		Validators:      []string{crypto.AddressFromPrivateKey(validatorKey)},
		Balances:        map[string]uint64{},
	}, 0o600)
	writeJSONTestFile(t, outsiderKeyPath, validatorKeyFileForTest(outsiderKey), 0o600)
	if _, err := buildNodeConfig(nodeOptions{
		GenesisPath: genesisPath,
		KeyPath:     outsiderKeyPath,
	}); err == nil || !strings.Contains(err.Error(), "not in the genesis validator set") {
		t.Fatalf("outsider key error = %v", err)
	}
}

func TestRejectDuplicateSecuritySensitiveFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--out", "one", "--out=two"},
		{"--private-key", "one", "-private-key", "two"},
		{"--key-file=one", "--key-file", "two"},
		{"--role", "validator", "--role=observer"},
	} {
		if err := rejectDuplicateFlags(args, "out", "private-key", "key-file", "role"); err == nil || !strings.Contains(err.Error(), "only once") {
			t.Fatalf("duplicate flag error for %#v = %v", args, err)
		}
	}
	if err := rejectDuplicateFlags([]string{"--peer", "one", "--peer", "two"}, "private-key"); err != nil {
		t.Fatalf("repeatable peer flag was rejected: %v", err)
	}
}

func TestBuildNodeConfigRejectsExplicitPrivateKeyWithPublicGenesis(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validator := crypto.AddressFromPrivateKey(key)
	genesisPath := filepath.Join(t.TempDir(), "genesis.json")
	writeJSONTestFile(t, genesisPath, GenesisFile{
		ChainID:         "chainlab-local",
		GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
		Validators:      []string{validator},
		Balances:        map[string]uint64{validator: 1_000_000},
	}, 0o600)
	if _, err := buildNodeConfig(nodeOptions{
		GenesisPath:   genesisPath,
		PrivateKeyHex: crypto.PrivateKeyToHex(key),
	}); err == nil || !strings.Contains(err.Error(), "--key-file instead of --private-key") {
		t.Fatalf("explicit private key error = %v", err)
	}
}

func validatorKeyFileForTest(key crypto.PrivateKey) ValidatorKeyFile {
	return ValidatorKeyFile{
		Address:    crypto.AddressFromPrivateKey(key),
		PrivateKey: crypto.PrivateKeyToHex(key),
	}
}

func writeJSONTestFile(t *testing.T, path string, value any, mode os.FileMode) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeRawTestFile(t, path, raw, mode)
}

func writeRawTestFile(t *testing.T, path string, raw []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
}

func TestPeerListFlagAcceptsRepeatedPeers(t *testing.T) {
	flags := flag.NewFlagSet("node", flag.ContinueOnError)
	var peers peerListFlag
	flags.Var(&peers, "peer", "peer URL")

	if err := flags.Parse([]string{"--peer", "http://127.0.0.1:18547", "--peer", "http://127.0.0.1:18548"}); err != nil {
		t.Fatal(err)
	}
	got := peers.Values()
	if len(got) != 2 {
		t.Fatalf("peer count = %d", len(got))
	}
	if got[0] != "http://127.0.0.1:18547" || got[1] != "http://127.0.0.1:18548" {
		t.Fatalf("peers = %#v", got)
	}
}

func TestBuildSignedTransferFetchesNonceFromRPC(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	tx, err := buildSignedTransfer(server.URL, crypto.PrivateKeyToHex(key), bob, 100, 21_000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if tx.From != alice {
		t.Fatalf("from = %q", tx.From)
	}
	if tx.Nonce != 0 {
		t.Fatalf("nonce = %d", tx.Nonce)
	}
	if tx.Signature == "" {
		t.Fatal("transaction should be signed")
	}
	if !crypto.Verify(alice, tx.SigningBytes(), tx.Signature) {
		t.Fatal("signature should verify")
	}
}

func TestBuildSignedTransactionForceSignerWhenFromMatchesKey(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	account := crypto.AddressFromPrivateKey(key)
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{account: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	tx, err := buildSignedTransactionFromSpec(server.URL, crypto.PrivateKeyToHex(key), signedTransactionSpec{
		txType:       types.TxAccountRecovery,
		fromOverride: account,
		forceSigner:  true,
		gasLimit:     55_000,
		gasPrice:     1,
		payload:      map[string]string{"action": "clear"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tx.Signer != account {
		t.Fatalf("signer = %q, want %q", tx.Signer, account)
	}
	if tx.Nonce != 0 {
		t.Fatalf("nonce = %d", tx.Nonce)
	}
	if !crypto.Verify(account, tx.SigningBytes(), tx.Signature) {
		t.Fatal("forced signer transaction signature should verify")
	}
}

func TestSubmitTransferCommandSendsSignedTx(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var out bytes.Buffer
	if err := transferCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--to", bob,
		"--value", "100",
	}, &out); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Hash == "" {
		t.Fatal("transfer command should print transaction hash")
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("block transactions = %d", len(block.Transactions))
	}
	if got := n.Account(bob).Balance; got != 100 {
		t.Fatalf("bob balance = %d", got)
	}
}

func TestTransferCommandSupportsEIP1559FeeCaps(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var out bytes.Buffer
	if err := transferCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--to", bob,
		"--value", "100",
		"--max-fee-per-gas", "5",
		"--max-priority-fee-per-gas", "2",
	}, &out); err != nil {
		t.Fatal(err)
	}
	pool := n.TxPool()
	if pool.PendingCount != 1 {
		t.Fatalf("txpool = %+v", pool)
	}
	if pool.Pending[0].MaxFeePerGas != 5 || pool.Pending[0].MaxPriorityFeePerGas != 2 {
		t.Fatalf("fee caps = max %d priority %d", pool.Pending[0].MaxFeePerGas, pool.Pending[0].MaxPriorityFeePerGas)
	}
}

func TestTransferCommandSupportsPaymasterSponsoredFees(t *testing.T) {
	userKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	paymasterKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	user := crypto.AddressFromPrivateKey(userKey)
	paymaster := crypto.AddressFromPrivateKey(paymasterKey)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.NewDevelopment(node.Config{
		ChainID:     "chainlab-local",
		ProposerKey: paymasterKey,
		GenesisBalance: map[string]uint64{
			user:      100,
			paymaster: 100_000,
		}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var out bytes.Buffer
	if err := transferCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(userKey),
		"--to", receiver,
		"--value", "100",
		"--gas-price", "2",
		"--paymaster-private-key", crypto.PrivateKeyToHex(paymasterKey),
	}, &out); err != nil {
		t.Fatal(err)
	}
	pool := n.TxPool()
	if pool.PendingCount != 1 {
		t.Fatalf("txpool = %+v", pool)
	}
	tx := pool.Pending[0]
	if tx.Paymaster != paymaster || tx.PaymasterSignature == "" {
		t.Fatalf("sponsored tx = %+v", tx)
	}
	if !crypto.Verify(paymaster, tx.PaymasterSigningBytes(), tx.PaymasterSignature) {
		t.Fatal("paymaster signature should verify")
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if block.Receipts[0].FeePayer != paymaster {
		t.Fatalf("receipt = %+v", block.Receipts[0])
	}
	if got := n.Account(user).Balance; got != 0 {
		t.Fatalf("user balance = %d", got)
	}
}

func TestBatchTransferCommandSendsSignedBatchTx(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var out bytes.Buffer
	if err := batchTransferCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--to", bob + ":10",
		"--to", carol + ":20",
	}, &out); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Hash == "" {
		t.Fatal("batch transfer command should print transaction hash")
	}
	pool := n.TxPool()
	if pool.PendingCount != 1 {
		t.Fatalf("txpool = %+v", pool)
	}
	tx := pool.Pending[0]
	if tx.Type != types.TxBatch || len(tx.Batch) != 2 || tx.GasLimit != 84_000 {
		t.Fatalf("batch tx = %+v", tx)
	}
	if !crypto.Verify(alice, tx.SigningBytes(), tx.Signature) {
		t.Fatal("batch transaction signature should verify")
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 1 || block.Transactions[0].Type != types.TxBatch {
		t.Fatalf("block transactions = %#v", block.Transactions)
	}
	if got := n.Account(bob).Balance; got != 10 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := n.Account(carol).Balance; got != 20 {
		t.Fatalf("carol balance = %d", got)
	}
}

func TestTransferCommandCanBuildSmartAccountTx(t *testing.T) {
	ownerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := crypto.AddressFromPrivateKey(ownerKey)
	smartAccount := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    ownerKey,
		GenesisBalance: map[string]uint64{owner: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var out bytes.Buffer
	if err := transferCommand([]string{
		"--rpc", server.URL,
		"--from", smartAccount,
		"--private-key", crypto.PrivateKeyToHex(ownerKey),
		"--to", receiver,
		"--value", "100",
		"--raw-only",
	}, &out); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Transaction types.Transaction `json:"transaction"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	tx := response.Transaction
	if tx.From != smartAccount || tx.Signer != owner {
		t.Fatalf("smart account tx = %+v", tx)
	}
	if tx.Nonce != 0 {
		t.Fatalf("nonce = %d", tx.Nonce)
	}
	if !crypto.Verify(owner, tx.SigningBytes(), tx.Signature) {
		t.Fatal("smart account transaction signature should verify against owner")
	}
}

func TestSetCodeCommandDelegatesEOAAndOwnerTransfer(t *testing.T) {
	eoaKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	eoa := crypto.AddressFromPrivateKey(eoaKey)
	owner := crypto.AddressFromPrivateKey(ownerKey)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    eoaKey,
		GenesisBalance: map[string]uint64{eoa: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var setCodeOut bytes.Buffer
	if err := setCodeCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(eoaKey),
		"--code-id", contracts.AccountCodeID,
		"--owner", owner,
	}, &setCodeOut); err != nil {
		t.Fatal(err)
	}
	var blockOut bytes.Buffer
	if err := produceCommand([]string{"--rpc", server.URL}, &blockOut); err != nil {
		t.Fatal(err)
	}
	if account := n.Account(eoa); account.DelegatedCodeID != contracts.AccountCodeID {
		t.Fatalf("delegated account = %+v", account)
	}

	var transferOut bytes.Buffer
	if err := transferCommand([]string{
		"--rpc", server.URL,
		"--from", eoa,
		"--private-key", crypto.PrivateKeyToHex(ownerKey),
		"--to", receiver,
		"--value", "100",
	}, &transferOut); err != nil {
		t.Fatal(err)
	}
	var transferBlockOut bytes.Buffer
	if err := produceCommand([]string{"--rpc", server.URL}, &transferBlockOut); err != nil {
		t.Fatal(err)
	}
	if receiverBalance := n.Account(receiver).Balance; receiverBalance != 100 {
		t.Fatalf("receiver balance = %d", receiverBalance)
	}
	if ownerNonce := n.Account(owner).Nonce; ownerNonce != 0 {
		t.Fatalf("owner nonce = %d", ownerNonce)
	}
}

func TestSessionKeyCommandInstallsKeyAndSessionTransferWorks(t *testing.T) {
	ownerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sessionKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := crypto.AddressFromPrivateKey(ownerKey)
	session := crypto.AddressFromPrivateKey(sessionKey)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    ownerKey,
		GenesisBalance: map[string]uint64{owner: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var deployOut bytes.Buffer
	if err := deployCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(ownerKey),
		"--code-id", contracts.AccountCodeID,
		"--arg", "owner=" + owner,
	}, &deployOut); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	account := deployBlock.Receipts[0].ContractAddress

	var fundOut bytes.Buffer
	if err := transferCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(ownerKey),
		"--to", account,
		"--value", "200000",
	}, &fundOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	var sessionOut bytes.Buffer
	if err := sessionKeyCommand([]string{
		"--rpc", server.URL,
		"--from", account,
		"--private-key", crypto.PrivateKeyToHex(ownerKey),
		"--key", session,
		"--limit", "100",
		"--to", receiver,
	}, &sessionOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(account).Storage["session:"+session+":limit"]; got != "100" {
		t.Fatalf("session limit = %q", got)
	}

	var transferOut bytes.Buffer
	if err := transferCommand([]string{
		"--rpc", server.URL,
		"--from", account,
		"--private-key", crypto.PrivateKeyToHex(sessionKey),
		"--to", receiver,
		"--value", "40",
	}, &transferOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if receiverBalance := n.Account(receiver).Balance; receiverBalance != 40 {
		t.Fatalf("receiver balance = %d", receiverBalance)
	}
	if got := n.Account(account).Storage["session:"+session+":spent"]; got != "40" {
		t.Fatalf("session spent = %q", got)
	}
}

func TestSessionKeyCommandInstallsCallPolicyAndSessionCallWorks(t *testing.T) {
	ownerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sessionKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := crypto.AddressFromPrivateKey(ownerKey)
	session := crypto.AddressFromPrivateKey(sessionKey)
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    ownerKey,
		GenesisBalance: map[string]uint64{owner: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var accountOut bytes.Buffer
	if err := deployCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(ownerKey),
		"--code-id", contracts.AccountCodeID,
		"--arg", "owner=" + owner,
	}, &accountOut); err != nil {
		t.Fatal(err)
	}
	accountBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	account := accountBlock.Receipts[0].ContractAddress

	var counterOut bytes.Buffer
	if err := deployCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(ownerKey),
		"--code-id", "counter.v1",
		"--arg", "initial=0",
	}, &counterOut); err != nil {
		t.Fatal(err)
	}
	counterBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	counter := counterBlock.Receipts[0].ContractAddress

	var fundOut bytes.Buffer
	if err := transferCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(ownerKey),
		"--to", account,
		"--value", "300000",
	}, &fundOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	var sessionOut bytes.Buffer
	if err := sessionKeyCommand([]string{
		"--rpc", server.URL,
		"--from", account,
		"--private-key", crypto.PrivateKeyToHex(ownerKey),
		"--key", session,
		"--call-to", counter,
		"--call-method", "increment",
	}, &sessionOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(account).Storage["session:"+session+":call_method"]; got != "increment" {
		t.Fatalf("session call method = %q", got)
	}

	var callOut bytes.Buffer
	if err := contractCallCommand([]string{
		"--rpc", server.URL,
		"--from", account,
		"--private-key", crypto.PrivateKeyToHex(sessionKey),
		"--to", counter,
		"--method", "increment",
		"--arg", "amount=3",
	}, &callOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(counter).Storage["count"]; got != "3" {
		t.Fatalf("counter value = %q", got)
	}
	if got := n.Account(session).Nonce; got != 0 {
		t.Fatalf("session nonce = %d", got)
	}
}

func TestRecoveryCommandRotatesAccountOwnerEndToEnd(t *testing.T) {
	ownerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	guardianAKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	guardianBKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	newOwnerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := crypto.AddressFromPrivateKey(ownerKey)
	guardianA := crypto.AddressFromPrivateKey(guardianAKey)
	guardianB := crypto.AddressFromPrivateKey(guardianBKey)
	newOwner := crypto.AddressFromPrivateKey(newOwnerKey)
	receiver := "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	const guardianBalance uint64 = 500_000

	n, err := node.NewDevelopment(node.Config{
		ChainID:     "chainlab-local",
		ProposerKey: ownerKey,
		GenesisBalance: map[string]uint64{
			owner:     2_000_000,
			guardianA: guardianBalance,
			guardianB: guardianBalance,
		}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var deployOut bytes.Buffer
	if err := deployCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(ownerKey),
		"--code-id", contracts.AccountCodeID,
		"--arg", "owner=" + owner,
	}, &deployOut); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(deployBlock.Receipts) != 1 || deployBlock.Receipts[0].ContractAddress == "" {
		t.Fatalf("deploy receipts = %#v", deployBlock.Receipts)
	}
	account := deployBlock.Receipts[0].ContractAddress

	var fundOut bytes.Buffer
	if err := transferCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(ownerKey),
		"--to", account,
		"--value", "400000",
	}, &fundOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(account).Balance; got != 400_000 {
		t.Fatalf("funded account balance = %d", got)
	}

	submitRecovery := func(key crypto.PrivateKey, action string, extra ...string) types.Block {
		t.Helper()
		args := []string{
			"--rpc", server.URL,
			"--from", account,
			"--private-key", crypto.PrivateKeyToHex(key),
			"--action", action,
		}
		args = append(args, extra...)
		var out bytes.Buffer
		if err := recoveryCommand(args, &out); err != nil {
			t.Fatalf("recovery %s: %v", action, err)
		}
		block, err := n.ProduceBlock()
		if err != nil {
			t.Fatalf("produce recovery %s block: %v", action, err)
		}
		if len(block.Transactions) != 1 || len(block.Receipts) != 1 || !block.Receipts[0].Success {
			t.Fatalf("recovery %s block = %#v", action, block)
		}
		return block
	}
	assertRecoveryTx := func(block types.Block, signer string, nonce uint64, action string) {
		t.Helper()
		tx := block.Transactions[0]
		if tx.Type != types.TxAccountRecovery || tx.From != account || tx.Signer != signer || tx.Nonce != nonce {
			t.Fatalf("recovery %s transaction = %+v", action, tx)
		}
		if tx.GasLimit != 55_000 || tx.GasPrice != 1 || tx.Payload["action"] != action {
			t.Fatalf("recovery %s gas/payload = %+v", action, tx)
		}
	}

	configureBlock := submitRecovery(ownerKey, "configure",
		"--guardian", guardianA,
		"--guardian", guardianB,
		"--threshold", "2",
		"--delay", "2",
	)
	assertRecoveryTx(configureBlock, owner, 0, "configure")
	configured := n.Account(account)
	if configured.Storage["recovery:guardians"] != guardianA+","+guardianB ||
		configured.Storage["recovery:threshold"] != "2" || configured.Storage["recovery:delay"] != "2" {
		t.Fatalf("recovery configuration = %#v", configured.Storage)
	}

	approveABlock := submitRecovery(guardianAKey, "approve", "--new-owner", newOwner)
	assertRecoveryTx(approveABlock, guardianA, 1, "approve")
	if approveABlock.Receipts[0].FeePayer != guardianA {
		t.Fatalf("guardian A fee payer = %q", approveABlock.Receipts[0].FeePayer)
	}
	approveBBlock := submitRecovery(guardianBKey, "approve", "--new-owner", newOwner)
	assertRecoveryTx(approveBBlock, guardianB, 2, "approve")
	if approveBBlock.Receipts[0].FeePayer != guardianB {
		t.Fatalf("guardian B fee payer = %q", approveBBlock.Receipts[0].FeePayer)
	}
	wantExecuteAfter := approveBBlock.Header.Height + 2
	if got := n.Account(account).Storage["recovery:execute_after"]; got != strconv.FormatUint(wantExecuteAfter, 10) {
		t.Fatalf("execute after = %q, want %d", got, wantExecuteAfter)
	}

	for i := uint64(0); i < 2; i++ {
		block, err := n.ProduceBlock()
		if err != nil {
			t.Fatalf("produce delay block %d: %v", i+1, err)
		}
		if len(block.Transactions) != 0 {
			t.Fatalf("delay block %d transactions = %#v", block.Header.Height, block.Transactions)
		}
	}

	executeBlock := submitRecovery(guardianAKey, "execute", "--new-owner", newOwner)
	assertRecoveryTx(executeBlock, guardianA, 3, "execute")
	if executeBlock.Receipts[0].FeePayer != guardianA {
		t.Fatalf("execute fee payer = %q", executeBlock.Receipts[0].FeePayer)
	}
	if got := n.Account(account).Storage["owner"]; got != newOwner {
		t.Fatalf("recovered account owner = %q, want %q", got, newOwner)
	}
	if got := n.Account(guardianA); got.Balance != guardianBalance-110_000 || got.Nonce != 0 {
		t.Fatalf("guardian A account = %+v", got)
	}
	if got := n.Account(guardianB); got.Balance != guardianBalance-55_000 || got.Nonce != 0 {
		t.Fatalf("guardian B account = %+v", got)
	}

	var transferOut bytes.Buffer
	if err := transferCommand([]string{
		"--rpc", server.URL,
		"--from", account,
		"--private-key", crypto.PrivateKeyToHex(newOwnerKey),
		"--to", receiver,
		"--value", "100",
	}, &transferOut); err != nil {
		t.Fatal(err)
	}
	transferBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(transferBlock.Transactions) != 1 || transferBlock.Transactions[0].Signer != newOwner || transferBlock.Transactions[0].Nonce != 4 {
		t.Fatalf("new owner transfer = %#v", transferBlock.Transactions)
	}
	if got := n.Account(receiver).Balance; got != 100 {
		t.Fatalf("receiver balance = %d", got)
	}
	if got := n.Account(newOwner).Nonce; got != 0 {
		t.Fatalf("new owner nonce = %d", got)
	}
}

func TestRecoveryCommandRejectsInvalidActionFlags(t *testing.T) {
	account := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	guardian := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	newOwner := "0xcccccccccccccccccccccccccccccccccccccccc"
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing from", args: []string{"--action", "clear"}, want: "from account is required"},
		{name: "unknown action", args: []string{"--from", account, "--action", "recover"}, want: "action must be"},
		{name: "configure missing guardian", args: []string{"--from", account, "--action", "configure", "--threshold", "1", "--delay", "0"}, want: "at least one --guardian"},
		{name: "configure missing threshold", args: []string{"--from", account, "--action", "configure", "--guardian", guardian, "--delay", "0"}, want: "positive --threshold"},
		{name: "configure zero threshold", args: []string{"--from", account, "--action", "configure", "--guardian", guardian, "--threshold", "0", "--delay", "0"}, want: "positive --threshold"},
		{name: "configure missing explicit delay", args: []string{"--from", account, "--action", "configure", "--guardian", guardian, "--threshold", "1"}, want: "explicit --delay"},
		{name: "configure rejects new owner and accepts zero delay", args: []string{"--from", account, "--action", "configure", "--guardian", guardian, "--threshold", "1", "--delay", "0", "--new-owner", newOwner}, want: "does not allow --new-owner"},
		{name: "approve missing new owner", args: []string{"--from", account, "--action", "approve"}, want: "approve requires --new-owner"},
		{name: "approve rejects configure flags", args: []string{"--from", account, "--action", "approve", "--new-owner", newOwner, "--guardian", guardian}, want: "approve does not allow"},
		{name: "execute missing new owner", args: []string{"--from", account, "--action", "execute"}, want: "execute requires --new-owner"},
		{name: "execute rejects configure flags", args: []string{"--from", account, "--action", "execute", "--new-owner", newOwner, "--delay", "1"}, want: "execute does not allow"},
		{name: "cancel rejects action flags", args: []string{"--from", account, "--action", "cancel", "--threshold", "1"}, want: "cancel does not allow recovery action flags"},
		{name: "clear rejects action flags", args: []string{"--from", account, "--action", "clear", "--new-owner", newOwner}, want: "clear does not allow recovery action flags"},
		{name: "rejects positional arguments", args: []string{"--from", account, "--action", "clear", "extra"}, want: "unexpected recovery arguments"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := recoveryCommand(test.args, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestBatchTransferCommandCanBuildSmartAccountTx(t *testing.T) {
	ownerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := crypto.AddressFromPrivateKey(ownerKey)
	smartAccount := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    ownerKey,
		GenesisBalance: map[string]uint64{owner: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var out bytes.Buffer
	if err := batchTransferCommand([]string{
		"--rpc", server.URL,
		"--from", smartAccount,
		"--private-key", crypto.PrivateKeyToHex(ownerKey),
		"--to", bob + ":10",
		"--to", carol + ":20",
		"--raw-only",
	}, &out); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Transaction types.Transaction `json:"transaction"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	tx := response.Transaction
	if tx.Type != types.TxBatch || tx.From != smartAccount || tx.Signer != owner || len(tx.Batch) != 2 {
		t.Fatalf("smart account batch tx = %+v", tx)
	}
	if !crypto.Verify(owner, tx.SigningBytes(), tx.Signature) {
		t.Fatal("smart account batch signature should verify against owner")
	}
}

func TestTransferCommandCanBuildMultisigAccountTx(t *testing.T) {
	ownerAKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerBKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerA := crypto.AddressFromPrivateKey(ownerAKey)
	ownerB := crypto.AddressFromPrivateKey(ownerBKey)
	multisig := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    ownerAKey,
		GenesisBalance: map[string]uint64{ownerA: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var out bytes.Buffer
	if err := transferCommand([]string{
		"--rpc", server.URL,
		"--from", multisig,
		"--private-key", crypto.PrivateKeyToHex(ownerAKey),
		"--auth-private-key", crypto.PrivateKeyToHex(ownerBKey),
		"--to", receiver,
		"--value", "100",
		"--raw-only",
	}, &out); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Transaction types.Transaction `json:"transaction"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	tx := response.Transaction
	if tx.From != multisig || tx.Signer != "" || len(tx.Authorizations) != 2 {
		t.Fatalf("multisig tx = %+v", tx)
	}
	if tx.Authorizations[0].Signer != ownerA || tx.Authorizations[1].Signer != ownerB {
		t.Fatalf("authorizations = %+v", tx.Authorizations)
	}
	for _, authorization := range tx.Authorizations {
		if !crypto.Verify(authorization.Signer, tx.SigningBytes(), authorization.Signature) {
			t.Fatalf("authorization should verify: %+v", authorization)
		}
	}
}

func TestTransferCommandUsesPendingNonceAndQueryMempool(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if err := transferCommand([]string{
			"--rpc", server.URL,
			"--private-key", crypto.PrivateKeyToHex(key),
			"--to", bob,
			"--value", "100",
		}, &out); err != nil {
			t.Fatal(err)
		}
	}

	future := signedCLITx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    3,
		Value:    50,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := n.SubmitTx(future); err != nil {
		t.Fatal(err)
	}

	var poolOut bytes.Buffer
	if err := mempoolCommand([]string{"--rpc", server.URL}, &poolOut); err != nil {
		t.Fatal(err)
	}
	var pool struct {
		PendingCount int                 `json:"pending_count"`
		QueuedCount  int                 `json:"queued_count"`
		Pending      []types.Transaction `json:"pending"`
		Queued       []types.Transaction `json:"queued"`
	}
	if err := json.Unmarshal(poolOut.Bytes(), &pool); err != nil {
		t.Fatal(err)
	}
	if pool.PendingCount != 2 || pool.QueuedCount != 1 || len(pool.Pending) != 2 || len(pool.Queued) != 1 {
		t.Fatalf("mempool = %+v", pool)
	}
	if pool.Queued[0].Hash() != future.Hash() {
		t.Fatalf("queued tx = %+v, want %s", pool.Queued[0], future.Hash())
	}

	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 2 {
		t.Fatalf("block transactions = %d", len(block.Transactions))
	}
	if block.Transactions[0].Nonce != 0 || block.Transactions[1].Nonce != 1 {
		t.Fatalf("transaction nonces = %d, %d", block.Transactions[0].Nonce, block.Transactions[1].Nonce)
	}
}

func TestRawTransactionCommandsBuildAndSubmitRawTx(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var rawOut bytes.Buffer
	if err := transferCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--to", bob,
		"--value", "100",
		"--raw-only",
	}, &rawOut); err != nil {
		t.Fatal(err)
	}
	var rawResult struct {
		Hash string `json:"hash"`
		Raw  string `json:"raw"`
	}
	if err := json.Unmarshal(rawOut.Bytes(), &rawResult); err != nil {
		t.Fatal(err)
	}
	if rawResult.Hash == "" || len(rawResult.Raw) <= 2 || rawResult.Raw[:2] != "0x" {
		t.Fatalf("raw result = %+v", rawResult)
	}
	if pool := n.TxPool(); pool.PendingCount != 0 {
		t.Fatalf("raw-only should not submit tx, pool = %+v", pool)
	}

	var submitOut bytes.Buffer
	if err := rawSubmitCommand([]string{"--rpc", server.URL, "--raw", rawResult.Raw}, &submitOut); err != nil {
		t.Fatal(err)
	}
	var submitResult struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(submitOut.Bytes(), &submitResult); err != nil {
		t.Fatal(err)
	}
	if submitResult.Hash != rawResult.Hash {
		t.Fatalf("submit hash = %q, want %q", submitResult.Hash, rawResult.Hash)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(bob).Balance; got != 100 {
		t.Fatalf("bob balance = %d", got)
	}
}

func TestFaucetRequestCommandRequestsFunds(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := crypto.AddressFromPrivateKey(key)
	recipient := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{proposer: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var out bytes.Buffer
	if err := faucetRequestCommand([]string{
		"--rpc", server.URL,
		"--to", recipient,
		"--amount", "60",
	}, &out); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Hash == "" {
		t.Fatalf("faucet result = %+v", result)
	}
	pool := n.TxPool()
	if pool.PendingCount != 1 || pool.Pending[0].Hash() != result.Hash {
		t.Fatalf("txpool = %+v, hash = %s", pool, result.Hash)
	}

	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(recipient).Balance; got != 60 {
		t.Fatalf("recipient balance = %d", got)
	}
}

func TestStakeCommandAndDisabledValidatorJoin(t *testing.T) {
	proposerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := crypto.AddressFromPrivateKey(proposerKey)
	validator := crypto.AddressFromPrivateKey(validatorKey)
	n, err := node.NewDevelopment(node.Config{
		ChainID:     "chainlab-local",
		ProposerKey: proposerKey,
		GenesisBalance: map[string]uint64{
			proposer:  1_000_000,
			validator: 1_000_000,
		},
		Validators: []string{proposer}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var stakeOut bytes.Buffer
	if err := stakeCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(validatorKey),
		"--value", "500",
	}, &stakeOut); err != nil {
		t.Fatal(err)
	}
	stakeBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(stakeBlock.Transactions) != 1 || stakeBlock.Transactions[0].Type != types.TxStake {
		t.Fatalf("stake block transactions = %#v", stakeBlock.Transactions)
	}
	if got := n.StakeOf(validator); got != 500 {
		t.Fatalf("validator stake = %d", got)
	}

	var joinOut bytes.Buffer
	err = validatorJoinCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(validatorKey),
	}, &joinOut)
	if err == nil || !strings.Contains(err.Error(), "certified epoch transition") || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("validator join error = %v", err)
	}
	if joinOut.Len() != 0 {
		t.Fatalf("disabled validator join output = %q", joinOut.String())
	}
	pool := n.TxPool()
	if pool.PendingCount != 0 || pool.QueuedCount != 0 {
		t.Fatalf("disabled validator join entered tx pool: %+v", pool)
	}
	if validators := n.Validators(); len(validators) != 1 || validators[0] != proposer {
		t.Fatalf("validators after disabled join = %#v", validators)
	}
}

func TestGovernanceTransactionCommandsAreDisabled(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	privateKey := crypto.PrivateKeyToHex(key)
	tests := []struct {
		name string
		run  func(*bytes.Buffer) error
	}{
		{name: "submit", run: func(out *bytes.Buffer) error {
			return proposalSubmitCommand([]string{"--rpc", server.URL, "--private-key", privateKey, "--title", "x", "--kind", "param.change", "--param", "x", "--value", "y", "--voting-period", "2"}, out)
		}},
		{name: "vote", run: func(out *bytes.Buffer) error {
			return voteCommand([]string{"--rpc", server.URL, "--private-key", privateKey, "--proposal", "proposal:disabled", "--choice", "yes"}, out)
		}},
		{name: "execute", run: func(out *bytes.Buffer) error {
			return proposalExecuteCommand([]string{"--rpc", server.URL, "--private-key", privateKey, "--proposal", "proposal:disabled"}, out)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := test.run(&out); err == nil || !strings.Contains(err.Error(), "voting-power snapshots") {
				t.Fatalf("command error = %v", err)
			}
			if out.Len() != 0 {
				t.Fatalf("disabled command output = %q", out.String())
			}
		})
	}
	if pool := n.TxPool(); pool.PendingCount != 0 || pool.QueuedCount != 0 {
		t.Fatalf("disabled governance entered txpool: %+v", pool)
	}
}

func TestValidatorLeaveCommandIsDisabledWithoutSubmittingTx(t *testing.T) {
	proposerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := crypto.AddressFromPrivateKey(proposerKey)
	validator := crypto.AddressFromPrivateKey(validatorKey)
	n, err := node.NewDevelopment(node.Config{
		ChainID:     "chainlab-local",
		ProposerKey: proposerKey,
		GenesisBalance: map[string]uint64{
			proposer:  1_000_000,
			validator: 1_000_000,
		},
		Validators: []string{proposer, validator}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var leaveOut bytes.Buffer
	err = validatorLeaveCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(validatorKey),
	}, &leaveOut)
	if err == nil || !strings.Contains(err.Error(), "certified epoch transition") || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("validator leave error = %v", err)
	}
	if leaveOut.Len() != 0 {
		t.Fatalf("disabled validator leave output = %q", leaveOut.String())
	}
	pool := n.TxPool()
	if pool.PendingCount != 0 || pool.QueuedCount != 0 {
		t.Fatalf("disabled validator leave entered tx pool: %+v", pool)
	}
	validators := n.Validators()
	if len(validators) != 2 || validators[0] != proposer || validators[1] != validator {
		t.Fatalf("validators = %#v", validators)
	}
}

func TestValidatorSlashCommandSendsSignedTx(t *testing.T) {
	proposerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := crypto.AddressFromPrivateKey(proposerKey)
	validator := crypto.AddressFromPrivateKey(validatorKey)
	n, err := node.NewDevelopment(node.Config{
		ChainID:     "chainlab-local",
		ProposerKey: proposerKey,
		GenesisBalance: map[string]uint64{
			proposer:  1_000_000,
			validator: 1_000_000,
		},
		Validators: []string{proposer, validator}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	stake := signedCLITx(t, validatorKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxStake,
		From:     validator,
		Nonce:    0,
		Value:    300,
		GasLimit: 30_000,
		GasPrice: 1,
	})
	if err := n.SubmitTx(stake); err != nil {
		t.Fatal(err)
	}
	const evidenceHeight = uint64(3)
	firstBlockHash := "0x" + strings.Repeat("77", 32)
	secondBlockHash := "0x" + strings.Repeat("88", 32)
	firstSignature, err := crypto.Sign(validatorKey, types.FinalityVoteSigningBytes("chainlab-local", evidenceHeight, firstBlockHash))
	if err != nil {
		t.Fatal(err)
	}
	secondSignature, err := crypto.Sign(validatorKey, types.FinalityVoteSigningBytes("chainlab-local", evidenceHeight, secondBlockHash))
	if err != nil {
		t.Fatal(err)
	}

	var slashOut bytes.Buffer
	if err := validatorSlashCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(proposerKey),
		"--target", validator,
		"--height", strconv.FormatUint(evidenceHeight, 10),
		"--first-block-hash", firstBlockHash,
		"--first-signature", firstSignature,
		"--second-block-hash", secondBlockHash,
		"--second-signature", secondSignature,
	}, &slashOut); err != nil {
		t.Fatal(err)
	}
	slashBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(slashBlock.Transactions) != 2 || slashBlock.Transactions[1].Type != types.TxValidatorSlash {
		t.Fatalf("slash block transactions = %#v", slashBlock.Transactions)
	}
	slashTx := slashBlock.Transactions[1]
	if slashTx.Payload["target"] != validator ||
		slashTx.Payload["height"] != strconv.FormatUint(evidenceHeight, 10) ||
		slashTx.Payload["first_block_hash"] != firstBlockHash ||
		slashTx.Payload["first_signature"] != firstSignature ||
		slashTx.Payload["second_block_hash"] != secondBlockHash ||
		slashTx.Payload["second_signature"] != secondSignature {
		t.Fatalf("slash transaction payload = %#v", slashTx.Payload)
	}
	if len(slashBlock.Receipts) != 2 || len(slashBlock.Receipts[1].Events) != 1 || slashBlock.Receipts[1].Events[0].Type != "validator.slashed" {
		t.Fatalf("slash receipt = %#v", slashBlock.Receipts)
	}
	if got := n.StakeOf(validator); got != 0 {
		t.Fatalf("validator stake = %d", got)
	}
	validators := n.Validators()
	if len(validators) != 2 || validators[0] != proposer || validators[1] != validator {
		t.Fatalf("validators = %#v", validators)
	}
}

func TestDeployAndContractCallCommandsSendSignedTx(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var deployOut bytes.Buffer
	if err := deployCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--code-id", "counter.v1",
		"--arg", "initial=2",
	}, &deployOut); err != nil {
		t.Fatal(err)
	}
	var deployResponse struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(deployOut.Bytes(), &deployResponse); err != nil {
		t.Fatal(err)
	}
	if deployResponse.Hash == "" {
		t.Fatal("deploy command should print transaction hash")
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(deployBlock.Transactions) != 1 || deployBlock.Transactions[0].Type != types.TxDeploy {
		t.Fatalf("deploy block transactions = %#v", deployBlock.Transactions)
	}
	counter := deployBlock.Receipts[0].ContractAddress
	if counter == "" {
		t.Fatal("deploy receipt should include contract address")
	}

	var callOut bytes.Buffer
	if err := contractCallCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--to", counter,
		"--method", "increment",
		"--arg", "amount=5",
	}, &callOut); err != nil {
		t.Fatal(err)
	}
	var callResponse struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(callOut.Bytes(), &callResponse); err != nil {
		t.Fatal(err)
	}
	if callResponse.Hash == "" {
		t.Fatal("call command should print transaction hash")
	}
	callBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(callBlock.Transactions) != 1 || callBlock.Transactions[0].Type != types.TxCall {
		t.Fatalf("call block transactions = %#v", callBlock.Transactions)
	}

	var readOut bytes.Buffer
	if err := callCommand([]string{
		"--rpc", server.URL,
		"--from", alice,
		"--to", counter,
		"--method", "get",
	}, &readOut); err != nil {
		t.Fatal(err)
	}
	var readResult struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(readOut.Bytes(), &readResult); err != nil {
		t.Fatal(err)
	}
	if readResult.Result != "7" {
		t.Fatalf("counter value = %q", readResult.Result)
	}
}

func TestWASMUploadCommandDeploysUploadedCode(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	wasmPath := filepath.Join(t.TempDir(), "echo.wasm")
	if err := os.WriteFile(wasmPath, contracts.WasmEchoCode(), 0o600); err != nil {
		t.Fatal(err)
	}

	var uploadOut bytes.Buffer
	if err := wasmUploadCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--wasm-file", wasmPath,
	}, &uploadOut); err != nil {
		t.Fatal(err)
	}
	var uploadResponse struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(uploadOut.Bytes(), &uploadResponse); err != nil {
		t.Fatal(err)
	}
	if uploadResponse.Hash == "" {
		t.Fatal("wasm upload command should print transaction hash")
	}
	uploadBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	codeID := uploadBlock.Receipts[0].CodeID
	if codeID == "" {
		t.Fatalf("upload receipt = %+v", uploadBlock.Receipts[0])
	}

	var deployOut bytes.Buffer
	if err := deployCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--code-id", codeID,
		"--arg", "message=hello",
	}, &deployOut); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	contract := deployBlock.Receipts[0].ContractAddress

	var callOut bytes.Buffer
	if err := contractCallCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--to", contract,
		"--method", "set",
		"--arg", "message=world",
	}, &callOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	var readOut bytes.Buffer
	if err := callCommand([]string{"--rpc", server.URL, "--from", alice, "--to", contract, "--method", "get"}, &readOut); err != nil {
		t.Fatal(err)
	}
	var readResult struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(readOut.Bytes(), &readResult); err != nil {
		t.Fatal(err)
	}
	if readResult.Result != "world" {
		t.Fatalf("uploaded wasm result = %q", readResult.Result)
	}
}

func TestReadWASMUploadBytecodeLoadsExample(t *testing.T) {
	bytecode, err := readWASMUploadBytecode("", "", "echo")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytecode, contracts.WasmEchoCode()) {
		t.Fatal("echo example bytecode should match built-in wasm echo module")
	}
}

func TestWASMUploadCommandUsesMeteredDefaultGasLimit(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var out bytes.Buffer
	if err := wasmUploadCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--example", "echo",
	}, &out); err != nil {
		t.Fatal(err)
	}
	pool := n.TxPool()
	if pool.PendingCount != 1 {
		t.Fatalf("txpool = %+v", pool)
	}
	wantGas := core.EstimateWASMUploadGas(contracts.WasmEchoCode())
	if pool.Pending[0].GasLimit != wantGas {
		t.Fatalf("upload gas limit = %d, want %d", pool.Pending[0].GasLimit, wantGas)
	}
}

func TestQueryAccountAndProduceCommands(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var accountOut bytes.Buffer
	if err := accountCommand([]string{"--rpc", server.URL, "--address", alice}, &accountOut); err != nil {
		t.Fatal(err)
	}
	var account types.Account
	if err := json.Unmarshal(accountOut.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	if account.Balance != 1_000_000 {
		t.Fatalf("balance = %d", account.Balance)
	}

	var blockOut bytes.Buffer
	if err := produceCommand([]string{"--rpc", server.URL}, &blockOut); err != nil {
		t.Fatal(err)
	}
	var block types.Block
	if err := json.Unmarshal(blockOut.Bytes(), &block); err != nil {
		t.Fatal(err)
	}
	if block.Header.Height != 1 {
		t.Fatalf("produced height = %d", block.Header.Height)
	}

	var validatorsOut bytes.Buffer
	if err := validatorsCommand([]string{"--rpc", server.URL}, &validatorsOut); err != nil {
		t.Fatal(err)
	}
	var validators []string
	if err := json.Unmarshal(validatorsOut.Bytes(), &validators); err != nil {
		t.Fatal(err)
	}
	if len(validators) != 1 || validators[0] != alice {
		t.Fatalf("validators = %#v", validators)
	}
}

func TestQueryFinalityCommand(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := n.ProduceBlock(); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var out bytes.Buffer
	if err := finalityCommand([]string{"--rpc", server.URL}, &out); err != nil {
		t.Fatal(err)
	}
	var finality struct {
		HeadHeight      uint64 `json:"head_height"`
		SafeHeight      uint64 `json:"safe_height"`
		FinalizedHeight uint64 `json:"finalized_height"`
	}
	if err := json.Unmarshal(out.Bytes(), &finality); err != nil {
		t.Fatal(err)
	}
	if finality.HeadHeight != 3 || finality.SafeHeight != 2 || finality.FinalizedHeight != 0 {
		t.Fatalf("finality = %+v", finality)
	}
}

func TestQueryFinalityEvidenceCommand(t *testing.T) {
	keyA, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyC, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := crypto.AddressFromPrivateKey(keyA)
	validatorB := crypto.AddressFromPrivateKey(keyB)
	validatorC := crypto.AddressFromPrivateKey(keyC)
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: map[string]uint64{validatorA: 1_000_000},
		Validators:     []string{validatorA, validatorB, validatorC}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := seedCommandFinalityEquivocationEvidence(t, n, keyA, keyB, validatorA, validatorB)
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var out bytes.Buffer
	if err := finalityEvidenceCommand([]string{"--rpc", server.URL}, &out); err != nil {
		t.Fatal(err)
	}
	var evidence []types.FinalityEquivocationEvidence
	if err := json.Unmarshal(out.Bytes(), &evidence); err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].SecondBlockHash != expected.SecondBlockHash {
		t.Fatalf("command evidence = %+v, want %+v", evidence, expected)
	}
}

func TestChainFinalityVoteCommandSignsAndSubmitsVote(t *testing.T) {
	keyA, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyC, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := crypto.AddressFromPrivateKey(keyA)
	validatorB := crypto.AddressFromPrivateKey(keyB)
	validatorC := crypto.AddressFromPrivateKey(keyC)
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: map[string]uint64{validatorA: 1_000_000},
		Validators:     []string{validatorA, validatorB, validatorC}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var finality node.FinalityCheckpoint
	for _, key := range []crypto.PrivateKey{keyA, keyB, keyC} {
		var out bytes.Buffer
		if err := finalityVoteCommand([]string{
			"--rpc", server.URL,
			"--private-key", crypto.PrivateKeyToHex(key),
			"--height", "1",
		}, &out); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Vote     types.FinalitySignature `json:"vote"`
			Finality node.FinalityCheckpoint `json:"finality"`
		}
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !consensus.VerifyFinalityVote(block, result.Vote) {
			t.Fatalf("command vote does not verify: %+v", result.Vote)
		}
		finality = result.Finality
	}
	if finality.CertifiedHeight != block.Header.Height || finality.FinalizedSource != "bft_certificate" {
		t.Fatalf("finality vote result = %+v", finality)
	}
}

func TestQueryFeesCommand(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var out bytes.Buffer
	if err := feesCommand([]string{"--rpc", server.URL}, &out); err != nil {
		t.Fatal(err)
	}
	var fees struct {
		BaseFeePerGas        string `json:"base_fee_per_gas"`
		MaxPriorityFeePerGas string `json:"max_priority_fee_per_gas"`
		GasPrice             string `json:"gas_price"`
	}
	if err := json.Unmarshal(out.Bytes(), &fees); err != nil {
		t.Fatal(err)
	}
	if fees.BaseFeePerGas != "0x1" || fees.MaxPriorityFeePerGas != "0x1" || fees.GasPrice != "0x2" {
		t.Fatalf("fees = %+v", fees)
	}
}

func TestQueryLogsCommand(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	deploy := signedCLITx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    0,
		GasLimit: 100_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": "counter.v1",
			"initial": "0",
		},
	})
	if err := n.SubmitTx(deploy); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	counter := deployBlock.Receipts[0].ContractAddress

	call := signedCLITx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       counter,
		Nonce:    1,
		GasLimit: 60_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method": "increment",
			"amount": "4",
		},
	})
	if err := n.SubmitTx(call); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	topic := hash.KeccakHex([]byte("counter.incremented"))
	if err := logsCommand([]string{
		"--rpc", server.URL,
		"--from-block", "0x1",
		"--to-block", "latest",
		"--address", counter,
		"--topic", topic,
	}, &out); err != nil {
		t.Fatal(err)
	}
	var logs []map[string]any
	if err := json.Unmarshal(out.Bytes(), &logs); err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("log count = %d", len(logs))
	}
	if logs[0]["address"] != counter || logs[0]["transactionHash"] != call.Hash() {
		t.Fatalf("log = %#v", logs[0])
	}
}

func TestQueryCallAndEstimateGasCommands(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	deploy := signedCLITx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    0,
		GasLimit: 100_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": "counter.v1",
			"initial": "7",
		},
	})
	if err := n.SubmitTx(deploy); err != nil {
		t.Fatal(err)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	counter := block.Receipts[0].ContractAddress

	var callOut bytes.Buffer
	if err := callCommand([]string{
		"--rpc", server.URL,
		"--from", alice,
		"--to", counter,
		"--method", "get",
	}, &callOut); err != nil {
		t.Fatal(err)
	}
	var callResult struct {
		Raw    string `json:"raw"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal(callOut.Bytes(), &callResult); err != nil {
		t.Fatal(err)
	}
	wantRaw := "0x" + strings.Repeat("0", 63) + "7"
	if callResult.Result != "7" || callResult.Raw != wantRaw {
		t.Fatalf("call result = %+v", callResult)
	}

	var gasOut bytes.Buffer
	if err := estimateGasCommand([]string{
		"--rpc", server.URL,
		"--type", "call",
		"--to", counter,
	}, &gasOut); err != nil {
		t.Fatal(err)
	}
	var gasResult struct {
		Gas string `json:"gas"`
	}
	if err := json.Unmarshal(gasOut.Bytes(), &gasResult); err != nil {
		t.Fatal(err)
	}
	if gasResult.Gas != "0xc350" {
		t.Fatalf("gas result = %+v", gasResult)
	}
}

func TestRPCJSONCall(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["method"] != "eth_blockNumber" {
			t.Fatalf("method = %v", request["method"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": request["id"], "result": "0x2"})
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	var result string
	if err := rpcCall(server.URL, "eth_blockNumber", []any{}, &result); err != nil {
		t.Fatal(err)
	}
	if result != "0x2" {
		t.Fatalf("result = %q", result)
	}
}

func signedCLITx(t *testing.T, key crypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	signature, err := crypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = signature
	return tx
}

func seedCommandFinalityEquivocationEvidence(t *testing.T, n *node.Node, keyA crypto.PrivateKey, keyB crypto.PrivateKey, validatorA string, validatorB string) types.FinalityEquivocationEvidence {
	t.Helper()
	blockA1, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	voteA1, err := consensus.SignFinalityVote(keyA, blockA1)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitFinalityVote(voteA1); err != nil {
		t.Fatal(err)
	}
	genesis, ok := n.Block(0)
	if !ok {
		t.Fatal("genesis should exist")
	}
	blockB1 := signedEmptyCLIBlock(t, keyA, genesis, validatorA, 1, blockA1.Header.TimeUnix+10)
	blockB2 := signedEmptyCLIBlock(t, keyB, blockB1, validatorB, 2, blockA1.Header.TimeUnix+20)
	if err := n.ImportBlock(blockB1); err != nil {
		t.Fatal(err)
	}
	if err := n.ImportBlock(blockB2); err != nil {
		t.Fatal(err)
	}
	voteB1, err := consensus.SignFinalityVote(keyA, blockB1)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitFinalityVote(voteB1); err == nil {
		t.Fatal("conflicting finality vote should be rejected")
	}
	evidence := n.FinalityEvidence()
	if len(evidence) != 1 {
		t.Fatalf("evidence = %+v", evidence)
	}
	return evidence[0]
}

func signedEmptyCLIBlock(t *testing.T, key crypto.PrivateKey, parent types.Block, proposer string, height uint64, timeUnix int64) types.Block {
	t.Helper()
	block := types.Block{
		Header: types.BlockHeader{
			ChainID:       parent.Header.ChainID,
			Height:        height,
			ParentHash:    parent.Hash(),
			TimeUnix:      timeUnix,
			Proposer:      proposer,
			GasLimit:      node.DefaultBlockGasLimit,
			GasUsed:       0,
			BaseFeePerGas: node.NextBaseFee(parent, node.DefaultBlockGasLimit),
			TxRoot:        types.TransactionRoot(nil),
			ReceiptRoot:   types.ReceiptRoot(nil),
			StateRoot:     parent.Header.StateRoot,
		},
	}
	if err := consensus.SignBlock(key, &block); err != nil {
		t.Fatal(err)
	}
	return block
}
