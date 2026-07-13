# ChainLab

<p align="center">
  <strong>面向桌面电脑和可信小型社区的实验性自托管区块链节点。</strong>
</p>

<p align="center">
  <img alt="Go 1.25.12" src="https://img.shields.io/badge/Go-1.25.12-00ADD8?logo=go&logoColor=white">
  <img alt="桌面目标" src="https://img.shields.io/badge/target-desktop_%2F_community-1D6F42">
  <img alt="公共主网延后" src="https://img.shields.io/badge/public_mainnet-deferred-B45309">
</p>

[English](README.md)

ChainLab 将确定性 ABCI++ 应用、CometBFT 共识、Pebble 事务存储和统一桌面生命周期组合在一起。一个 `chainlab` 进程同时管理应用、共识节点、生命周期控制端点和本地 Explorer。

本仓库是实验性软件。原生余额、质押、合约 Token 和演示资产只是测试单位或社区积分，不承诺市场价值、法币兑换、发行方背书、收益、托管、持续在线或永久存在。ChainLab 尚未达到公共主网或真实价值资产使用条件。

## 发布 Profile

| Profile | 用途 | 签名权限 | 默认暴露范围 |
|---|---|---|---|
| `desktop-solo` | 一台电脑上的私有网络 | 一个本地开发验证者 | ABCI、RPC、生命周期控制、P2P、Explorer 全部 loopback |
| `home-validator` | 四名被明确邀请的可信运营者 | 每名运营者各自在本机生成一个验证者身份 | RPC/控制/Explorer 为 loopback；P2P 使用明确填写的局域网或公网地址 |
| `observer` | 同步、验证、查询、提供 Explorer、广播交易 | 没有验证者私钥和签名状态 | RPC/控制/Explorer 为 loopback；P2P 使用明确填写的地址 |

CometBFT 不提供自动 NAT 穿透。局域网需要可互访地址和防火墙规则；不同家庭需要显式公网地址与端口转发，或独立运营的 overlay。网络不会暗中依赖托管 seed 或 relay。

## 快速开始

源码构建需要 Go 1.25.12 和 Git。Windows amd64、Linux amd64 发布包包含可执行文件、中英文 README、Quick Start、版本元数据和 SHA-256 文件。

```powershell
go build -o .\bin\chainlab.exe .\cmd\chainlab
$data = Join-Path $env:LOCALAPPDATA "ChainLab\solo"
.\bin\chainlab.exe desktop init --data-dir $data --chain-id chainlab-solo
.\bin\chainlab.exe desktop start --data-dir $data
```

保持 start 终端开启，并访问 `http://127.0.0.1:8547/`。在另一个终端执行：

```powershell
.\bin\chainlab.exe desktop status --data-dir $data
.\bin\chainlab.exe desktop stop --data-dir $data
```

用同一数据目录重启会保留 chain ID、高度、app hash、余额、nonce 和交易回执。第二个目录所有者会被拒绝。强制结束进程会留下 runtime 记录，但操作系统锁会释放；下次启动会校验并恢复已有存储。

家庭验证者、observer、备份、恢复、更新和卸载命令见 [Desktop Quick Start](docs/desktop-quick-start.md)。

## 公开邀请

每名运营者只在本机生成密钥，对外仅分享 `identity.json`：

```powershell
chainlab desktop identity --data-dir D:\ChainLab\validator-0 --role validator --name validator-0 --p2p-address 192.168.1.10:26680
```

协调者将恰好四份公开身份组合成有大小上限、规范编码并带 SHA-256 的 invitation。它只包含 chain/profile 元数据、规范 genesis、公钥、公开 P2P 身份和地址，不包含验证者、P2P、账户或服务私钥。

```powershell
chainlab desktop invitation --out invitation.json --chain-id chainlab-home --network-name "Home Network" --identity validator-0.json --identity validator-1.json --identity validator-2.json --identity validator-3.json
chainlab desktop join --data-dir D:\ChainLab\validator-0 --invitation invitation.json
```

checksum 篡改、错误链 genesis、重复身份/地址、未知字段、不安全地址、非成员验证者，以及包含验证者签名材料的 observer 都会 fail closed。

## 存储与恢复

桌面 profile 保留 120,961 个应用高度，并请求 Comet 保留同样数量的区块。在五秒出块 cadence 下，这覆盖协议的 100,000 块或 7 天证据窗口后才开始裁剪。`desktop status` 会展示 profile、当前 retained range、数据路径、磁盘字节数和最近备份元数据。

