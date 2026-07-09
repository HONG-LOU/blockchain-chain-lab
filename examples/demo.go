package examples

import (
	"encoding/hex"

	"chainlab/internal/contracts"
	"chainlab/internal/core"
	"chainlab/internal/crypto"
	"chainlab/internal/node"
	"chainlab/internal/types"
)

type Summary struct {
	Height                      uint64
	TransferReceiverBalance     uint64
	SponsoredReceiverBalance    uint64
	SponsoredUserBalance        uint64
	BatchReceiverBalance        uint64
	BatchCounterValue           string
	SmartAccountReceiverBalance uint64
	SmartAccountBalance         uint64
	SmartAccountNonce           uint64
	CounterValue                string
	TokenReceiverBalance        string
	WASMValue                   string
	Stake                       uint64
	YesVotes                    uint64
	GovernanceParam             string
}

func RunDemo() (Summary, error) {
	key, err := crypto.GenerateKey()
	if err != nil {
		return Summary{}, err
	}
	alice := crypto.AddressFromPrivateKey(key)
	sponsoredKey, err := crypto.GenerateKey()
	if err != nil {
		return Summary{}, err
	}
	sponsoredUser := crypto.AddressFromPrivateKey(sponsoredKey)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sponsoredReceiver := "0xdddddddddddddddddddddddddddddddddddddddd"
	batchReceiver := "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	smartAccountReceiver := "0xffffffffffffffffffffffffffffffffffffffff"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 2_000_000, sponsoredUser: 15},
	})
	if err != nil {
		return Summary{}, err
	}

	if _, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	}); err != nil {
		return Summary{}, err
	}

	counterBlock, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    1,
		GasLimit: 80_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": "counter.v1",
			"initial": "0",
		},
	})
	if err != nil {
		return Summary{}, err
	}
	counter := counterBlock.Receipts[0].ContractAddress

	if _, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       counter,
		Nonce:    2,
		GasLimit: 50_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method": "increment",
			"amount": "3",
		},
	}); err != nil {
		return Summary{}, err
	}

	tokenBlock, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    3,
		GasLimit: 80_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": "token.v1",
			"symbol":  "LAB",
		},
	})
	if err != nil {
		return Summary{}, err
	}
	token := tokenBlock.Receipts[0].ContractAddress

	if err := signAndSubmit(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       token,
		Nonce:    4,
		GasLimit: 50_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method": "mint",
			"to":     alice,
			"amount": "100",
		},
	}); err != nil {
		return Summary{}, err
	}
	if err := signAndSubmit(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       token,
		Nonce:    5,
		GasLimit: 50_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method": "transfer",
			"to":     bob,
			"amount": "25",
		},
	}); err != nil {
		return Summary{}, err
	}
	if _, err := n.ProduceBlock(); err != nil {
		return Summary{}, err
	}

	wasmBytecode := contracts.WasmEchoCode()
	wasmUploadBlock, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxWASMUpload,
		From:     alice,
		Nonce:    6,
		GasLimit: core.EstimateWASMUploadGas(wasmBytecode),
		GasPrice: 1,
		Payload: map[string]string{
			"bytecode": "0x" + hex.EncodeToString(wasmBytecode),
		},
	})
	if err != nil {
		return Summary{}, err
	}
	wasmCodeID := wasmUploadBlock.Receipts[0].CodeID

	wasmBlock, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    7,
		GasLimit: 90_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": wasmCodeID,
			"message": "hello",
		},
	})
	if err != nil {
		return Summary{}, err
	}
	wasmContract := wasmBlock.Receipts[0].ContractAddress

	if _, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       wasmContract,
		Nonce:    8,
		GasLimit: 60_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method":  "set",
			"message": "world",
		},
	}); err != nil {
		return Summary{}, err
	}
	wasmValue, err := n.ReadContract(alice, wasmContract, "get", nil)
	if err != nil {
		return Summary{}, err
	}

	if _, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxStake,
		From:     alice,
		Nonce:    9,
		Value:    200,
		GasLimit: 30_000,
		GasPrice: 1,
	}); err != nil {
		return Summary{}, err
	}

	proposalBlock, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxProposalSubmit,
		From:     alice,
		Nonce:    10,
		GasLimit: 35_000,
		GasPrice: 1,
		Payload: map[string]string{
			"title":         "Set local quorum",
			"description":   "Use majority quorum for the local chain",
			"kind":          "param.change",
			"param":         "governance.quorum",
			"value":         "majority",
			"voting_period": "2",
		},
	})
	if err != nil {
		return Summary{}, err
	}
	proposalID := proposalBlock.Receipts[0].ProposalID

	if _, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxVote,
		From:     alice,
		Nonce:    11,
		GasLimit: 25_000,
		GasPrice: 1,
		Payload: map[string]string{
			"proposal": proposalID,
			"choice":   "yes",
		},
	}); err != nil {
		return Summary{}, err
	}

	if _, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxProposalExecute,
		From:     alice,
		Nonce:    12,
		GasLimit: 35_000,
		GasPrice: 1,
		Payload: map[string]string{
			"proposal": proposalID,
		},
	}); err != nil {
		return Summary{}, err
	}

	sponsoredTransfer := types.Transaction{
		ChainID:   "chainlab-local",
		Type:      types.TxTransfer,
		From:      sponsoredUser,
		To:        sponsoredReceiver,
		Nonce:     0,
		Value:     15,
		GasLimit:  21_000,
		GasPrice:  2,
		Paymaster: alice,
	}
	if err := signSponsoredAndSubmit(n, sponsoredKey, key, sponsoredTransfer); err != nil {
		return Summary{}, err
	}
	if _, err := n.ProduceBlock(); err != nil {
		return Summary{}, err
	}

	batchCounterBlock, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    13,
		GasLimit: 80_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": "counter.v1",
			"initial": "0",
		},
	})
	if err != nil {
		return Summary{}, err
	}
	batchCounter := batchCounterBlock.Receipts[0].ContractAddress

	if _, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxBatch,
		From:     alice,
		Nonce:    14,
		GasLimit: 113_000,
		GasPrice: 1,
		Batch: []types.BatchOperation{
			{
				Type: types.TxCall,
				To:   batchCounter,
				Payload: map[string]string{
					"method": "increment",
					"amount": "2",
				},
			},
			{Type: types.TxTransfer, To: batchReceiver, Value: 7},
		},
	}); err != nil {
		return Summary{}, err
	}

	smartAccountBlock, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    15,
		GasLimit: 80_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": contracts.AccountCodeID,
			"owner":   alice,
		},
	})
	if err != nil {
		return Summary{}, err
	}
	smartAccount := smartAccountBlock.Receipts[0].ContractAddress

	if _, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       smartAccount,
		Nonce:    16,
		Value:    50_000,
		GasLimit: 21_000,
		GasPrice: 1,
	}); err != nil {
		return Summary{}, err
	}

	if _, err := submitAndProduce(n, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     smartAccount,
		Signer:   alice,
		To:       smartAccountReceiver,
		Nonce:    0,
		Value:    42,
		GasLimit: 21_000,
		GasPrice: 2,
	}); err != nil {
		return Summary{}, err
	}

	return Summary{
		Height:                      n.Head().Header.Height,
		TransferReceiverBalance:     n.Account(bob).Balance,
		SponsoredReceiverBalance:    n.Account(sponsoredReceiver).Balance,
		SponsoredUserBalance:        n.Account(sponsoredUser).Balance,
		BatchReceiverBalance:        n.Account(batchReceiver).Balance,
		BatchCounterValue:           n.Account(batchCounter).Storage["count"],
		SmartAccountReceiverBalance: n.Account(smartAccountReceiver).Balance,
		SmartAccountBalance:         n.Account(smartAccount).Balance,
		SmartAccountNonce:           n.Account(smartAccount).Nonce,
		CounterValue:                n.Account(counter).Storage["count"],
		TokenReceiverBalance:        n.Account(token).Storage["balance:"+bob],
		WASMValue:                   wasmValue,
		Stake:                       n.StakeOf(alice),
		YesVotes:                    n.Proposal(proposalID).Votes["yes"],
		GovernanceParam:             n.Param("governance.quorum"),
	}, nil
}

func submitAndProduce(n *node.Node, key crypto.PrivateKey, tx types.Transaction) (types.Block, error) {
	if err := signAndSubmit(n, key, tx); err != nil {
		return types.Block{}, err
	}
	return n.ProduceBlock()
}

func signAndSubmit(n *node.Node, key crypto.PrivateKey, tx types.Transaction) error {
	signature, err := crypto.Sign(key, tx.SigningBytes())
	if err != nil {
		return err
	}
	tx.Signature = signature
	return n.SubmitTx(tx)
}

func signSponsoredAndSubmit(n *node.Node, userKey crypto.PrivateKey, paymasterKey crypto.PrivateKey, tx types.Transaction) error {
	signature, err := crypto.Sign(userKey, tx.SigningBytes())
	if err != nil {
		return err
	}
	tx.Signature = signature
	paymasterSignature, err := crypto.Sign(paymasterKey, tx.PaymasterSigningBytes())
	if err != nil {
		return err
	}
	tx.PaymasterSignature = paymasterSignature
	return n.SubmitTx(tx)
}
