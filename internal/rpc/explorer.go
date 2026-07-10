package rpc

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"chainlab/internal/node"
	"chainlab/internal/types"
)

const explorerRecentBlockLimit = 8
const explorerRecentEventLimit = 32

type explorerPageData struct {
	ChainID             string
	Head                explorerBlock
	Finality            node.FinalityCheckpoint
	Mempool             node.MempoolSnapshot
	PendingTransactions []explorerTransaction
	Validators          []string
	Blocks              []explorerBlock
}

type explorerBlock struct {
	Height           uint64
	URL              string
	Hash             string
	ParentHash       string
	ParentURL        string
	Proposer         string
	ProposerURL      string
	StateRoot        string
	TxRoot           string
	ReceiptRoot      string
	TransactionCount int
	Transactions     []explorerTransaction
}

type explorerTransaction struct {
	Hash         string
	URL          string
	Type         types.TxType
	From         string
	FromURL      string
	Signer       string
	SignerURL    string
	To           string
	ToURL        string
	Nonce        uint64
	Value        uint64
	GasLimit     uint64
	GasPrice     uint64
	BatchCount   int
	AuthCount    int
	Paymaster    string
	PaymasterURL string
}

type explorerTransactionPageData struct {
	ChainID     string
	Transaction explorerTransaction
	Receipt     types.Receipt
	BlockHeight uint64
	BlockHash   string
	BlockURL    string
	Index       int
	Status      string
}

type explorerAccountPageData struct {
	ChainID     string
	Account     types.Account
	Stake       uint64
	IsValidator bool
}

type explorerEventsPageData struct {
	ChainID string
	Events  []explorerEvent
}

type explorerEvent struct {
	Type             string
	Topic            string
	Address          string
	AddressURL       string
	BlockHeight      uint64
	BlockHash        string
	BlockURL         string
	TransactionHash  string
	TransactionURL   string
	TransactionIndex int
	EventIndex       int
	Attributes       map[string]string
}

func (s *Server) handleExplorer(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/explorer" {
		http.NotFound(w, r)
		return
	}
	data := s.explorerData()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := explorerTemplate.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleExplorerEvents(w http.ResponseWriter, r *http.Request) {
	data := explorerEventsPageData{
		ChainID: s.node.ChainID(),
		Events:  s.explorerEvents(explorerRecentEventLimit),
	}
	writeExplorerHTML(w, explorerEventsTemplate, data)
}

func (s *Server) handleExplorerBlock(w http.ResponseWriter, r *http.Request) {
	height, err := strconv.ParseUint(r.PathValue("height"), 10, 64)
	if err != nil {
		http.Error(w, "invalid block height", http.StatusBadRequest)
		return
	}
	block, ok := s.node.Block(height)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data := struct {
		ChainID string
		Block   explorerBlock
	}{
		ChainID: s.node.ChainID(),
		Block:   newExplorerBlock(block),
	}
	writeExplorerHTML(w, explorerBlockTemplate, data)
}

func (s *Server) handleExplorerTransaction(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimSpace(r.PathValue("hash"))
	if hash == "" {
		http.Error(w, "transaction hash is required", http.StatusBadRequest)
		return
	}
	record, ok := s.node.Transaction(hash)
	if !ok {
		http.NotFound(w, r)
		return
	}
	status := "failed"
	if record.Receipt.Success {
		status = "success"
	}
	data := explorerTransactionPageData{
		ChainID:     s.node.ChainID(),
		Transaction: newExplorerTransaction(record.Transaction),
		Receipt:     record.Receipt,
		BlockHeight: record.BlockHeight,
		BlockHash:   record.BlockHash,
		BlockURL:    explorerBlockURL(record.BlockHeight),
		Index:       record.Index,
		Status:      status,
	}
	writeExplorerHTML(w, explorerTransactionTemplate, data)
}

