import { keccak256, stringToBytes, type Address, type Hex } from "viem";

/** Contract target and encoded calldata for one batch entry */
export interface ContractCall {
  to: Address;
  data: Hex;
}

/** Safe Transaction Builder format 1.0 with raw calldata and checksum metadata */
export interface SafeBatchFile {
  version: "1.0";
  chainId: string;
  createdAt: number;
  meta: {
    name: string;
    description: string;
    createdFromSafeAddress: string;
    checksum?: Hex;
  };
  transactions: { to: string; value: string; data: string }[];
}

// Serialize sorted object keys followed by values and commas; undefined becomes null
function serialize(json: unknown): string {
  const replacer = (_: string, value: unknown) =>
    value === undefined ? null : value;
  if (Array.isArray(json)) return `[${json.map(serialize).join(",")}]`;
  if (typeof json === "object" && json !== null) {
    const record = json as Record<string, unknown>;
    const keys = Object.keys(record).sort();
    let out = `{${JSON.stringify(keys, replacer)}`;
    for (const key of keys) out += `${serialize(record[key])},`;
    return `${out}}`;
  }
  return `${JSON.stringify(json, replacer)}`;
}

/** Computes the batch checksum with meta.name set to null and meta.checksum excluded */
export function batchChecksum(batch: object & { meta: object }): Hex {
  const meta: Record<string, unknown> = { ...batch.meta, name: null };
  delete meta.checksum;
  return keccak256(stringToBytes(serialize({ ...batch, meta })));
}

/** Builds ordered, zero-value calls with a checksum for the selected Safe and chain */
export function buildSafeBatch({
  chainId,
  safe,
  name,
  description,
  calls,
  createdAt = Date.now(),
}: {
  chainId: number;
  safe: Address;
  name: string;
  description: string;
  calls: ContractCall[];
  createdAt?: number;
}): SafeBatchFile {
  const batch: SafeBatchFile = {
    version: "1.0",
    chainId: String(chainId),
    createdAt,
    meta: { name, description, createdFromSafeAddress: safe },
    transactions: calls.map((c) => ({ to: c.to, value: "0", data: c.data })),
  };
  batch.meta.checksum = batchChecksum(batch);
  return batch;
}

/** Downloads a checksummed batch; verify its chain and selected Safe before importing */
export function exportSafeBatch(
  chainId: number,
  safe: Address,
  name: string,
  calls: ContractCall[],
): string {
  const batch = buildSafeBatch({
    chainId,
    safe,
    name,
    description: `${name}: ${calls.length} transaction(s) for Safe ${safe} on chain ${chainId}`,
    calls,
  });
  const fileName = `safe-batch-chain${chainId}-${name.toLowerCase().replace(/[^a-z0-9]+/g, "-")}-${batch.createdAt}.json`;
  const url = URL.createObjectURL(
    new Blob([JSON.stringify(batch, null, 2)], { type: "application/json" }),
  );
  const link = document.createElement("a");
  link.href = url;
  link.download = fileName;
  link.click();
  URL.revokeObjectURL(url);
  return `Saved ${fileName} (${calls.length} transaction(s) for Safe ${safe} on chain ${chainId}). Review it in Safe{Wallet}'s Transaction Builder, then have the owners sign and execute it.`;
}