```powershell
chainlab desktop stop --data-dir $data
chainlab desktop backup --data-dir $data --out D:\Backups\chainlab-solo.zip
chainlab desktop verify --data-dir $data --chain-id chainlab-solo
chainlab desktop restore --backup D:\Backups\chainlab-solo.zip --data-dir D:\ChainLab\restored --chain-id chainlab-solo
```

备份包含私密签名材料，必须按 secret 保存。每个普通文件都记录规范路径、大小、权限和 SHA-256。恢复先解压到 staging，拒绝路径穿越、symlink、重复、损坏、错误链和不兼容材料；Comet 与 Pebble 全部验证通过后才原子发布目标目录。

## 已实现协议能力

- 确定性账户、费用、EIP-1559 风格 type-2 交易、typed raw transaction、receipt、log、mempool replacement、future nonce 队列、session key、delegated EOA 和社交恢复；
- 原生 account/token/counter 合约，以及对模块大小、内存、fuel、调用深度和 host API 有明确上限的确定性 WASM；
- CometBFT ABCI++ proposal、vote extension、evidence、state sync、block sync、quorum halt/recovery、验证者生命周期，以及到 ChainLab V5 的 fail-closed 升级；
- Pebble 原子状态、mutation delta、checkpoint、sparse proof、历史查询、snapshot、backup、compaction、完整性验证和重启恢复；
- 有界 RPC、WebSocket subscription、filter、query、txpool 和 loopback Desktop Explorer。

本地 PoA harness 只用于开发和 differential test；CometBFT 才是权威多机路径。

## 资源证据

消费者目标是 Windows 10/11 amd64、四个逻辑核、8 GiB 内存和 100 GiB 可用 SSD。只有同时给出准确机器、持续时间、高度增量和 artifact 的结果才算证据，见 [Desktop Resource Evidence](docs/desktop-resource-evidence-2026-07-13.md)。

当前开发机是 Windows 11 amd64、Intel i5-14400F（10 核/16 逻辑处理器）、47.79 GiB 可见内存和 NTFS 存储。该机器的测量不能证明 4 核/8 GiB 目标。没有完成对应运行前，不声明 24 小时、10,000 块或长期资源表现。

## 验证

```powershell
go test -count=1 ./...
go test -race -short -count=1 ./...
go vet ./...
go build ./cmd/...
go run ./cmd/chainlab demo
git diff --check
```

发布 gate 还包括隔离的五进程 validator/observer 流程、Markdown/link 检查、桌面/移动端浏览器验证、Windows/Linux 打包、SHA-256 校验，以及使用干净 module cache 的 Linux `go mod verify`。

## 发布打包

```powershell
.\scripts\package-windows.ps1 -Version v0.1.0
```

```bash
scripts/package-linux.sh v0.1.0
```

`chainlab version` 输出版本、commit、构建时间、Go 版本、操作系统和架构。卸载默认只移除程序包，绝不会自动删除用户数据。

## 延后的公共主网门槛

Desktop v0.1 不包含 permissionless membership、验证者奖励、完整质押经济、treasury、公共治理、remote signer/HSM、sentry/多区域运维、官方稳定币、法币入口、交易所/桥/oracle 集成、金融托管、金融级灾难恢复或公共主网可用性承诺。

延后要求保留在 [Mainstream Chain Capability And Production Gates](docs/mainstream-chain-capability-and-production-gates-2026-07-10.md)。在 ChainLab 支持陌生人、真实价值资产、兑换承诺或公共金融网络前，必须显式重新启用这些门槛。

## 文档

| 文档 | 范围 |
|---|---|
| [Desktop Quick Start](docs/desktop-quick-start.md) | 生命周期、可信家庭网络、observer、恢复、更新、卸载 |
| [Progress And Next Steps](docs/chainlab-progress-and-next-steps-2026-07-10.md) | 桌面优先决策、完成审计、实现证据 |
| [Desktop Resource Evidence](docs/desktop-resource-evidence-2026-07-13.md) | 机器规格、命令、持续时间、资源 artifact |
| [Application Store V2](docs/chainlab-application-storage-v2.md) | 原子存储、retention、migration、backup、recovery |
| [Comet Fault Network](docs/chainlab-comet-fault-network.md) | quorum loss、partition、delay、proposal、evidence 保证 |
| [V2 Validator Lifecycle](docs/chainlab-v2-validator-lifecycle.md) | 验证者身份和生命周期契约 |
| [V3 Proofs And Upgrades](docs/chainlab-v3-proofs-and-upgrades.md) | 协议升级和 inclusion proof |
| [V4 Sparse State](docs/chainlab-v4-sparse-state.md) | Sparse membership/non-membership proof |
| [V5 Offence Retention](docs/chainlab-v5-offence-retention.md) | 认证 offence retention 与 compaction |