func (s *Server) handleExplorerAccount(w http.ResponseWriter, r *http.Request) {
	address := strings.TrimSpace(r.PathValue("address"))
	if address == "" {
		http.Error(w, "account address is required", http.StatusBadRequest)
		return
	}
	account := s.node.Account(address)
	data := explorerAccountPageData{
		ChainID:     s.node.ChainID(),
		Account:     account,
		Stake:       s.node.StakeOf(address),
		IsValidator: explorerContains(s.node.Validators(), account.Address),
	}
	writeExplorerHTML(w, explorerAccountTemplate, data)
}

func writeExplorerHTML(w http.ResponseWriter, tmpl *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := tmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) explorerData() explorerPageData {
	head := s.node.Head()
	finality := s.node.Finality()
	pool, err := s.node.TxPoolBounded(MaxTxPoolResponseSourceBytes)
	if err != nil {
		pending, queued := s.node.TxPoolCounts()
		pool.PendingCount = pending
		pool.QueuedCount = queued
	}
	validators := s.node.Validators()

	blocks := make([]explorerBlock, 0, explorerRecentBlockLimit)
	for height := head.Header.Height; ; height-- {
		block, ok := s.node.Block(height)
		if !ok {
			break
		}
		blocks = append(blocks, newExplorerBlock(block))
		if height == 0 || len(blocks) == explorerRecentBlockLimit {
			break
		}
	}
	pendingTransactions := make([]explorerTransaction, 0, len(pool.Pending))
	for _, tx := range pool.Pending {
		pendingTransactions = append(pendingTransactions, newExplorerTransaction(tx))
	}

	return explorerPageData{
		ChainID:             s.node.ChainID(),
		Head:                newExplorerBlock(head),
		Finality:            finality,
		Mempool:             pool,
		PendingTransactions: pendingTransactions,
		Validators:          validators,
		Blocks:              blocks,
	}
}

func (s *Server) explorerEvents(limit int) []explorerEvent {
	if limit <= 0 {
		return nil
	}
	head := s.node.Head()
	events := make([]explorerEvent, 0, limit)
	records := s.node.Events(node.EventFilter{
		FromBlock:  0,
		ToBlock:    head.Header.Height,
		HasToBlock: true,
		Limit:      limit,
		Descending: true,
	})
	for _, record := range records {
		events = append(events, newExplorerEvent(record))
	}
	return events
}

func newExplorerEvent(record types.EventRecord) explorerEvent {
	return explorerEvent{
		Type:             record.Event.Type,
		Topic:            record.Topic0,
		Address:          record.Address,
		AddressURL:       explorerAccountURL(record.Address),
		BlockHeight:      record.BlockHeight,
		BlockHash:        record.BlockHash,
		BlockURL:         explorerBlockURL(record.BlockHeight),
		TransactionHash:  record.TransactionHash,
		TransactionURL:   explorerTransactionURL(record.TransactionHash),
		TransactionIndex: record.TransactionIndex,
		EventIndex:       record.EventIndex,
		Attributes:       record.Event.Attributes,
	}
}

func newExplorerBlock(block types.Block) explorerBlock {
	transactions := make([]explorerTransaction, 0, len(block.Transactions))
	for _, tx := range block.Transactions {
		transactions = append(transactions, newExplorerTransaction(tx))
	}
	parentURL := ""
	if block.Header.Height > 0 {
		parentURL = explorerBlockURL(block.Header.Height - 1)
	}
	return explorerBlock{
		Height:           block.Header.Height,
		URL:              explorerBlockURL(block.Header.Height),
		Hash:             block.Hash(),
		ParentHash:       block.Header.ParentHash,
		ParentURL:        parentURL,
		Proposer:         block.Header.Proposer,
		ProposerURL:      explorerAccountURL(block.Header.Proposer),
		StateRoot:        block.Header.StateRoot,
		TxRoot:           block.Header.TxRoot,
		ReceiptRoot:      block.Header.ReceiptRoot,
		TransactionCount: len(block.Transactions),
		Transactions:     transactions,
	}
}

