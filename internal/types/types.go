package types

import (
	"chainlab/internal/hash"
)

const (
	DefaultBlockGasLimit uint64 = 30_000_000
	InitialBaseFeePerGas uint64 = 1
)

type TxType string

const (
	TxTransfer        TxType = "transfer"
	TxBatch           TxType = "batch"
	TxDeploy          TxType = "deploy"
	TxCall            TxType = "call"
	TxSetCode         TxType = "set_code"
	TxWASMUpload      TxType = "wasm.upload"
	TxStake           TxType = "stake"
	TxUnstake         TxType = "unstake"
	TxProposalSubmit  TxType = "proposal.submit"
	TxProposalExecute TxType = "proposal.execute"
	TxVote            TxType = "vote"
	TxValidatorJoin   TxType = "validator.join"
	TxValidatorLeave  TxType = "validator.leave"
	TxValidatorSlash  TxType = "validator.slash"
)

const SignatureKindEthereumType2 = "ethereum.type2"

type Account struct {
	Address         string            `json:"address"`
	Balance         uint64            `json:"balance"`
	Nonce           uint64            `json:"nonce"`
	CodeID          string            `json:"code_id,omitempty"`
	DelegatedCodeID string            `json:"delegated_code_id,omitempty"`
	Storage         map[string]string `json:"storage,omitempty"`
}

type Transaction struct {
	ChainID              string            `json:"chain_id"`
	Type                 TxType            `json:"type"`
	From                 string            `json:"from"`
	Signer               string            `json:"signer,omitempty"`
	To                   string            `json:"to,omitempty"`
	Nonce                uint64            `json:"nonce"`
	Value                uint64            `json:"value,omitempty"`
	GasLimit             uint64            `json:"gas_limit"`
	GasPrice             uint64            `json:"gas_price,omitempty"`
	MaxFeePerGas         uint64            `json:"max_fee_per_gas,omitempty"`
	MaxPriorityFeePerGas uint64            `json:"max_priority_fee_per_gas,omitempty"`
	Paymaster            string            `json:"paymaster,omitempty"`
	Payload              map[string]string `json:"payload,omitempty"`
	Batch                []BatchOperation  `json:"batch,omitempty"`
	Authorizations       []Authorization   `json:"authorizations,omitempty"`
	Signature            string            `json:"signature,omitempty"`
	PaymasterSignature   string            `json:"paymaster_signature,omitempty"`
	SignatureKind        string            `json:"signature_kind,omitempty"`
	EthereumRawHash      string            `json:"ethereum_raw_hash,omitempty"`
}

type Authorization struct {
	Signer    string `json:"signer"`
	Signature string `json:"signature,omitempty"`
}

type BatchOperation struct {
	Type    TxType            `json:"type"`
	To      string            `json:"to,omitempty"`
	Value   uint64            `json:"value,omitempty"`
	Payload map[string]string `json:"payload,omitempty"`
}

func (tx Transaction) SigningBytes() []byte {
	tx.Signature = ""
	tx.PaymasterSignature = ""
	if len(tx.Authorizations) > 0 {
		tx.Authorizations = append([]Authorization(nil), tx.Authorizations...)
		for i := range tx.Authorizations {
			tx.Authorizations[i].Signature = ""
		}
	}
	return hash.MustCanonicalBytes(tx)
}

func (tx Transaction) PaymasterSigningBytes() []byte {
	tx.PaymasterSignature = ""
	return hash.MustCanonicalBytes(tx)
}

func (tx Transaction) Hash() string {
	if tx.SignatureKind == SignatureKindEthereumType2 && tx.EthereumRawHash != "" {
		return tx.EthereumRawHash
	}
	return hash.MustHex(tx)
}

func WASMCodeID(bytecode []byte) string {
	return "wasm:" + hash.KeccakHex(bytecode)
}

func ProposalID(txHash string) string {
	return "proposal:" + txHash
}

