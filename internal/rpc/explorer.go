package rpc

import (
	"html/template"
	"net/http"

	"chainlab/internal/node"
	"chainlab/internal/types"
)

const explorerRecentBlockLimit = 8

type explorerPageData struct {
	ChainID    string
	Head       explorerBlock
	Finality   node.FinalityCheckpoint
	Mempool    node.MempoolSnapshot
	Validators []string
	Blocks     []explorerBlock
}

type explorerBlock struct {
	Height           uint64
	Hash             string
	ParentHash       string
	Proposer         string
	StateRoot        string
	TxRoot           string
	ReceiptRoot      string
	TransactionCount int
	Transactions     []explorerTransaction
}

type explorerTransaction struct {
	Hash  string
	Type  types.TxType
	From  string
	To    string
	Value uint64
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

func (s *Server) explorerData() explorerPageData {
	head := s.node.Head()
	finality := s.node.Finality()
	pool := s.node.TxPool()
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

	return explorerPageData{
		ChainID:    s.node.ChainID(),
		Head:       newExplorerBlock(head),
		Finality:   finality,
		Mempool:    pool,
		Validators: validators,
		Blocks:     blocks,
	}
}

func newExplorerBlock(block types.Block) explorerBlock {
	transactions := make([]explorerTransaction, 0, len(block.Transactions))
	for _, tx := range block.Transactions {
		transactions = append(transactions, explorerTransaction{
			Hash:  tx.Hash(),
			Type:  tx.Type,
			From:  tx.From,
			To:    tx.To,
			Value: tx.Value,
		})
	}
	return explorerBlock{
		Height:           block.Header.Height,
		Hash:             block.Hash(),
		ParentHash:       block.Header.ParentHash,
		Proposer:         block.Header.Proposer,
		StateRoot:        block.Header.StateRoot,
		TxRoot:           block.Header.TxRoot,
		ReceiptRoot:      block.Header.ReceiptRoot,
		TransactionCount: len(block.Transactions),
		Transactions:     transactions,
	}
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
      <div class="badge"><span class="dot"></span> Local devnet</div>
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
              <td>{{.Height}}</td>
              <td><div class="hash">{{.Hash}}</div></td>
              <td><div class="hash">{{.Proposer}}</div></td>
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
          <li class="row"><span class="hash">{{.}}</span></li>
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
            <span class="hash">{{.Hash}}</span>
            <span class="hash">{{.From}} -> {{.To}}</span>
          </li>
          {{end}}
        </ul>
        {{else}}
        <div class="empty">No transactions in the current head block.</div>
        {{end}}
      </div>

      <aside class="section">
        <h2>Mempool</h2>
        {{if .Mempool.Pending}}
        <ul class="list">
          {{range .Mempool.Pending}}
          <li class="row">
            <span><span class="pill warn">{{.Type}}</span> value {{.Value}}</span>
            <span class="hash">{{.Hash}}</span>
            <span class="hash">{{.From}} -> {{.To}}</span>
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
