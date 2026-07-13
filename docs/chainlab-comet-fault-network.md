# ChainLab CometBFT Fault Network Evidence

Status date: 2026-07-13

## Scope

ChainLab's production path uses CometBFT v0.39.3 rather than the local PoA harness for Byzantine consensus. The real-process suite now fixes six equal-power four-validator availability and proposal-validity boundaries:

- one validator unavailable: the remaining 3-of-4 commit transactions;
- the predicted round-0 proposer unavailable: the remaining 3-of-4 advance to a later round, commit, and converge after recovery;
- the predicted round-0 proposer connected but its proposal construction delayed beyond timeout: the full mesh remains intact while the network advances rounds and commits;
- the predicted round-0 proposer returns a proposal containing a malformed transaction: every application rejects it through `ProcessProposal`, a different proposer commits at a later round, and the valid mempool transaction remains available;
- two validators unavailable: 2-of-4 cannot advance consensus or committed application state;
- a symmetric 2+2 P2P topology partition: neither side commits, transaction gossip stays on its originating side, and healing converges without divergent application state.

Separate lifecycle/evidence networks also exercise certified runtime admission, same-height light-client equivocation, and cross-height forward-lunatic proofs through the real Comet process/RPC boundary. These are Windows/amd64 development-network results, not public-network or hostile-infrastructure claims.

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

## Invalid Proposal Test

`TestFourValidatorInvalidProposalAdvancesRound` keeps all four validators and the complete peer mesh online. A test-only ABCI wrapper targets the predicted round-0 proposer at height `H+2`, first lets the real ChainLab application build its proposal, and then appends one malformed transaction. Consuming the one-shot trigger file proves that the target application actually injected the invalid payload.

Height `H+1` must still use its predicted proposer. The targeted proposer must not produce the committed block at `H+2`, and the commit round must be greater than zero. The valid transaction broadcast before the fault must remain available, commit successfully, and converge all four block/application hashes, state roots, and sender nonces. The scenario passed five consecutive real-process runs. The final tree also passed the full unit and race suites, vet, dependency-tidiness diff, four production command builds, height-18 demo, formatting/diff checks, and fixed `govulncheck` v1.6.0 with zero reachable vulnerabilities. It proves application-level proposal rejection and consensus round recovery; it does not prove packet corruption handling or arbitrary Byzantine proposer behavior.

## Runtime Admission Test

`TestFourValidatorV2RuntimeAdmission` funds and stakes a locally generated candidate, then stops two validators to prove the 2-of-4 network cannot commit the candidate's certified join. The certificate binds the last committed validator root and height and is signed by all four active test validators. Restoring a third validator commits the join and returns the candidate's power-one update at `H+2`; restoring the fourth keeps four of five voting-power units online, strictly above the liveness threshold. All four applications and Comet validator-set queries must converge before a later transaction commits.

This proves one non-removal validator-set expansion across the application/socket/Comet process boundary. It does not cover simultaneous admissions, re-entry, runtime power changes, admission during a partition, or operation of the newly admitted validator process.

## Evidence Boundary

The tests cover process outage, a targeted missing round-0 proposer, validator-local delayed proposal construction while connected, application rejection of a malformed proposal, and a symmetric disjoint P2P topology. Separate V2 lifecycle networks cover simultaneous evidence-driven power-zero removals, a Comet-verified same-height light-client equivocation proof, and a cross-height forward-lunatic proof. The forward-lunatic test uses common height `H`, an invalid conflicting application hash at `H+1`, and waits until the full nodes have committed `H+2`; three historical validators sign the conflicting header, Comet derives those signers as Byzantine, and the application removes them before proving post-transition liveness and convergence. It does not provide an externally operated light-client detector or test a conflicting height ahead of the full node's local block store.

The suite does not emulate dynamic packet delay, drop, duplication, corruption, or reordering; bandwidth exhaustion; asymmetric one-way reachability; amnesia light-client evidence; simultaneous admission, re-entry, or non-removal power changes; sentry topology; or sustained load. Those remain production gates and require a controllable network-fault environment rather than relabeling application delay, proposal-content injection, or process shutdown as a packet-level fault.
