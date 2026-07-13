# ChainLab V2 Validator Lifecycle

Status date: 2026-07-11

## Scope

`chainlab-v2` adds an explicitly versioned CometBFT evidence-to-slashing path and epoch-bound validator removal. Genesis-certified admission can derive initial power from stake, and a validator policy may explicitly activate delayed unbonding after evidence removal. It does not enable runtime `validator.join`, voluntary `validator.leave`, re-entry, runtime power changes, rewards, or governance-controlled set changes. Those operations remain fail closed until a separately activated transition protocol and economics specification exist.

`chainlab-v1` keeps its fixed-genesis-set semantics and app-hash encoding. A v1 genesis cannot carry a v2 validator policy or lifecycle state.

## Genesis Policy

A v2 genesis commits the epoch length, duplicate-vote and light-client-attack slash basis points, and evidence maximum age in both blocks and nanoseconds. `InitChain` requires the CometBFT evidence retention values and application version to exactly match the committed policy. Genesis validator updates must be compressed secp256k1 keys with equal power 1 and exactly match the ChainLab validator accounts.

## Persistent Identity

At `InitChain`, every genesis validator is permanently bound to its canonical ChainLab account, 20-byte CometBFT consensus address, compressed secp256k1 public key, voting power, and inclusive active/exclusive inactive heights.

The bindings, offence tombstones, slash results, and scheduled removal heights are consensus state. They are included in the state root, v2 validator root, Pebble version manifests, application snapshots, startup validation, and state-sync validation. V2-V4 tombstones have no evidence timestamp and therefore remain permanent; V5 safely compacts only newly timestamped offences after both retention dimensions expire.

## Evidence Validation

CometBFT verifies the original duplicate-vote or light-client-attack proof before exposing `Misbehavior` to ABCI. The application independently validates the deterministic fields it receives: supported type, known identity, historical activity and voting power, past height, non-future time, one record per validator-height in a block, and the genesis-bound retention rule.

The retention rule matches CometBFT v0.39.3: evidence expires only when both its block age and duration age exceed their respective maxima.

## State Transition Order

For prepare, process, and finalize, evidence is applied to a detached copy of the last committed state before transaction simulation. A same-block unstake or transfer therefore cannot escape an already authenticated penalty. The decided block is published only after the synchronized Pebble commit succeeds.

The one-time tombstone key is the ChainLab validator account plus offence height, independent of proof ordering or evidence type. Repeated delivery remains committed in the evidence root and emits a repeat outcome, but cannot slash stake or schedule removal again.

The slash amount is:

```text
ceil(current_stake * slash_basis_points / 10000)
```

Quotient/remainder arithmetic avoids `uint64` multiplication overflow.

## Epoch And CometBFT Timing

For epoch length `L`, epoch starts are heights `1`, `L+1`, `2L+1`, and so on. Evidence observed while finalizing height `H` schedules removal at the first epoch start `T` for which `T >= H+2`.

CometBFT v0.39.3 applies validator updates returned from `FinalizeBlock(H)` at height `H+2`. ChainLab therefore returns the power-zero update exactly at `T-2`. The validator remains eligible through `T-1` and is rejected as proposer or vote-extension validator from `T` onward.

Updates are sorted by consensus address. Multiple authenticated offences may schedule multiple validators for the same epoch boundary; ChainLab returns the complete deterministic power-zero batch at `T-2`. A transition that would leave no active validator fails closed instead of returning an invalid empty CometBFT validator set.

## Evidence

Automated coverage proves deterministic direct-ABCI slashing, tombstone replay, policy binding, retention, epoch scheduling, `H+2` activation, inactive-proposer rejection, Pebble restart, and snapshot/state-sync recovery.

A real four-validator process test constructs two correctly signed conflicting votes, broadcasts the resulting evidence through Comet RPC, lets CometBFT verify and propagate it, and verifies that all applications slash the same stake and converge from four active validators to three.

