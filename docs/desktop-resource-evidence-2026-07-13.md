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
| Current candidate | Windows/Linux amd64 `v0.1.0-rc2`, commit `c87e291c154cba89d979a4306d02ae9ce19b0e86` |
| Solo/four-process baseline build | Windows amd64 `v0.1.0-rc1`, commit `1cb93261a9404a65e5a471b243acb9017699d922` |

The reference target remains Windows 10/11 amd64, four logical cores, 8 GiB RAM, and 100 GiB free SSD. This development host has materially more CPU and memory and therefore cannot close that hardware gate.

## Repeatable Commands

```powershell
.\scripts\measure-desktop.ps1 -Mode solo -DurationSeconds 30
.\scripts\measure-desktop.ps1 -Mode four-validator -DurationSeconds 30
$env:CHAINLAB_DESKTOP_10000_BLOCK_MEASURE = "D:\blockchain-chain-lab\output\desktop-measurements\10000-blocks-accelerated-20260713.json"
go test -count=1 -run '^TestMeasureTenThousandEmptyBlocks$' -v -timeout 12m ./internal/desktop
$env:CHAINLAB_DESKTOP_RETENTION_MEASURE = "D:\blockchain-chain-lab\output\desktop-measurements\retention-final-c87e291.json"
go test -count=1 -run '^TestMeasureRetentionBoundary$' -v -timeout 130m ./internal/desktop
.\scripts\measure-desktop-container-hosts.ps1 -DurationSeconds 30 -Executable .\dist\chainlab-v0.1.0-rc2-linux-amd64\chainlab
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

Both packages report `v0.1.0-rc2` and commit `c87e291c154cba89d979a4306d02ae9ce19b0e86` when executed on their target operating system. The Linux build also passed `go mod verify` in a clean `golang:1.25-bookworm` container.

| Artifact | SHA-256 |
|---|---|
| Windows amd64 `chainlab.exe` | `80b968dfce8b0b8979c8341d54581ff6e3d2bdee110ab410be06ec82083784cf` |
| Windows amd64 zip | `566f7d03e1fd37a20b0182f97b93bb672b9afb511c9ac90f9469728e70d03927` |
| Linux amd64 `chainlab` | `d3fe8f4fdcbda484f37c924981e834e0275456d512675f0a987f4d9e7869cdff` |
| Linux amd64 tar.gz | `29b8a9af5f072182648d3f335c99398ec63bc99d78d558ca07a514d8c1ec650b` |

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

## Retention Boundary Run

Artifact: `output/desktop-measurements/retention-final-c87e291.json`

Artifact SHA-256: `d44f6ca3300b0fdabc842d8b23c4b6f598015f9cfce55dbc214502c4d46b12a7`

| Metric | Result |
|---|---:|
| Source | clean commit `c87e291c154cba89d979a4306d02ae9ce19b0e86` |
| Duration | 3,268.4113021 s |
| Retention | 120,961 application heights and Comet blocks |
| Initial checkpoint | observed 6; application minimum 0; 57,763 bytes |
| First boundary checkpoint | observed 121,060; application minimum 100; 444,434,200 bytes |
| Second boundary checkpoint | observed 241,960; application minimum 121,000; 739,913,230 bytes |
| First-window growth | 444,376,437 bytes |
| Second-window growth | 295,479,030 bytes |
| Comet height 1 query | pruned while latest block remained queryable |
| Application retained range | bounded to at most 120,961 heights |
| Ordered shutdown and cleanup | test PASS; recorded temporary root absent |

This directly proves both application and Comet pruning across two retention windows on the accelerated empty-block path. It is not a release-cadence CPU/RSS result, a 24-hour soak, or evidence that disk growth has reached a long-term steady plateau.

## Network And Recovery Evidence

`TestHomeValidatorQuorumRecoveryAndObserverBroadcast` runs four separately rooted validator processes plus an observer process. It verifies exact public invitation membership, full peer mesh, 3-of-4 progress, deterministic 2-of-4 halt with a transaction accepted into the live mempool, recovery and commit after returning to 3-of-4, four-validator convergence, observer sync/query, absence of validator key/state in the observer home, observer broadcast, and five-node account convergence.

`TestDesktopLifecycleTransferAndRestart`, `TestDesktopForcedTerminationRecoversStaleRuntime`, and `TestDesktopBackupRestoreAndIntegrityRejection` verify normal restart, forced-process recovery, duplicate owner rejection, account/receipt persistence, complete backup, destructive-copy restore semantics, wrong-chain rejection, corrupted archive rejection, and clean stop.

These processes share one physical host and loopback network. They are equivalent isolated homes for filesystem/key/process ownership, but they are not four separately operated physical computers and do not close the real-home hardware/network gate.

## Equivalent Isolated Container Hosts

Repeatable command:

```powershell
.\scripts\measure-desktop-container-hosts.ps1 -DurationSeconds 30 -Executable .\dist\chainlab-v0.1.0-rc2-linux-amd64\chainlab
```

Artifact: `output/desktop-measurements/20260713T132909Z-container-hosts-7211cdbc/measurement.json`

Artifact SHA-256: `e6bce7d65abd8f947698f6324d812d97dbf6cd2cad8b11aad09f91ddb0c0cd39`

The run used Docker Server 28.3.3 on Docker Desktop/WSL2 and immutable image `postgres@sha256:f992505e18f114c1e5102ac4dcf00f791b44462f6a423d899320f0bbf80e386f`. Each of four validators and one observer had a distinct container ID, IP address, network namespace, mount namespace, PID namespace, named data volume, read-only root filesystem, 256-process limit, four-CPU cgroup quota, and 8-GiB memory limit. All five shared one WSL2 kernel and one physical host.

| Metric | Result |
|---|---:|
| Four validators through committed height | 13.304 s |
| Initial validator peer counts | 3, 3, 3, 3 |
| Idle measured duration | 30 s |
| Height delta per validator | +6, +6, +7, +7 |
| Maximum cgroup memory peak | 56,754,176 bytes |
| Per-validator average cgroup memory | 33,432,497 to 46,746,703 bytes |
| CPU per validator as percent of its four-CPU quota | 1.058% to 1.25% |
| Per-validator `eth0` received bytes | 170,154 to 180,834 bytes |
| Per-validator `eth0` sent bytes | 152,106 to 208,452 bytes |
| Per-validator disk growth | 78,431 to 89,807 bytes |
| Observer sync/query/Explorer | 5.080 s; balance 300; `catching_up=false`; Explorer rendered |
| Observer signing material | no validator key; no validator state; local signing rejected |
| Clean stop and cleanup | 0 runtime files, containers, volumes, or networks remaining |

The fault flow committed an initial transfer, advanced two heights with 3-of-4, held both remaining validators at height 12 and an unchanged app hash for 12 seconds with 2-of-4, observed one transaction in the live mempool, committed it after restoring a third validator, restored the fourth validator, returned to peer counts 3/3/3/3, and directly observed a common height 14 block hash, app hash, and recipient balance 300. The public invitation passed a private-material field scan. The observer synced the final balance, served its loopback Explorer, reported `catching_up=false`, and failed closed when asked to sign locally; external raw broadcast remains covered by `TestHomeValidatorQuorumRecoveryAndObserverBroadcast` on the same shipped tree.

This closes the fixed-address equivalent-container-host, independent namespace/data ownership, cgroup-limit, quorum, process-isolated P2P-byte, and cleanup evidence slices. It does not prove four physical homes, independent kernels or power domains, Windows resource behavior, WAN/firewall/port-forwarding behavior, dynamic IP, sleep/resume, or interrupted host shutdown.

## RC2 Browser Evidence

The packaged Windows RC2 initialized and ran a real solo Desktop node, served Explorer on loopback, reported `catching_up=false`, and stopped with zero remaining ChainLab processes or Explorer/RPC listeners. Playwright used Microsoft Edge and the shipped Explorer at `http://127.0.0.1:8547/`.