type Event struct {
	Type       string            `json:"type"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type Receipt struct {
	TxHash            string  `json:"tx_hash"`
	Success           bool    `json:"success"`
	Error             string  `json:"error,omitempty"`
	GasUsed           uint64  `json:"gas_used"`
	BaseFeePerGas     uint64  `json:"base_fee_per_gas,omitempty"`
	EffectiveGasPrice uint64  `json:"effective_gas_price,omitempty"`
	FeePayer          string  `json:"fee_payer,omitempty"`
	BaseFeeBurned     uint64  `json:"base_fee_burned,omitempty"`
	PriorityFeePaid   uint64  `json:"priority_fee_paid,omitempty"`
	Events            []Event `json:"events,omitempty"`
	CodeID            string  `json:"code_id,omitempty"`
	ProposalID        string  `json:"proposal_id,omitempty"`
	ContractAddress   string  `json:"contract_address,omitempty"`
}

type ContractCode struct {
	CodeID   string `json:"code_id"`
	Runtime  string `json:"runtime"`
	Creator  string `json:"creator"`
	Bytecode string `json:"bytecode"`
}

type TransactionRecord struct {
	Transaction Transaction `json:"transaction"`
	Receipt     Receipt     `json:"receipt"`
	BlockHeight uint64      `json:"block_height"`
	BlockHash   string      `json:"block_hash"`
	Index       int         `json:"index"`
}

type EventRecord struct {
	Event            Event  `json:"event"`
	Address          string `json:"address,omitempty"`
	Topic0           string `json:"topic0"`
	BlockHeight      uint64 `json:"block_height"`
	BlockHash        string `json:"block_hash"`
	TransactionHash  string `json:"transaction_hash"`
	TransactionIndex int    `json:"transaction_index"`
	EventIndex       int    `json:"event_index"`
	LogIndex         uint64 `json:"log_index"`
}

type ProposalStatus string

const (
	ProposalStatusOpen     ProposalStatus = "open"
	ProposalStatusRejected ProposalStatus = "rejected"
	ProposalStatusExecuted ProposalStatus = "executed"
)

type Proposal struct {
	ID              string            `json:"id"`
	Proposer        string            `json:"proposer,omitempty"`
	Title           string            `json:"title,omitempty"`
	Description     string            `json:"description,omitempty"`
	Kind            string            `json:"kind,omitempty"`
	Param           string            `json:"param,omitempty"`
	Value           string            `json:"value,omitempty"`
	Status          ProposalStatus    `json:"status,omitempty"`
	SubmitHeight    uint64            `json:"submit_height,omitempty"`
	VotingEndHeight uint64            `json:"voting_end_height,omitempty"`
	Votes           map[string]uint64 `json:"votes"`
	Voters          map[string]string `json:"voters"`
}

type BlockHeader struct {
	ChainID       string `json:"chain_id"`
	Height        uint64 `json:"height"`
	ParentHash    string `json:"parent_hash"`
	TimeUnix      int64  `json:"time_unix"`
	Proposer      string `json:"proposer"`
	GasLimit      uint64 `json:"gas_limit,omitempty"`
	GasUsed       uint64 `json:"gas_used,omitempty"`
	BaseFeePerGas uint64 `json:"base_fee_per_gas,omitempty"`
	TxRoot        string `json:"tx_root"`
	ReceiptRoot   string `json:"receipt_root"`
	StateRoot     string `json:"state_root"`
}

func (h BlockHeader) SigningBytes() []byte {
	return hash.MustCanonicalBytes(h)
}

type FinalitySignature struct {
	Validator string `json:"validator"`
	Signature string `json:"signature"`
}

type FinalityCertificate struct {
	ChainID    string              `json:"chain_id"`
	Height     uint64              `json:"height"`
	BlockHash  string              `json:"block_hash"`
	Signatures []FinalitySignature `json:"signatures"`
}

type FinalityEquivocationEvidence struct {
	Validator       string `json:"validator"`
	Height          uint64 `json:"height"`
	FirstBlockHash  string `json:"first_block_hash"`
	FirstSignature  string `json:"first_signature"`
	SecondBlockHash string `json:"second_block_hash"`
	SecondSignature string `json:"second_signature"`
}

type finalityVotePayload struct {
	ChainID   string `json:"chain_id"`
	Height    uint64 `json:"height"`
	BlockHash string `json:"block_hash"`
}

func FinalityVoteSigningBytes(chainID string, height uint64, blockHash string) []byte {
	return hash.MustCanonicalBytes(finalityVotePayload{
		ChainID:   chainID,
		Height:    height,
		BlockHash: blockHash,
	})
}

type Block struct {
	Header              BlockHeader          `json:"header"`
	Transactions        []Transaction        `json:"transactions,omitempty"`
	Receipts            []Receipt            `json:"receipts,omitempty"`
	Signature           string               `json:"signature,omitempty"`
	FinalityCertificate *FinalityCertificate `json:"finality_certificate,omitempty"`
}

func (b Block) Hash() string {
	return hash.MustHex(b.Header)
}

func GenesisBlock(chainID string, stateRoot string) Block {
	return Block{
		Header: BlockHeader{
			ChainID:       chainID,
			Height:        0,
			ParentHash:    "",
			TimeUnix:      0,
			Proposer:      "genesis",
			GasLimit:      DefaultBlockGasLimit,
			BaseFeePerGas: InitialBaseFeePerGas,
			TxRoot:        TransactionRoot(nil),
			ReceiptRoot:   ReceiptRoot(nil),
			StateRoot:     stateRoot,
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
