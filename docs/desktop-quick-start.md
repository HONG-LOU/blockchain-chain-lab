# ChainLab Desktop v0.1 Quick Start

ChainLab Desktop is experimental software for local and trusted small-community networks. Native balances, tokens, and other units are test units or community points. They have no promised market value, fiat redemption, custody, yield, or guaranteed permanence.

## Windows Solo Network

Open PowerShell in the extracted package:

```powershell
$data = Join-Path $env:LOCALAPPDATA "ChainLab\solo"
.\chainlab.exe version
.\chainlab.exe desktop init --data-dir $data --chain-id chainlab-solo
.\chainlab.exe desktop start --data-dir $data
```

Keep the start terminal open. The command owns the application, CometBFT, and Explorer together. Open `http://127.0.0.1:8547/`.

From another PowerShell window:

```powershell
.\chainlab.exe desktop status --data-dir $data
.\chainlab.exe desktop stop --data-dir $data
```

Normal stop releases every managed listener and data lock. Starting the same command again resumes the same chain. A second owner of the same data directory is rejected.

## Backup And Restore

Stop the node before backup:

```powershell
.\chainlab.exe desktop backup --data-dir $data --out "$env:USERPROFILE\chainlab-solo-backup.zip"
.\chainlab.exe desktop verify --data-dir $data --chain-id chainlab-solo
.\chainlab.exe desktop restore --backup "$env:USERPROFILE\chainlab-solo-backup.zip" --data-dir "$env:LOCALAPPDATA\ChainLab\restored" --chain-id chainlab-solo
```

Backups contain validator, P2P, and account signing material. Store them as secrets. Restore rejects modified archives, incompatible metadata, a wrong expected chain ID, or an existing destination.

## Trusted Home Network

Each validator generates its identity locally. Share only `identity.json`:

```powershell
.\chainlab.exe desktop identity --data-dir "$env:LOCALAPPDATA\ChainLab\validator-0" --role validator --name validator-0 --p2p-address 192.168.1.10:26680
```

After collecting exactly four public identity files, one coordinator creates the public invitation:

```powershell
.\chainlab.exe desktop invitation --out .\invitation.json --chain-id chainlab-home --network-name "My Home Network" --identity .\validator-0.json --identity .\validator-1.json --identity .\validator-2.json --identity .\validator-3.json
```

Each validator joins using its original local data directory:

```powershell
.\chainlab.exe desktop join --data-dir "$env:LOCALAPPDATA\ChainLab\validator-0" --invitation .\invitation.json
.\chainlab.exe desktop start --data-dir "$env:LOCALAPPDATA\ChainLab\validator-0"
```

For an observer, generate `--role observer`, then join the same invitation. Observer identity directories contain a P2P key but no validator key or signing state.

LAN peers must have mutually reachable addresses and Windows Firewall rules for the selected P2P port. Different homes require explicit public addresses and router port forwarding, or an independently operated overlay network. ChainLab and CometBFT do not provide automatic NAT traversal.

RPC, lifecycle control, ABCI, and Explorer remain bound to `127.0.0.1`. Do not publish them through router forwarding or a reverse proxy without implementing an appropriate authentication and rate-limit boundary.

## Update And Uninstall

Stop ChainLab, verify a new package checksum, replace only the executable, run `version`, and restart. Keep the previous executable until the node has restarted successfully.

To uninstall, stop the node and remove the extracted program directory. User data under `%LOCALAPPDATA%\ChainLab` is not removed automatically. Delete a data directory only after making and verifying a backup and only when permanent chain removal is intended.
