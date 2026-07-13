# ChainLab

<p align="center">
  <strong>Experimental self-hosted blockchain nodes for desktops and trusted small communities.</strong>
</p>

<p align="center">
  <img alt="Go 1.25.12" src="https://img.shields.io/badge/Go-1.25.12-00ADD8?logo=go&logoColor=white">
  <img alt="Desktop target" src="https://img.shields.io/badge/target-desktop_%2F_community-1D6F42">
  <img alt="Public mainnet deferred" src="https://img.shields.io/badge/public_mainnet-deferred-B45309">
</p>

[简体中文](README.zh-CN.md)

ChainLab combines a deterministic ABCI++ application, CometBFT consensus, transactional Pebble storage, and a single desktop lifecycle. One `chainlab` process owns the application, consensus node, lifecycle control endpoint, and local Explorer together.

This repository is experimental software. Native balances, stake, contract tokens, and demo assets are test units or community points. They have no promised market value, fiat redemption, issuer backing, yield, custody, guaranteed uptime, or guaranteed permanence. ChainLab is not ready for public mainnet or valuable assets.

## Release Profiles

| Profile | Purpose | Signing authority | Default exposure |
|---|---|---|---|
| `desktop-solo` | One private network on one computer | One local development validator | ABCI, RPC, lifecycle control, P2P, and Explorer on loopback |
| `home-validator` | Four explicitly invited trusted operators | One locally generated validator identity per operator | RPC/control/Explorer on loopback; P2P on the explicit advertised LAN/public address |
| `observer` | Sync, verify, query, serve Explorer, and broadcast | No validator key or signing state | RPC/control/Explorer on loopback; P2P on the explicit advertised address |

CometBFT does not provide automatic NAT traversal. LAN operation requires reachable local addresses and firewall rules. Different homes require explicit public addresses and port forwarding, or an independently operated overlay. No undocumented hosted seed or relay is required.

## Quick Start

Requirements for source builds: Go 1.25.12 and Git. Windows amd64 and Linux amd64 release packages include the executable, this README, the Chinese README, Quick Start, version metadata, and SHA-256 files.

```powershell
go build -o .\bin\chainlab.exe .\cmd\chainlab
$data = Join-Path $env:LOCALAPPDATA "ChainLab\solo"
.\bin\chainlab.exe desktop init --data-dir $data --chain-id chainlab-solo
.\bin\chainlab.exe desktop start --data-dir $data
```

Keep the start terminal open. Open `http://127.0.0.1:8547/`. From another terminal:

```powershell
.\bin\chainlab.exe desktop status --data-dir $data
.\bin\chainlab.exe desktop stop --data-dir $data
```

Restarting the same data directory preserves chain ID, height, application hash, balances, nonces, and transaction receipts. A second owner is rejected. Forced process termination leaves a stale runtime record but the operating-system lock is released; the next start verifies and recovers the existing stores.

See [Desktop Quick Start](docs/desktop-quick-start.md) for home-validator, observer, backup, restore, update, and uninstall commands.

## Public Invitations

Every operator creates keys locally and shares only `identity.json`:

```powershell
chainlab desktop identity --data-dir D:\ChainLab\validator-0 --role validator --name validator-0 --p2p-address 192.168.1.10:26680
```

One coordinator combines exactly four public identities into a bounded, canonical, SHA-256-protected invitation. The invitation contains chain/profile metadata, canonical genesis bytes, public validator keys, public P2P identities, and advertised addresses. It contains no validator, P2P, account, or service private key.

```powershell
chainlab desktop invitation --out invitation.json --chain-id chainlab-home --network-name "Home Network" --identity validator-0.json --identity validator-1.json --identity validator-2.json --identity validator-3.json
chainlab desktop join --data-dir D:\ChainLab\validator-0 --invitation invitation.json
```

Tampered checksums, wrong-chain genesis, duplicate identities/addresses, unknown fields, unsafe advertised addresses, non-members, and observer directories containing validator signing material fail closed.

## Storage And Recovery

Desktop profiles retain 120,961 application heights and request the same Comet block retention. At the five-second block cadence this covers the protocol's 100,000-block or seven-day evidence window before pruning. The status output reports the configured profile, current retained range, data path, disk bytes, and latest backup metadata.

```powershell
chainlab desktop stop --data-dir $data
chainlab desktop backup --data-dir $data --out D:\Backups\chainlab-solo.zip
chainlab desktop verify --data-dir $data --chain-id chainlab-solo
chainlab desktop restore --backup D:\Backups\chainlab-solo.zip --data-dir D:\ChainLab\restored --chain-id chainlab-solo
```