func newExplorerTransaction(tx types.Transaction) explorerTransaction {
	return explorerTransaction{
		Hash:         tx.Hash(),
		URL:          explorerTransactionURL(tx.Hash()),
		Type:         tx.Type,
		From:         tx.From,
		FromURL:      explorerAccountURL(tx.From),
		Signer:       tx.Signer,
		SignerURL:    explorerAccountURL(tx.Signer),
		To:           tx.To,
		ToURL:        explorerAccountURL(tx.To),
		Nonce:        tx.Nonce,
		Value:        tx.Value,
		GasLimit:     tx.GasLimit,
		GasPrice:     tx.GasPrice,
		BatchCount:   len(tx.Batch),
		AuthCount:    len(tx.Authorizations),
		Paymaster:    tx.Paymaster,
		PaymasterURL: explorerAccountURL(tx.Paymaster),
	}
}

func explorerBlockURL(height uint64) string {
	return "/explorer/block/" + strconv.FormatUint(height, 10)
}

func explorerTransactionURL(hash string) string {
	if strings.TrimSpace(hash) == "" {
		return ""
	}
	return "/explorer/tx/" + hash
}

func explorerAccountURL(address string) string {
	if strings.TrimSpace(address) == "" {
		return ""
	}
	return "/explorer/account/" + strings.ToLower(strings.TrimSpace(address))
}

func explorerContains(values []string, target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == target {
			return true
		}
	}
	return false
}