| Viewport | Screenshot | SHA-256 | Width/console result |
|---|---|---|---|
| 1440x1000 | `output/playwright/rc2-c87e291-desktop-1440x1000.png` | `fc1c63dc229db3653e0b21c37a883762b9b58ce71bbff42678d1c7dd49607b79` | body/document 1440/1440; no overflowing elements; 0 errors, 0 warnings |
| 390x844 | `output/playwright/rc2-c87e291-mobile-390x844.png` | `0d43a52d1a3f9ae2c62649e6b34397c55a5ba61475adb8c81f1b656bd1e911a9` | body/document 375/375; no overflowing elements; 0 errors, 0 warnings |

Direct screenshot inspection found no blank page, overlap, clipped control, or unreadable long path/hash. The mobile layout wrapped the data path and both hashes within the viewport.

## RC2 24-Hour Soak Failure And Remediation

Formal attempt 2 used RC2 commit `c87e291c154cba89d979a4306d02ae9ce19b0e86`, release cadence 5 seconds, and run ID `20260713T140449Z-solo-941b42c9`. It is a failed run, not partial PASS evidence.

| Evidence | Result |
|---|---|
| Launch manifest | `output/desktop-measurements/soak-24h-rc2-c87e291-attempt2-launch.json`; SHA-256 `1c1b756b97317b0800af8263cd9bf2b96637ef70ba017900ee11425d005f5836` |
| Node log | `output/desktop-measurements/20260713T140449Z-solo-941b42c9/node-0.log`; SHA-256 `13560aed345dcb1281ddfe122833e04a9462607a97fe461c5867018957325e62` |
| Last committed height | 10,019 |
| Failure height and time | 10,020 at `2026-07-14T04:09:01.829Z` |
| Exact failure | Windows denied replacement of `priv_validator_state.json` while another process held the destination |
| Safety state | persisted destination remained prevote step 2; completed temporary file contained precommit step 3 |
| Misleading liveness | RPC and process remained alive after Comet stopped only its consensus state |

