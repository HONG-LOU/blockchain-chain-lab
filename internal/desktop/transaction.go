package desktop

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	chainabci "chainlab/internal/abci"
	chaincrypto "chainlab/internal/crypto"
	chaintypes "chainlab/internal/types"

	cmtjson "github.com/cometbft/cometbft/libs/json"
	"github.com/cometbft/cometbft/privval"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
)

type TransferResult struct {
	TransactionHash string             `json:"transaction_hash"`
	CometHash       string             `json:"comet_hash"`
	Height          int64              `json:"height"`
	Receipt         chaintypes.Receipt `json:"receipt"`
}

func Account(ctx context.Context, root string, address string) (chaintypes.Account, error) {
	config, err := Load(root)
	if err != nil {
		return chaintypes.Account{}, err
	}
	normalized, err := chaincrypto.NormalizeAddress(address)
	if err != nil {
		return chaintypes.Account{}, err
	}
	client, err := rpchttp.New(tcpHTTPURL(config.RPCAddress), "/websocket")
	if err != nil {
		return chaintypes.Account{}, fmt.Errorf("create desktop RPC client: %w", err)
	}
	result, err := client.ABCIQuery(ctx, "/account", []byte(normalized))
	if err != nil {
		return chaintypes.Account{}, fmt.Errorf("query desktop account: %w", err)
	}
	if result.Response.Code != chainabci.CodeOK {
		return chaintypes.Account{}, fmt.Errorf("query desktop account failed: %s", result.Response.Log)
	}
	var account chaintypes.Account
	if err := json.Unmarshal(result.Response.Value, &account); err != nil {
		return chaintypes.Account{}, fmt.Errorf("decode desktop account: %w", err)
	}
	if account.Address != normalized {
		return chaintypes.Account{}, errors.New("desktop account response address does not match request")
	}
	return account, nil
}

func Transfer(ctx context.Context, root string, to string, value uint64) (TransferResult, error) {
	if value == 0 {
		return TransferResult{}, errors.New("desktop transfer value must be positive")
	}
	config, err := Load(root)
	if err != nil {
		return TransferResult{}, err
	}
	if config.Profile == ProfileObserver {
		return TransferResult{}, errors.New("observer profile has no local validator signing key")
	}
	recipient, err := chaincrypto.NormalizeAddress(to)
	if err != nil {
		return TransferResult{}, err
	}
	key, err := loadSoloKey(root, config)
	if err != nil {
		return TransferResult{}, err
	}
	from := chaincrypto.AddressFromPrivateKey(key)
	account, err := Account(ctx, root, from)
	if err != nil {
		return TransferResult{}, err
	}
	transaction := chaintypes.Transaction{
		ChainID: config.ChainID, Type: chaintypes.TxTransfer, From: from, To: recipient,
		Nonce: account.Nonce, Value: value, GasLimit: 21_000, GasPrice: 1,
	}
	transaction.Signature, err = chaincrypto.Sign(key, transaction.SigningBytes())
	if err != nil {
		return TransferResult{}, err
	}
	encoded, err := chaintypes.EncodeRawTransaction(transaction)
	if err != nil {
		return TransferResult{}, err
	}
	cometHash, err := SubmitRaw(ctx, root, encoded)
	if err != nil {
		return TransferResult{}, err
	}
	hashBytes, _ := hex.DecodeString(strings.TrimPrefix(cometHash, "0x"))
	client, err := rpchttp.New(tcpHTTPURL(config.RPCAddress), "/websocket")
	if err != nil {
		return TransferResult{}, fmt.Errorf("create desktop RPC client: %w", err)
	}
	return waitForReceipt(ctx, client, hashBytes)
}

