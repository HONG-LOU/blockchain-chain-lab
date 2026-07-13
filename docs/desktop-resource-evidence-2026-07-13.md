# ChainLab Desktop Resource Evidence

Status date: 2026-07-13

This document records measurements, not estimates. Results apply only to the stated machine, build, duration, workload, and artifact. They do not prove the 4-core/8-GiB consumer target, a 24-hour soak, or long-term operation.

## Machine

| Item | Value |
|---|---|
| OS | Microsoft Windows 11 Pro 10.0.26200, 64-bit |
| CPU | Intel Core i5-14400F, 10 cores, 16 logical processors, reported max 2500 MHz |
| Visible RAM | 51,318,800,384 bytes (47.79 GiB) |
| Measurement volume | C:, NTFS, 644,036,972,544 bytes total, about 89.3 GB free during the runs |
| Go | `go version go1.25.12 windows/amd64` |
| Candidate | Windows/Linux amd64 `v0.1.0-rc1`, commit `1cb93261a9404a65e5a471b243acb9017699d922` |

The reference target remains Windows 10/11 amd64, four logical cores, 8 GiB RAM, and 100 GiB free SSD. This development host has materially more CPU and memory and therefore cannot close that hardware gate.

## Repeatable Commands

```powershell
.\scripts\measure-desktop.ps1 -Mode solo -DurationSeconds 30
.\scripts\measure-desktop.ps1 -Mode four-validator -DurationSeconds 30
$env:CHAINLAB_DESKTOP_10000_BLOCK_MEASURE = "D:\blockchain-chain-lab\output\desktop-measurements\10000-blocks-accelerated-20260713.json"
go test -count=1 -run '^TestMeasureTenThousandEmptyBlocks$' -v -timeout 12m ./internal/desktop
```

The PowerShell harness creates GUID-owned data below `%TEMP%`, starts the packaged executable, samples the process, records artifacts, stops through the lifecycle API, verifies process/listener/runtime cleanup, and deletes only its owned temporary data. Full JSON samples and logs remain under the ignored local `output/desktop-measurements/` directory.

## Solo Baseline

Artifact: `output/desktop-measurements/20260713T060951Z-solo-172218ca/measurement.json`

Artifact SHA-256: `d55426278ea0ef0188edf595f44d1ab7852307dfba09fb4d38b58d7b54c27654`

| Metric | Result |
|---|---:|
| Measured idle duration | 30 s |
| Startup through committed height 1 | 7.198 s |
| Height | 1 to 7 (+6) |
| Peak RSS | 53,788,672 bytes |
| Steady sampled RSS average | 53,675,008 bytes |
| CPU as percent of all 16 logical processors | 0.039% |
| Data bytes growth | 60,114 bytes |
| Listening ports while running | 5, all `127.0.0.1` |
| ChainLab-managed child processes | 0 |
| Windows console-host processes from hidden measurement launcher | 1 `conhost.exe` |
| Clean stop | process exited; 0 managed listeners; 0 runtime files |

System-wide adapter delta was 7,360,320 received bytes and 8,906,112 sent bytes. This includes unrelated machine traffic and is not a process-isolated ChainLab network measurement, so it is not promoted as an idle-traffic claim.

## Four-Validator Baseline

Artifact: `output/desktop-measurements/20260713T061057Z-four-validator-33179304/measurement.json`

Artifact SHA-256: `8067dfe1e3a39bdd2723c356fe02473a3f048ee75048d9772fefc05b7df3829a`

| Metric | Result |
|---|---:|
| Measured idle duration | 30 s |
| Four nodes through committed height 1 | 7.249 s |
| Height delta per node | +6, +6, +6, +6 |
| Maximum per-process peak RSS | 60,018,688 bytes |
| Steady sampled per-process RSS average | 56,330,069 bytes |
| Aggregate CPU as percent of all 16 logical processors | 0.225% |
| Aggregate data bytes growth | 421,120 bytes |
| Listening ports while running | 20, all `127.0.0.1` |
| ChainLab-managed child processes per node | 0 |
| Windows console-host processes per hidden launcher | 1 `conhost.exe` |
| Clean stop | all processes exited; 0 managed listeners; 0 runtime files |