var explorerTemplate = template.Must(template.New("explorer").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>ChainLab Explorer</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f6f8fb;
      --panel: #ffffff;
      --ink: #172033;
      --muted: #5f6b7a;
      --line: #d9e0ea;
      --accent: #0f766e;
      --accent-soft: #d9f2ed;
      --warn: #9a5b00;
      --warn-soft: #fff0c2;
      --code: #263141;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      background: var(--bg);
      color: var(--ink);
      font: 14px/1.5 ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    .shell { max-width: 1180px; margin: 0 auto; padding: 24px; }
    header {
      display: flex;
      align-items: flex-end;
      justify-content: space-between;
      gap: 16px;
      padding: 14px 0 22px;
      border-bottom: 1px solid var(--line);
    }
    h1 { margin: 0; font-size: 28px; line-height: 1.1; }
    h2 { margin: 0 0 12px; font-size: 16px; }
    a { color: #075985; text-decoration: none; }
    a:hover { text-decoration: underline; }
    .network { color: var(--muted); margin-top: 6px; }
    .badge {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      border: 1px solid var(--line);
      border-radius: 999px;
      background: var(--panel);
      padding: 7px 11px;
      color: var(--muted);
      white-space: nowrap;
    }
    .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--accent); box-shadow: 0 0 0 4px var(--accent-soft); }
    .header-actions { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
    .stats {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      gap: 12px;
      margin: 18px 0;
    }
    .stat, .section {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      box-shadow: 0 1px 2px rgba(23, 32, 51, 0.04);
    }
    .stat { padding: 14px; min-width: 0; }
    .label { color: var(--muted); font-size: 12px; text-transform: uppercase; letter-spacing: 0; }
    .value { margin-top: 6px; font-size: 24px; font-weight: 700; line-height: 1.15; }
    .hash {
      color: var(--code);
      font-family: ui-monospace, SFMono-Regular, Consolas, "Liberation Mono", monospace;
      font-size: 12px;
      overflow-wrap: anywhere;
    }
    .grid {
      display: grid;
      grid-template-columns: minmax(0, 1.4fr) minmax(280px, 0.6fr);
      gap: 16px;
      align-items: start;
    }
    .section { padding: 16px; min-width: 0; }
    table { width: 100%; border-collapse: collapse; }
    th, td { padding: 10px 8px; border-top: 1px solid var(--line); text-align: left; vertical-align: top; }
    th { color: var(--muted); font-size: 12px; font-weight: 600; text-transform: uppercase; letter-spacing: 0; }
    .list { display: grid; gap: 10px; margin: 0; padding: 0; list-style: none; }
    .row {
      display: grid;
      gap: 3px;
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fbfcfe;
    }
    .pill {
      display: inline-block;
      border-radius: 999px;
      padding: 2px 8px;
      background: var(--accent-soft);
      color: var(--accent);
      font-size: 12px;
      font-weight: 700;
    }
    .warn { background: var(--warn-soft); color: var(--warn); }
    .empty { color: var(--muted); padding: 10px 0; }
    @media (max-width: 860px) {
      .shell { padding: 16px; }
      header { align-items: flex-start; flex-direction: column; }
      .stats { grid-template-columns: repeat(2, minmax(0, 1fr)); }
      .grid { grid-template-columns: 1fr; }
    }
    @media (max-width: 520px) {
      .stats { grid-template-columns: 1fr; }
      th:nth-child(3), td:nth-child(3) { display: none; }
    }
  </style>
</head>
<body>
  <main class="shell">
    <header>
      <div>
        <h1>ChainLab Explorer</h1>
        <div class="network">{{.ChainID}}</div>
      </div>
      <div class="header-actions">
        <a class="badge" href="/explorer/events">Events</a>
        <div class="badge"><span class="dot"></span> Local devnet</div>
      </div>
    </header>

    <section class="stats" aria-label="Chain overview">
      <div class="stat">
        <div class="label">Head Height</div>
        <div class="value">{{.Head.Height}}</div>
        <div class="hash">{{.Head.Hash}}</div>
      </div>
      <div class="stat">
        <div class="label">Finalized Height</div>
        <div class="value">{{.Finality.FinalizedHeight}}</div>
        <div class="hash">{{.Finality.FinalizedHash}}</div>
      </div>
      <div class="stat">
        <div class="label">Safe Height</div>
        <div class="value">{{.Finality.SafeHeight}}</div>
        <div class="hash">{{.Finality.SafeHash}}</div>
      </div>
      <div class="stat">
        <div class="label">Pending Transactions</div>
        <div class="value">{{.Mempool.PendingCount}}</div>
        <span class="pill warn">queued {{.Mempool.QueuedCount}}</span>
      </div>
    </section>

    <section class="grid">
      <div class="section">
        <h2>Recent Blocks</h2>
        <table>
          <thead>
            <tr><th>Height</th><th>Hash</th><th>Proposer</th><th>Txs</th></tr>
          </thead>
          <tbody>
            {{range .Blocks}}
            <tr>
              <td><a href="{{.URL}}">{{.Height}}</a></td>
              <td><div class="hash"><a href="{{.URL}}">{{.Hash}}</a></div></td>
              <td><div class="hash"><a href="{{.ProposerURL}}">{{.Proposer}}</a></div></td>
              <td>{{.TransactionCount}}</td>
            </tr>
            {{end}}
          </tbody>
        </table>
      </div>

      <aside class="section">
        <h2>Validators</h2>
        <ul class="list">
          {{range .Validators}}
          <li class="row"><a class="hash" href="/explorer/account/{{.}}">{{.}}</a></li>
          {{end}}
        </ul>
      </aside>
    </section>

    <section class="grid" style="margin-top:16px">
      <div class="section">
        <h2>Head Block Transactions</h2>
        {{if .Head.Transactions}}
        <ul class="list">
          {{range .Head.Transactions}}
          <li class="row">
            <span><span class="pill">{{.Type}}</span> value {{.Value}}</span>
            <a class="hash" href="{{.URL}}">{{.Hash}}</a>
            <span class="hash"><a href="{{.FromURL}}">{{.From}}</a> -> {{if .ToURL}}<a href="{{.ToURL}}">{{.To}}</a>{{else}}-{{end}}</span>
          </li>
          {{end}}
        </ul>
        {{else}}
        <div class="empty">No transactions in the current head block.</div>
        {{end}}
      </div>

      <aside class="section">
        <h2>Mempool</h2>
        {{if .PendingTransactions}}
        <ul class="list">
          {{range .PendingTransactions}}
          <li class="row">
            <span><span class="pill warn">{{.Type}}</span> value {{.Value}}</span>
            <a class="hash" href="{{.URL}}">{{.Hash}}</a>
            <span class="hash"><a href="{{.FromURL}}">{{.From}}</a> -> {{if .ToURL}}<a href="{{.ToURL}}">{{.To}}</a>{{else}}-{{end}}</span>
          </li>
          {{end}}
        </ul>
        {{else}}
        <div class="empty">No pending transactions.</div>
        {{end}}
      </aside>
    </section>
  </main>
</body>
</html>`))

func explorerDetailHTML(title string, body string) string {
	return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>` + title + ` - ChainLab Explorer</title>
  <style>
    :root {
      --bg: #f6f8fb;
      --panel: #ffffff;
      --ink: #172033;
      --muted: #5f6b7a;
      --line: #d9e0ea;
      --accent: #0f766e;
      --accent-soft: #d9f2ed;
      --code: #263141;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      background: var(--bg);
      color: var(--ink);
      font: 14px/1.5 ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    .shell { max-width: 1060px; margin: 0 auto; padding: 24px; }
    header {
      display: flex;
      justify-content: space-between;
      gap: 16px;
      align-items: flex-start;
      padding: 14px 0 22px;
      border-bottom: 1px solid var(--line);
    }
    h1 { margin: 0; font-size: 28px; line-height: 1.1; }
    h2 { margin: 0 0 12px; font-size: 16px; }
    a { color: #075985; text-decoration: none; }
    a:hover { text-decoration: underline; }
    .network { color: var(--muted); margin-top: 6px; }
    .section {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      box-shadow: 0 1px 2px rgba(23, 32, 51, 0.04);
      padding: 16px;
      margin-top: 16px;
      min-width: 0;
    }
    .grid {
      display: grid;
      grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
      gap: 16px;
    }
    .field {
      display: grid;
      grid-template-columns: 150px minmax(0, 1fr);
      gap: 12px;
      padding: 9px 0;
      border-top: 1px solid var(--line);
    }
    .field:first-child { border-top: 0; }
    .label { color: var(--muted); font-size: 12px; text-transform: uppercase; letter-spacing: 0; }
    .hash {
      color: var(--code);
      font-family: ui-monospace, SFMono-Regular, Consolas, "Liberation Mono", monospace;
      font-size: 12px;
      overflow-wrap: anywhere;
    }
    .pill {
      display: inline-block;
      border-radius: 999px;
      padding: 2px 8px;
      background: var(--accent-soft);
      color: var(--accent);
      font-size: 12px;
      font-weight: 700;
    }
    table { width: 100%; border-collapse: collapse; }
    th, td { padding: 10px 8px; border-top: 1px solid var(--line); text-align: left; vertical-align: top; }
    th { color: var(--muted); font-size: 12px; font-weight: 600; text-transform: uppercase; letter-spacing: 0; }
    @media (max-width: 760px) {
      .shell { padding: 16px; }
      header, .grid { grid-template-columns: 1fr; display: grid; }
      .field { grid-template-columns: 1fr; gap: 3px; }
    }
  </style>
</head>
<body>
  <main class="shell">` + body + `
  </main>
</body>
</html>`
}

