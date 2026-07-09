# Ethereum Type-2 Raw Transactions Design

## Goal

Add a first slice of Ethereum raw transaction compatibility to ChainLab by accepting EIP-2718 / EIP-1559 type-2 raw transactions through existing raw transaction submission paths.

This slice covers signed Ethereum type-2 value transfers only:

- transaction envelope starts with `0x02`
- payload is RLP-encoded EIP-1559 fields
- `to` is a 20-byte address
- `data` is empty
- `access_list` is empty
- fee fields map to ChainLab `max_fee_per_gas` and `max_priority_fee_per_gas`

## References

- EIP-2718 defines the typed transaction envelope as `TransactionType || TransactionPayload`, where the payload semantics are defined by the transaction type.
- EIP-1559 defines type `0x02`, its RLP payload fields, and the signature hash as Keccak over `0x02 || rlp(unsigned_payload)`.

## Recommended Approach

Keep ChainLab's native raw transaction format, but teach the raw decoder to detect Ethereum type-2 raw transactions when the first decoded byte is `0x02`.

For Ethereum type-2 raw transactions:

1. Decode the RLP payload.
2. Validate the payload has exactly the EIP-1559 field shape.
3. Restrict this first slice to plain transfers: empty `data`, empty `access_list`, and non-empty 20-byte destination.
4. Rebuild the EIP-1559 signing payload and recover the sender from `signature_y_parity`, `signature_r`, and `signature_s`.
5. Translate into a ChainLab `transfer` transaction with a signature kind that tells the executor to validate the Ethereum type-2 signing digest instead of ChainLab-native signing bytes.
6. Preserve the Ethereum raw transaction hash as the transaction hash returned by `eth_sendRawTransaction`, txpool lookups, receipts, and block indexes for that transaction.

The executor remains deterministic because every field required to reconstruct and verify the Ethereum signing digest is committed in the translated ChainLab transaction.

## Alternatives Considered

1. Support type-2 transfer only.
   - Best next step because ChainLab already has EIP-1559 fee fields and transfer execution.
   - Keeps signature verification honest.
   - Avoids pretending ChainLab can execute arbitrary EVM calldata.

2. Support full type-2 calldata immediately.
   - More compatible with Ethereum wallets and Solidity tooling.
   - Requires broader EVM ABI/calldata execution semantics and an EVM runtime boundary that ChainLab does not have.
   - Too large for one verified slice.

3. Decode type-2 raw transactions but re-sign or wrap them as ChainLab-native transactions.
   - Avoids changing executor signature validation.
   - Incorrect security model because the submitted Ethereum signature would not be the signature validated during consensus replay.

The selected approach is option 1.

## Data Model

Add explicit transaction metadata:

- `signature_kind`: empty for ChainLab-native signatures, `ethereum.type2` for translated EIP-1559 transactions.
- `ethereum_raw_hash`: the Keccak hash of the submitted raw bytes.

For `signature_kind == "ethereum.type2"`, `Transaction.Hash()` returns `ethereum_raw_hash`. Native transactions keep their current hash behavior.

## Chain ID Mapping

ChainLab currently exposes `chainlab-local` as Ethereum chain number `31337` through `eth_chainId`.

For this slice:

- Ethereum type-2 raw `chain_id == 31337` maps to internal ChainLab `chain_id == "chainlab-local"`.
- Other chain ids decode to a hex chain id string and will only execute if the node is configured with that same internal string.
- A later config pass can introduce a first-class reversible `evm_chain_id` field for custom networks.

## Signature Verification

Native transactions keep the existing `crypto.Verify(tx.From, tx.SigningBytes(), tx.Signature)` path.

Ethereum type-2 transactions use:

```text
keccak256(0x02 || rlp([
  chain_id,
  nonce,
  max_priority_fee_per_gas,
  max_fee_per_gas,
  gas_limit,
  destination,
  amount,
  data,
  access_list
]))
```

The executor recovers the public key from the compact form of `signature_y_parity`, `signature_r`, and `signature_s`, derives the address, and compares it to `tx.From`.

## Raw Submission Paths

Both existing raw submission paths must use the new decoder:

- JSON-RPC `eth_sendRawTransaction`
- REST `POST /tx/raw`

Existing ChainLab-native raw transactions must continue to round-trip unchanged.

## Boundaries

- No legacy Ethereum RLP transaction support yet.
- No type-1 access-list support yet.
- No contract creation from Ethereum raw tx yet.
- No non-empty calldata execution yet.
- No non-empty access list support yet.
- No claim of full EVM transaction compatibility.

## Testing

Use TDD:

1. Add a test that builds and signs an Ethereum type-2 transfer raw tx for chain id `31337`.
2. Confirm `eth_sendRawTransaction` initially fails because the raw payload is not ChainLab JSON.
3. Implement RLP type-2 decode, sender recovery, translation, and executor verification.
4. Verify the raw tx enters the txpool, returns the Ethereum raw transaction hash, produces a block, and transfers value.
5. Keep existing native raw transaction tests green.
6. Run full repository verification before commit.