Backups include private signing material and must be stored as secrets. Each regular file is bounded and recorded with canonical path, size, mode, and SHA-256. Restore extracts into a staging directory, rejects traversal/symlinks/duplicates/corruption/wrong-chain/incompatible material, validates Comet and Pebble state, and publishes only after every check passes.

## Implemented Protocol

- deterministic accounts, fees, EIP-1559-style type-2 transactions, typed raw transactions, receipts, logs, mempool replacement, queued nonces, session keys, delegated EOAs, and social recovery;
- native account/token/counter contracts and deterministic metered WASM execution with bounded modules, memory, fuel, call depth, and host APIs;
- CometBFT ABCI++ proposal processing, vote extensions, evidence handling, state sync, block sync, quorum halt/recovery, validator lifecycle, and fail-closed protocol upgrades through ChainLab V5;
- atomic Pebble state, mutation-derived deltas, checkpoints, sparse proofs, historical reads, snapshots, backup, compaction, integrity validation, and restart recovery;
- bounded RPC, WebSocket subscriptions, filters, queries, transaction pools, and a loopback desktop Explorer.

The local PoA harness remains a development/differential-test tool. CometBFT is the authoritative multi-computer path.

## Resource Evidence

The consumer target is Windows 10/11 amd64, four logical cores, 8 GiB RAM, and 100 GiB free SSD. Results are claims only when tied to the exact machine, duration, height delta, and artifacts in [Desktop Resource Evidence](docs/desktop-resource-evidence-2026-07-13.md).

The current development host is Windows 11 amd64 with an Intel i5-14400F (10 cores/16 logical processors), 47.79 GiB visible RAM, and NTFS storage. Its measurements do not prove the 4-core/8-GiB target. No 24-hour, 10,000-block, or long-term resource claim is made without the corresponding completed run.

## Verification

```powershell
go test -count=1 ./...
go test -race -short -count=1 ./...
go vet ./...
go build ./cmd/...
go run ./cmd/chainlab demo
git diff --check
```

The release gate also runs the isolated five-process validator/observer flow, Markdown/link checks, desktop/mobile browser validation, Windows packaging, Linux packaging, SHA-256 verification, and Linux `go mod verify` using a clean module cache.

## Release Packaging

```powershell
.\scripts\package-windows.ps1 -Version v0.1.0
```

```bash
scripts/package-linux.sh v0.1.0
```

`chainlab version` reports version, commit, build time, Go version, OS, and architecture. Uninstall removes only the program package; user data is never deleted by default.

## Deferred Public-Mainnet Gates

Desktop v0.1 does not include permissionless membership, validator rewards, full staking economics, treasury, public governance, remote signer/HSM, sentry/multi-region operations, official stablecoins, fiat gateways, exchange/bridge/oracle integration, financial custody, financial-grade disaster recovery, or public-mainnet availability claims.

The deferred requirements remain documented in [Mainstream Chain Capability And Production Gates](docs/mainstream-chain-capability-and-production-gates-2026-07-10.md). They must be explicitly reactivated before ChainLab supports strangers, valuable assets, redemption promises, or a public financial network.

## Documentation

| Document | Scope |
|---|---|
| [Desktop Quick Start](docs/desktop-quick-start.md) | Lifecycle, trusted-home join, observer, recovery, update, uninstall |
| [Progress And Next Steps](docs/chainlab-progress-and-next-steps-2026-07-10.md) | Desktop-first decision, completion audit, implementation evidence |
| [Desktop Resource Evidence](docs/desktop-resource-evidence-2026-07-13.md) | Machine specifications, commands, durations, resource artifacts |
| [Application Store V2](docs/chainlab-application-storage-v2.md) | Atomic storage, retention, migration, backup, recovery |
| [Comet Fault Network](docs/chainlab-comet-fault-network.md) | Quorum loss, partition, delay, proposal and evidence guarantees |
| [V2 Validator Lifecycle](docs/chainlab-v2-validator-lifecycle.md) | Validator identity and lifecycle contract |
| [V3 Proofs And Upgrades](docs/chainlab-v3-proofs-and-upgrades.md) | Protocol upgrades and inclusion proofs |
| [V4 Sparse State](docs/chainlab-v4-sparse-state.md) | Sparse membership/non-membership proofs |
| [V5 Offence Retention](docs/chainlab-v5-offence-retention.md) | Authenticated offence retention and compaction |