The remediation retains synchronous durability and does not disable `O_SYNC` safety as a performance workaround. ChainLab's compatible local signer writes, flushes, closes, retries only transient Windows rename conflicts, replaces, synchronizes the directory, and only then publishes its new in-memory HRS. Persistent replacement failure and Comet consensus panics now terminate the outer node instead of leaving a false-running RPC process. A real Windows lock-release test passes, a persistent-lock process test exits fail closed, and an accelerated single-validator run committed 10,000 blocks with about 30,000 durable signing-state replacements in 198.608 seconds. Its local artifact is `output/desktop-measurements/10000-blocks-durable-validator-20260714.json`, SHA-256 `fbed6c7e01ffe77b6287cc950df472b902aa53b165cd9f06d6e9eb7a7f69105b`.

This remediation evidence does not replace the failed release-cadence soak. A new immutable RC must run a fresh uninterrupted 24 hours before that gate can pass.

## RC3 Pre-Soak Release Evidence

RC3 was built from pushed commit `36739c9cff38fed66f6e44d391bc24185f61b0ad` after the full, short-race, vet, tidy, build, demo, link, and vulnerability gates passed. Windows used Go 1.26.5; a clean official Linux container used Go 1.25.12 and reported `all modules verified` before packaging.

| Artifact | SHA-256 |
|---|---|
| Windows amd64 `chainlab.exe` | `df750eba4614959a088254de3b687744c3f7ed835d235feab8351d00f2b5656e` |
| Windows amd64 zip | `715b3a381c500831dfc45d1e28fe66546a9d5f1c349f14e4341d0e7b463d911a` |
| Linux amd64 `chainlab` | `6c8d1aa561d0474df6c09dbb851b819d51d09d0dbc956c4b91309e06213dce96` |
| Linux amd64 tar.gz | `0a9c55c883f7aea5b36c85e6fb884c9778733a0c21aa451313d25c91f2c9511f` |

The packaged Windows executable initialized a new solo chain, reached `running` with `catching_up=false`, and served the real Explorer. Microsoft Edge checks at 1440x1000 and 390x844 found body/document widths within the viewport, zero overflowing elements, and zero console errors or warnings. Direct screenshot inspection found no blank content, overlap, clipping, or unreadable path/hash wrapping. Screenshot SHA-256 values are `0dfbd8f05d5c67c1ccfd785e47bb9269cb7abd01784334ec8b388c0fcadd9047` for desktop, `31026dbac5515b8ee2d4291b5ceae169f07f05f8a0b872ce64475b34c79bc8fe` for the mobile first viewport, and `6f5184ee650aef9e6571235faee57c9bf16be26a0dfcd90c2d23182a9b6f33a8` for the mobile long-field/footer viewport. Normal stop left zero process/listener/runtime entries, stderr remained empty, and the owned temporary root was removed.

The RC3 Linux package also passed a new five-container run, `20260714T060257Z-container-hosts-e6e55d61`; artifact SHA-256 `905c5d88547aaa2156f28636dd7693476a9cd01fda5b301a79aeacebbb0fd81f`. Four validators used distinct IP/network/mount/PID namespaces, named volumes, read-only roots, four-CPU quotas, and 8-GiB memory limits. Initial and restored peer counts were 3/3/3/3; 3-of-4 progressed, 2-of-4 held equal heights for 12 seconds with one queued transaction, recovery committed it, and all validators converged with recipient balance 300. The observer caught up, queried balance 300, rendered Explorer, contained no validator key/state, and rejected local signing. Cleanup recorded zero containers, volumes, networks, and runtime files.

These are RC3 packaging and short functional gates, not the open 24-hour, exact Windows reference-machine, physical-home WAN/power, or Windows sleep/interruption gates.

## Open Resource Gates

- repeat solo measurements on the exact 4-core/8-GiB/100-GiB-free-SSD Windows reference machine;
- a 24-hour release-cadence soak and a materially longer post-pruning run to characterize steady-state resource behavior;
- separately operated physical-home evidence, including WAN reachability, dynamic IP, firewall/port forwarding, and independent power domains;
- Windows sleep/resume and interrupted-host-shutdown recovery evidence;
- sustained RSS, log rotation, and state-sync resource results over materially longer durations.

Until these runs exist, no README or release note may claim the corresponding target or long-term behavior as proven.