System-wide adapter delta was 6,230,182 received bytes and 9,547,259 sent bytes. It is retained as raw environmental context only.

## RC Package Evidence

Both packages report `v0.1.0-rc1` and commit `1cb93261a9404a65e5a471b243acb9017699d922` when executed on their target operating system.

| Artifact | SHA-256 |
|---|---|
| Windows amd64 `chainlab.exe` | `746c510c4955b617feaad62400aaa78fa4b965c0795ac3ba0766ecbab0097bad` |
| Windows amd64 zip | `b4573be5cc740e73237799d4460292fe708da9c10c33e317b54a5dfc38bb8e4b` |
| Linux amd64 `chainlab` | `0f0fbb948e41b684dc98220382f31de5b938667f862b400ba81fb1feb1b97c3e` |
| Linux amd64 tar.gz | `d9678986cef312158c845f22f9058105bcc77ad8f87663c3d056e83eb29da9fe` |

## 10,000-Block Storage Run

Artifact: `output/desktop-measurements/10000-blocks-accelerated-20260713.json`

Artifact SHA-256: `5bff52d54373e66ff2405b4f162c967fbe0bbcfd78380172a653dab8f9c5d41a`

| Metric | Result |
|---|---:|
| Protocol path | Real one-validator CometBFT plus ChainLab V2 to V5 and Pebble |
| Retention | 120,961 application heights and Comet blocks |
| Measurement cadence | 5 ms commit timeout, storage-only accelerated run |
| Duration | 181.0850399 s |
| Height | 1 to 10,001 (+10,000) |
| Disk bytes at height 1 | 20,602 bytes |
| Disk bytes at height 10,001 | 51,625,821 bytes |
| Disk growth per 10,000 empty blocks | 51,605,219 bytes |

This accelerated run is evidence for empty-block storage growth only. It is not release-cadence CPU, RSS, network, sleep/resume, 24-hour, or soak evidence. The 120,961 retention boundary was not crossed, so it does not measure post-pruning steady-state disk size.

## Network And Recovery Evidence

`TestHomeValidatorQuorumRecoveryAndObserverBroadcast` runs four separately rooted validator processes plus an observer process. It verifies exact public invitation membership, full peer mesh, 3-of-4 progress, deterministic 2-of-4 halt with a transaction accepted into the live mempool, recovery and commit after returning to 3-of-4, four-validator convergence, observer sync/query, absence of validator key/state in the observer home, observer broadcast, and five-node account convergence.

`TestDesktopLifecycleTransferAndRestart`, `TestDesktopForcedTerminationRecoversStaleRuntime`, and `TestDesktopBackupRestoreAndIntegrityRejection` verify normal restart, forced-process recovery, duplicate owner rejection, account/receipt persistence, complete backup, destructive-copy restore semantics, wrong-chain rejection, corrupted archive rejection, and clean stop.

These processes share one physical host and loopback network. They are equivalent isolated homes for filesystem/key/process ownership, but they are not four separately operated physical computers and do not close the real-home hardware/network gate.

## Open Resource Gates

- repeat solo measurements on the exact 4-core/8-GiB/100-GiB-free-SSD Windows reference machine;
- process-isolated idle network bytes rather than system-wide adapter deltas;
- a 24-hour release-cadence soak and a post-120,961-height pruning steady-state run;
- separately operated physical-home or equivalent VM/container host evidence, including dynamic IP, firewall, sleep/resume, and interrupted shutdown;
- sustained RSS, log rotation, and state-sync resource results over materially longer durations.

Until these runs exist, no README or release note may claim the corresponding target or long-term behavior as proven.