func SubmitRaw(ctx context.Context, root string, encoded string) (string, error) {
	config, err := Load(root)
	if err != nil {
		return "", err
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(encoded, "0x"))
	if err != nil || len(raw) == 0 || len(raw) > chaintypes.MaxTransactionBytes {
		return "", errors.New("raw transaction must be non-empty bounded hex")
	}
	client, err := rpchttp.New(tcpHTTPURL(config.RPCAddress), "/websocket")
	if err != nil {
		return "", fmt.Errorf("create desktop RPC client: %w", err)
	}
	broadcast, err := client.BroadcastTxSync(ctx, cmttypes.Tx(raw))
	if err != nil {
		return "", fmt.Errorf("broadcast desktop transaction: %w", err)
	}
	if broadcast.Code != chainabci.CodeOK {
		return "", fmt.Errorf("desktop transaction rejected: %s", broadcast.Log)
	}
	return "0x" + hex.EncodeToString(broadcast.Hash), nil
}

func BroadcastRaw(ctx context.Context, root string, encoded string) (TransferResult, error) {
	cometHash, err := SubmitRaw(ctx, root, encoded)
	if err != nil {
		return TransferResult{}, err
	}
	config, err := Load(root)
	if err != nil {
		return TransferResult{}, err
	}
	hashBytes, _ := hex.DecodeString(strings.TrimPrefix(cometHash, "0x"))
	client, err := rpchttp.New(tcpHTTPURL(config.RPCAddress), "/websocket")
	if err != nil {
		return TransferResult{}, fmt.Errorf("create desktop RPC client: %w", err)
	}
	return waitForReceipt(ctx, client, hashBytes)
}

func waitForReceipt(ctx context.Context, client *rpchttp.HTTP, transactionHash []byte) (TransferResult, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, err := client.Tx(ctx, transactionHash, false)
		if err == nil {
			return transferResult(result, transactionHash)
		}
		select {
		case <-ctx.Done():
			return TransferResult{}, fmt.Errorf("wait for desktop transfer receipt: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func Receipt(ctx context.Context, root string, cometHash string) (TransferResult, error) {
	config, err := Load(root)
	if err != nil {
		return TransferResult{}, err
	}
	hashBytes, err := hex.DecodeString(strings.TrimPrefix(cometHash, "0x"))
	if err != nil || len(hashBytes) != 32 {
		return TransferResult{}, errors.New("Comet transaction hash must be 32-byte hex")
	}
	client, err := rpchttp.New(tcpHTTPURL(config.RPCAddress), "/websocket")
	if err != nil {
		return TransferResult{}, fmt.Errorf("create desktop RPC client: %w", err)
	}
	result, err := client.Tx(ctx, hashBytes, false)
	if err != nil {
		return TransferResult{}, fmt.Errorf("query desktop receipt: %w", err)
	}
	return transferResult(result, hashBytes)
}

func transferResult(result *coretypes.ResultTx, cometHash []byte) (TransferResult, error) {
	if result.TxResult.Code != chainabci.CodeOK {
		return TransferResult{}, fmt.Errorf("desktop transaction execution failed: %s", result.TxResult.Log)
	}
	var receipt chaintypes.Receipt
	if err := json.Unmarshal(result.TxResult.Data, &receipt); err != nil {
		return TransferResult{}, fmt.Errorf("decode desktop transaction receipt: %w", err)
	}
	return TransferResult{
		TransactionHash: receipt.TxHash,
		CometHash:       "0x" + hex.EncodeToString(cometHash),
		Height:          result.Height,
		Receipt:         receipt,
	}, nil
}

func loadSoloKey(root string, config Config) (chaincrypto.PrivateKey, error) {
	path := filepath.Join(root, filepath.FromSlash(config.NodeHome), "config", "priv_validator_key.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read desktop validator key: %w", err)
	}
	var key privval.FilePVKey
	if err := cmtjson.Unmarshal(raw, &key); err != nil {
		return nil, fmt.Errorf("decode desktop validator key: %w", err)
	}
	if key.PrivKey == nil {
		return nil, errors.New("desktop validator key has no private key")
	}
	return chaincrypto.PrivateKeyFromHex(hex.EncodeToString(key.PrivKey.Bytes()))
}