var explorerBlockTemplate = template.Must(template.New("explorer-block").Parse(explorerDetailHTML("Block Details", `
    <header>
      <div>
        <h1>Block Details</h1>
        <div class="network">{{.ChainID}}</div>
      </div>
      <a href="/explorer">Back to Explorer</a>
    </header>

    <section class="section">
      <div class="field"><div class="label">Height</div><div>{{.Block.Height}}</div></div>
      <div class="field"><div class="label">Hash</div><div class="hash">{{.Block.Hash}}</div></div>
      <div class="field"><div class="label">Parent Hash</div><div class="hash">{{if .Block.ParentURL}}<a href="{{.Block.ParentURL}}">{{.Block.ParentHash}}</a>{{else}}{{.Block.ParentHash}}{{end}}</div></div>
      <div class="field"><div class="label">Proposer</div><div class="hash"><a href="{{.Block.ProposerURL}}">{{.Block.Proposer}}</a></div></div>
      <div class="field"><div class="label">State Root</div><div class="hash">{{.Block.StateRoot}}</div></div>
      <div class="field"><div class="label">Tx Root</div><div class="hash">{{.Block.TxRoot}}</div></div>
      <div class="field"><div class="label">Receipt Root</div><div class="hash">{{.Block.ReceiptRoot}}</div></div>
      <div class="field"><div class="label">Transactions</div><div>{{.Block.TransactionCount}}</div></div>
    </section>

    <section class="section">
      <h2>Transactions</h2>
      {{if .Block.Transactions}}
      <table>
        <thead><tr><th>Hash</th><th>Type</th><th>From</th><th>To</th><th>Value</th></tr></thead>
        <tbody>
          {{range .Block.Transactions}}
          <tr>
            <td><a class="hash" href="{{.URL}}">{{.Hash}}</a></td>
            <td><span class="pill">{{.Type}}</span></td>
            <td><a class="hash" href="{{.FromURL}}">{{.From}}</a></td>
            <td>{{if .ToURL}}<a class="hash" href="{{.ToURL}}">{{.To}}</a>{{else}}-{{end}}</td>
            <td>{{.Value}}</td>
          </tr>
          {{end}}
        </tbody>
      </table>
      {{else}}
      <div class="label">No transactions in this block.</div>
      {{end}}
    </section>`)))

