package types

import (
	"chainlab/internal/hash"
)

type TxType string

const (
	TxTransfer TxType = "transfer"
	TxDeploy   TxType = "deploy"
	TxCall     TxType = "call"
	TxStake    TxType = "stake"
	TxUnstake  TxType = "unstake"
	TxVote     TxType = "vote"
)

type Account struct {
	Address string            `json:"address"`
	Balance uint64            `json:"balance"`
	Nonce   uint64            `json:"nonce"`
	CodeID  string            `json:"code_id,omitempty"`
	Storage map[string]string `json:"storage,omitempty"`
}

type Transaction struct {
	ChainID   string            `json:"chain_id"`
	Type      TxType            `json:"type"`
	From      string            `json:"from"`
	To        string            `json:"to,omitempty"`
	Nonce     uint64            `json:"nonce"`
	Value     uint64            `json:"value,omitempty"`
	GasLimit  uint64            `json:"gas_limit"`
	GasPrice  uint64            `json:"gas_price"`
	Payload   map[string]string `json:"payload,omitempty"`
	Signature string            `json:"signature,omitempty"`
}

func (tx Transaction) SigningBytes() []byte {
	tx.Signature = ""
	return hash.MustCanonicalBytes(tx)
}

func (tx Transaction) Hash() string {
	return hash.MustHex(tx)
}

type Event struct {
	Type       string            `json:"type"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type Receipt struct {
	TxHash          string  `json:"tx_hash"`
	Success         bool    `json:"success"`
	Error           string  `json:"error,omitempty"`
	GasUsed         uint64  `json:"gas_used"`
	Events          []Event `json:"events,omitempty"`
	ContractAddress string  `json:"contract_address,omitempty"`
}

type TransactionRecord struct {
	Transaction Transaction `json:"transaction"`
	Receipt     Receipt     `json:"receipt"`
	BlockHeight uint64      `json:"block_height"`
	BlockHash   string      `json:"block_hash"`
	Index       int         `json:"index"`
}

type Proposal struct {
	ID     string            `json:"id"`
	Votes  map[string]uint64 `json:"votes"`
	Voters map[string]string `json:"voters"`
}

type BlockHeader struct {
	ChainID     string `json:"chain_id"`
	Height      uint64 `json:"height"`
	ParentHash  string `json:"parent_hash"`
	TimeUnix    int64  `json:"time_unix"`
	Proposer    string `json:"proposer"`
	TxRoot      string `json:"tx_root"`
	ReceiptRoot string `json:"receipt_root"`
	StateRoot   string `json:"state_root"`
}

func (h BlockHeader) SigningBytes() []byte {
	return hash.MustCanonicalBytes(h)
}

type Block struct {
	Header       BlockHeader   `json:"header"`
	Transactions []Transaction `json:"transactions,omitempty"`
	Receipts     []Receipt     `json:"receipts,omitempty"`
	Signature    string        `json:"signature,omitempty"`
}

func (b Block) Hash() string {
	return hash.MustHex(b.Header)
}

func GenesisBlock(chainID string, stateRoot string) Block {
	return Block{
		Header: BlockHeader{
			ChainID:     chainID,
			Height:      0,
			ParentHash:  "",
			TimeUnix:    0,
			Proposer:    "genesis",
			TxRoot:      TransactionRoot(nil),
			ReceiptRoot: ReceiptRoot(nil),
			StateRoot:   stateRoot,
		},
	}
}

func TransactionRoot(txs []Transaction) string {
	hashes := make([]string, len(txs))
	for i, tx := range txs {
		hashes[i] = tx.Hash()
	}
	return hash.MustHex(hashes)
}

func ReceiptRoot(receipts []Receipt) string {
	hashes := make([]string, len(receipts))
	for i, receipt := range receipts {
		hashes[i] = hash.MustHex(receipt)
	}
	return hash.MustHex(hashes)
}
