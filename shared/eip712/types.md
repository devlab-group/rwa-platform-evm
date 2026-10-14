# EIP-712 canonical type strings

The Solidity contracts, Go signer, Go server and TypeScript client MUST use these
canonical strings byte for byte.

## Domain

```
name:              "RWA-Supply-Attestation"
version:           "1"
chainId:           <current chain id>
verifyingContract: <SupplyController address>
```

`EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)`

## MintAttestation

Canonical type string (used for the type hash):

```
MintAttestation(address auditor,bytes32 profileDigest,bytes32 recordKey,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)
```

`MINT_ATTESTATION_TYPEHASH = keccak256(<string above>)`; the value is in
`shared/vectors/typehashes.json`.

Field order (must not change):

1. `address auditor`
2. `bytes32 profileDigest`
3. `bytes32 recordKey`
4. `bytes32 metadataDigest`
5. `uint256 amount`
6. `uint256 nonce`
7. `uint64  validUntil`
8. `address vault`

## BurnAttestation

```
BurnAttestation(address auditor,bytes32 profileDigest,bytes32 operationId,bytes32 metadataDigest,uint256 amount,uint256 nonce,uint64 validUntil,address vault)
```

`BURN_ATTESTATION_TYPEHASH = keccak256(<string above>)`; the value is in
`shared/vectors/typehashes.json`.

Field order:

1. `address auditor`
2. `bytes32 profileDigest`
3. `bytes32 operationId`
4. `bytes32 metadataDigest`
5. `uint256 amount`
6. `uint256 nonce`
7. `uint64  validUntil`
8. `address vault`

## Digest construction

```
digest = keccak256(0x1901 ‖ domainSeparator ‖ hashStruct(message))
domainSeparator = keccak256(abi.encode(
    keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"),
    keccak256("RWA-Supply-Attestation"),
    keccak256("1"),
    chainId,
    verifyingContract))
hashStruct(m) = keccak256(abi.encode(TYPEHASH, m.field1, ..., m.fieldN))
```

`uint64 validUntil` is ABI-encoded as a full 32-byte word (left-padded).

## Definitions

- `recordKey = keccak256(bytes(recordId))`: one mint per record key.
- `operationId`: one burn per operation ID; the contract rejects reuse.
- `metadataDigest` / `profileDigest`: SHA-256 of the RFC 8785 canonical bytes, not keccak. This
  matches the IPFS raw/sha2-256 CID convention. Only the EIP-712 struct and domain hashing uses
  keccak256.
- `nonce`: random 256-bit value; mint and burn share one nonce namespace.
- `validUntil`: mandatory unix seconds. The signer rejects a deadline that is not in the future or
  is later than its policy's maximum lifetime (default 30 days) after the earlier of the signing
  time and the metadata `createdAt`.

## Safe multisig auditor

For a Safe auditor, set `auditor` to the Safe address and compute the attestation digest as usual.
With Safe v1.3.0+ and `CompatibilityFallbackHandler`, each owner signs this Safe message hash:

```
safeDomainSeparator = keccak256(abi.encode(
    keccak256("EIP712Domain(uint256 chainId,address verifyingContract)"),
    chainId,
    safe))
safeMessageHash = keccak256(0x19 ‖ 0x01 ‖ safeDomainSeparator ‖ keccak256(abi.encode(
    keccak256("SafeMessage(bytes message)"),
    keccak256(abi.encode(digest)))))
```

`abi.encode(digest)` is the digest's 32 bytes. Owners sign `safeMessageHash` directly, without a
personal-message prefix, using `v` in {27,28}. Pass at least `threshold` 65-byte owner signatures
to `mint`/`burn`, concatenated in ascending owner-address order. The Safe checks its current
owners and threshold. Golden values are in `shared/vectors/safe-auditor.json`; the
[signer reference](../../signer/README.md#safe-multisig-auditor) links the Safe implementation.