var explorerTransactionTemplate = template.Must(template.New("explorer-transaction").Parse(explorerDetailHTML("Transaction Details", `
    <header>
      <div>
        <h1>Transaction Details</h1>
        <div class="network">{{.ChainID}}</div>
      </div>
      <a href="/explorer">Back to Explorer</a>
    </header>

    <section class="grid">
      <div class="section">
        <h2>Transaction</h2>
        <div class="field"><div class="label">Hash</div><div class="hash">{{.Transaction.Hash}}</div></div>
        <div class="field"><div class="label">Type</div><div><span class="pill">{{.Transaction.Type}}</span></div></div>
        <div class="field"><div class="label">From</div><div><a class="hash" href="{{.Transaction.FromURL}}">{{.Transaction.From}}</a></div></div>
        {{if .Transaction.Signer}}<div class="field"><div class="label">Signer</div><div><a class="hash" href="{{.Transaction.SignerURL}}">{{.Transaction.Signer}}</a></div></div>{{end}}
        <div class="field"><div class="label">To</div><div>{{if .Transaction.ToURL}}<a class="hash" href="{{.Transaction.ToURL}}">{{.Transaction.To}}</a>{{else}}-{{end}}</div></div>
        <div class="field"><div class="label">Nonce</div><div>{{.Transaction.Nonce}}</div></div>
        <div class="field"><div class="label">Value</div><div>{{.Transaction.Value}}</div></div>
        {{if .Transaction.BatchCount}}<div class="field"><div class="label">Operations</div><div>{{.Transaction.BatchCount}}</div></div>{{end}}
        {{if .Transaction.AuthCount}}<div class="field"><div class="label">Authorizations</div><div>{{.Transaction.AuthCount}}</div></div>{{end}}
        <div class="field"><div class="label">Gas Limit</div><div>{{.Transaction.GasLimit}}</div></div>
        <div class="field"><div class="label">Gas Price</div><div>{{.Transaction.GasPrice}}</div></div>
        {{if .Transaction.Paymaster}}<div class="field"><div class="label">Paymaster</div><div><a class="hash" href="{{.Transaction.PaymasterURL}}">{{.Transaction.Paymaster}}</a></div></div>{{end}}
      </div>

      <div class="section">
        <h2>Receipt</h2>
        <div class="field"><div class="label">Status</div><div>{{.Status}}</div></div>
        <div class="field"><div class="label">Gas Used</div><div>{{.Receipt.GasUsed}}</div></div>
        <div class="field"><div class="label">Block</div><div><a href="{{.BlockURL}}">{{.BlockHeight}}</a></div></div>
        <div class="field"><div class="label">Block Hash</div><div class="hash">{{.BlockHash}}</div></div>
        <div class="field"><div class="label">Index</div><div>{{.Index}}</div></div>
        {{if .Receipt.FeePayer}}<div class="field"><div class="label">Fee Payer</div><div><a class="hash" href="/explorer/account/{{.Receipt.FeePayer}}">{{.Receipt.FeePayer}}</a></div></div>{{end}}
        {{if .Receipt.ContractAddress}}<div class="field"><div class="label">Contract</div><div><a class="hash" href="/explorer/account/{{.Receipt.ContractAddress}}">{{.Receipt.ContractAddress}}</a></div></div>{{end}}
        {{if .Receipt.Error}}<div class="field"><div class="label">Error</div><div>{{.Receipt.Error}}</div></div>{{end}}
      </div>
    </section>`)))