A second real-process test constructs valid duplicate-vote evidence for two different validators. Both offences schedule the same epoch boundary; RPC `BlockResults` must expose exactly two sorted power-zero updates at one height, and every node must observe the validator set change atomically from four to two. The remaining two validators then commit a transaction while all four full nodes converge on common block/application hashes, state root, and account nonce. This remains live because those two validators hold all voting power in the newly active set; it does not contradict the separate result that two online validators cannot make quorum while the active set still contains four. This proves simultaneous evidence-driven removals, not general admission, voting-power changes, or governance-controlled membership.

A third real-process test builds a conflicting light block at a canonical historical height and signs its precommit with three validators. Comet RPC and the evidence pool verify the conflicting header, validator hash, commit signatures, common height, total voting power, and derived Byzantine validators before ABCI receives the evidence. All three signers are slashed and removed at one epoch boundary; the remaining validator commits a transaction while all four full nodes converge. This covers Comet's same-height light-client equivocation evidence path, not production trusted-header acquisition or an externally operated light-client service.

A fourth real-process test uses a canonical common height `H` and constructs a conflicting block at `H+1` with an invalid application hash. Three of the four historical validators sign the cross-height header. After the full nodes have committed `H+2`, Comet verifies the forward-lunatic proof against the common and trusted commits, derives the three Byzantine validators, and passes their authenticated misbehavior to ABCI. The same epoch removal and post-transition transaction convergence checks then pass. This proves the on-node cross-height verification and punishment path; external light-client detection, evidence sourcing, and an attack height ahead of the full node remain separate work.

## Remaining Lifecycle Work

Protocol V2 genesis may carry ordered `chainlab-validator-admission-v1` certificates. Each certificate binds the chain ID, candidate account, compressed secp256k1 key, Comet consensus address, voting power, and epoch-aligned activation height, and requires canonical signatures from at least two thirds plus one of the genesis validator accounts. Admission power is not discretionary: `chainlab-validator-power-v1` derives `floor(genesis_stake / 1000)`, requires at least 1,000 stake, and rejects a mismatched certificate. InitChain writes only verified candidates into the lifecycle; certificate tampering, cross-chain replay, minority approval, duplicate candidates/signers, unknown signers, insufficient stake, power mismatch, and non-epoch activation fail closed. Once an identity enters the lifecycle, ordinary unstake produces an included failure while it is active.

`ValidatorPolicy.unbonding_epochs` is an explicit compatibility-preserving activation. Zero retains the previous permanent lock and does not alter existing V2-V5 canonical genesis or state roots. Values from 2 through 1,000 schedule `UnbondingHeight = RemovalHeight + UnbondingEpochs*EpochLength` when new Comet-authenticated evidence removes a validator. Stake remains locked before that inclusive height and can be withdrawn from that height onward. Old persisted removals without an unbonding height remain permanently locked instead of receiving a retroactive shorter safety window. Voluntary leave and re-entry remain disabled.

When delayed unbonding is enabled, each `chainlab.validator_slash` event includes an indexed `unbonding_height` for both the first accepted offence and repeated evidence. Legacy policies omit the attribute entirely, preserving their deterministic event/result bytes.

The lifecycle state schema retains certified non-genesis identities while requiring every genesis identity to remain present at activation height 1. Identity count, account, compressed public key, consensus address, voting power, uniqueness, and epoch-aligned admission/removal constraints remain enforced during proposal execution and persistent restore, and snapshot restore preserves the same validator root. The ABCI lifecycle emits each certified identity's deterministic positive-power update two heights before activation, alongside any zero-power removals. Proposal and vote-extension identity checks then resolve the active lifecycle instead of the frozen genesis map, so an activated validator is accepted only inside its recorded interval. No V1-V5 transaction can create or authorize an identity at runtime.

Production lifecycle completion still requires a separately activated runtime admission/re-entry protocol, runtime power changes, voluntary-leave unbonding, epoch transition records, reward accounting, evidence economics, remote signer/HSM protection, key rotation, quorum and emergency rules, governance/upgrade authority, partitions and rolling-upgrade tests, and adversarial economic simulation. V5 closes forward tombstone compaction; legacy tombstones deliberately remain permanent because their evidence time was never committed.
