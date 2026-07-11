# ChainLab CometBFT Fault Network Evidence

Status date: 2026-07-11

## Scope

ChainLab's production path uses CometBFT v0.39.3 rather than the local PoA harness for Byzantine consensus. The real-process suite now fixes five equal-power four-validator availability boundaries:

- one validator unavailable: the remaining 3-of-4 commit transactions;
- the predicted round-0 proposer unavailable: the remaining 3-of-4 advance to a later round, commit, and converge after recovery;
- the predicted round-0 proposer connected but its proposal construction delayed beyond timeout: the full mesh remains intact while the network advances rounds and commits;
- two validators unavailable: 2-of-4 cannot advance consensus or committed application state;
- a symmetric 2+2 P2P topology partition: neither side commits, transaction gossip stays on its originating side, and healing converges without divergent application state.

These are Windows/amd64 development-network results, not public-network or hostile-infrastructure claims.

## Quorum-Loss Test

`TestFourValidatorQuorumLossHaltsAndRecovers` stops two Comet validator processes while leaving the two live RPC and application processes available. It waits for equal heights to stabilize, broadcasts a transaction, and requires both height and committed account nonce to remain unchanged across multiple 750 ms commit intervals.

Restoring the third validator must commit the queued transaction and converge three common-height block/application hashes and state roots. Restoring the fourth must rebuild the full peer mesh and four-node convergence.

## P2P Partition Test

`TestFourValidatorP2PPartitionHaltsAndHeals` first establishes a full-mesh common state, then stops all Comet processes and restarts them with canonical node documents defining exactly two disjoint persistent-peer pairs:

```text
node0 <-> node1    node2 <-> node3
```

PEX remains disabled. RPC `NetInfo` must report exactly the expected peer ID for every node, so a hidden cross-partition connection fails the test.

A transaction broadcast to node0 must appear in only the node0/node1 mempools. Node2/node3 must remain at zero unconfirmed transactions, and both sides must keep stable heights. Healing restores the full peer configuration and restarts node1 as a bridge. The isolated transaction must then propagate, commit, and converge all four nodes before the remaining processes are returned to a full mesh.

The partition/heal scenario passed five consecutive real-process runs. The final tree also passed the full unit and race suites, vet, dependency-tidiness diff, four production command builds, height-18 demo, formatting/diff checks, and fixed `govulncheck` v1.6.0 with zero reachable vulnerabilities.

## Missing-Proposer Test

`TestFourValidatorMissingProposerAdvancesRoundAndRecovers` reconstructs Comet's complete validator set from the RPC validator priorities, checks the reconstruction against the current block proposer, and predicts the next two round-0 proposers. It stops the validator predicted for height `H+2` before that height, then broadcasts a transaction through the remaining 3-of-4 network.

Height `H+1` must be produced by its predicted round-0 proposer. At height `H+2`, the offline validator must not produce the block and the commit round must be greater than zero, proving that Comet timed out the missing proposal and advanced rounds while retaining quorum. The three live nodes continue through `H+3`; restarting the target validator must rebuild the full peer mesh and converge all four nodes on common block/application hashes, state root, and sender nonce.

The missing-proposer scenario passed five consecutive real-process runs. It proves round advancement after a targeted validator process outage, not packet-delay or one-way-reachability behavior.

## Delayed Connected Proposer Test

`TestFourValidatorDelayedConnectedProposerAdvancesRound` keeps every validator, Comet process, application process, and P2P connection online. A test-only ABCI wrapper delays exactly one predicted `PrepareProposal(H+2)` response for two seconds, beyond the configured 500 ms round-0 propose timeout.

The full peer mesh must remain present. Height `H+1` must use its predicted proposer; height `H+2` must be produced by a different validator with commit round greater than zero. The transaction must commit and all four nodes must converge. The scenario passed five consecutive runs. This covers validator-local proposal-construction latency, not packet delay or asymmetric network reachability.

## Evidence Boundary

The tests cover process outage, a targeted missing round-0 proposer, validator-local delayed proposal construction while connected, and a symmetric disjoint P2P topology. Separate V2 lifecycle networks cover simultaneous evidence-driven power-zero removals and a Comet-verified same-height light-client equivocation proof. They do not emulate dynamic packet delay, drop, duplication, corruption, or reordering; bandwidth exhaustion; asymmetric one-way reachability; forward/lunatic or amnesia light-client cases; simultaneous admission or non-removal power changes; sentry topology; or sustained load. Those remain production gates and require a controllable network-fault environment rather than relabeling application delay or process shutdown as a packet-level fault.