var explorerAccountTemplate = template.Must(template.New("explorer-account").Parse(explorerDetailHTML("Account Details", `
    <header>
      <div>
        <h1>Account Details</h1>
        <div class="network">{{.ChainID}}</div>
      </div>
      <a href="/explorer">Back to Explorer</a>
    </header>

    <section class="section">
      <div class="field"><div class="label">Address</div><div class="hash">{{.Account.Address}}</div></div>
      <div class="field"><div class="label">Balance</div><div>{{.Account.Balance}}</div></div>
      <div class="field"><div class="label">Nonce</div><div>{{.Account.Nonce}}</div></div>
      <div class="field"><div class="label">Stake</div><div>{{.Stake}}</div></div>
      <div class="field"><div class="label">Validator</div><div>{{.IsValidator}}</div></div>
      {{if .Account.CodeID}}<div class="field"><div class="label">Code ID</div><div>{{.Account.CodeID}}</div></div>{{end}}
      {{if .Account.DelegatedCodeID}}<div class="field"><div class="label">Delegated Code</div><div>{{.Account.DelegatedCodeID}}</div></div>{{end}}
    </section>

    {{if .Account.Storage}}
    <section class="section">
      <h2>Storage</h2>
      <table>
        <thead><tr><th>Key</th><th>Value</th></tr></thead>
        <tbody>
          {{range $key, $value := .Account.Storage}}
          <tr><td class="hash">{{$key}}</td><td class="hash">{{$value}}</td></tr>
          {{end}}
        </tbody>
      </table>
    </section>
    {{end}}`)))

var explorerEventsTemplate = template.Must(template.New("explorer-events").Parse(explorerDetailHTML("Contract Events", `
    <header>
      <div>
        <h1>Contract Events</h1>
        <div class="network">{{.ChainID}}</div>
      </div>
      <a href="/explorer">Back to Explorer</a>
    </header>

    <section class="section">
      <h2>Recent Events</h2>
      {{if .Events}}
      <table>
        <thead>
          <tr><th>Event</th><th>Address</th><th>Block</th><th>Transaction</th><th>Attributes</th></tr>
        </thead>
        <tbody>
          {{range .Events}}
          <tr>
            <td>
              <span class="pill">{{.Type}}</span>
              <div class="hash">{{.Topic}}</div>
            </td>
            <td>{{if .AddressURL}}<a class="hash" href="{{.AddressURL}}">{{.Address}}</a>{{else}}-{{end}}</td>
            <td>
              <a href="{{.BlockURL}}">{{.BlockHeight}}</a>
              <div class="hash">{{.BlockHash}}</div>
            </td>
            <td>
              <a class="hash" href="{{.TransactionURL}}">{{.TransactionHash}}</a>
              <div class="label">tx {{.TransactionIndex}} event {{.EventIndex}}</div>
            </td>
            <td>
              {{if .Attributes}}
              {{range $key, $value := .Attributes}}
              <div><span class="label">{{$key}}</span> <span class="hash">{{$value}}</span></div>
              {{end}}
              {{else}}-{{end}}
            </td>
          </tr>
          {{end}}
        </tbody>
      </table>
      {{else}}
      <div class="label">No contract events have been emitted yet.</div>
      {{end}}
    </section>`)))
