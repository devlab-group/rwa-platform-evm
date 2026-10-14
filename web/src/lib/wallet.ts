// Build wallet writes and Safe-batch calldata from pinned ABI fragments in lib/abis.ts.
// The console does not submit server-supplied calldata.
import {
  createPublicClient,
  createWalletClient,
  custom,
  decodeErrorResult,
  defineChain,
  encodeFunctionData,
  erc20Abi,
  type Address,
  type Chain,
  type Hex,
} from "viem";
import {
  accessControlAdminAbi,
  erc7943Abi,
  rwaTokenErrorsAbi,
  fixedPriceStrategyAbi,
  pausableAbi,
  redemptionEscrowAbi,
  rwaFactoryAbi,
  supplyControllerAbi,
  vaultAbi,
} from "./abis";
import { findNetwork, toAddChainParams } from "./networks";
import type { ContractCall } from "./safeBatch";

export interface InjectedProvider {
  request: (args: { method: string; params?: unknown[] }) => Promise<unknown>;
  on?: (event: string, handler: (...args: unknown[]) => void) => void;
  removeListener?: (
    event: string,
    handler: (...args: unknown[]) => void,
  ) => void;
}

declare global {
  interface Window {
    ethereum?: InjectedProvider;
  }
}

export function getInjectedProvider(): InjectedProvider | undefined {
  if (typeof window === "undefined") return undefined;
  return window.ethereum;
}

/**
 * Reports a failed expected-chain check.
 * The caller must switch chains and retry; writes do not silently retarget.
 */
export class ChainMismatchError extends Error {
  constructor(
    public readonly expectedChainId: number,
    public readonly actualChainId: number,
  ) {
    super(
      `Wallet is connected to chain ${actualChainId}, but this project requires chain ${expectedChainId}. Switch networks in your wallet and try again.`,
    );
    this.name = "ChainMismatchError";
  }
}

async function getInjectedChainId(provider: InjectedProvider): Promise<number> {
  const chainIdHex = (await provider.request({
    method: "eth_chainId",
  })) as string;
  return Number(chainIdHex);
}

/**
 * Reads the injected wallet's chain ID without requesting account access,
 * including during Setup before a project chain is configured
 */
export async function readInjectedChainId(): Promise<number> {
  const provider = getInjectedProvider();
  if (!provider)
    throw new Error(
      "No injected wallet found (e.g. MetaMask). Connect a wallet on the deployment chain to read the quote token's decimals.",
    );
  return getInjectedChainId(provider);
}

// Requires the injected wallet to be on expectedChainId
export async function assertChain(
  provider: InjectedProvider,
  expectedChainId: number,
): Promise<void> {
  const actual = await getInjectedChainId(provider);
  if (actual !== expectedChainId)
    throw new ChainMismatchError(expectedChainId, actual);
}

// projectChain supplies the expected chain while transport uses the injected provider
function projectChain(chainId: number): Chain {
  return defineChain({
    id: chainId,
    name: `project-chain-${chainId}`,
    nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 },
    rpcUrls: { default: { http: [] } },
  });
}

export interface ConnectedWallet {
  address: Address;
  chainId: number;
}

export async function connectWallet(): Promise<ConnectedWallet> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found (e.g. MetaMask).");
  const client = createWalletClient({ transport: custom(provider) });
  const [address] = await client.requestAddresses();
  const chainId = await client.getChainId();
  return { address, chainId };
}

// isChainNotAddedError finds code 4902 directly or under data.originalError
function isChainNotAddedError(err: unknown): boolean {
  if (typeof err !== "object" || err === null) return false;
  const top = (err as { code?: unknown }).code;
  if (top === 4902) return true;
  const nested = (err as { data?: { originalError?: { code?: unknown } } }).data
    ?.originalError?.code;
  return nested === 4902;
}

/**
 * Requests a switch to expectedChainId. On 4902, add a known network from lib/networks.ts
 * and retry; unknown networks and other errors propagate.
 */
export async function requestSwitchChain(
  expectedChainId: number,
): Promise<void> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  const chainIdHex = `0x${expectedChainId.toString(16)}`;
  try {
    await provider.request({
      method: "wallet_switchEthereumChain",
      params: [{ chainId: chainIdHex }],
    });
  } catch (err) {
    if (!isChainNotAddedError(err)) throw err;
    const network = findNetwork(expectedChainId);
    if (!network) throw err; // not something we know how to add
    await provider.request({
      method: "wallet_addEthereumChain",
      params: [toAddChainParams(network)],
    });
    // Request the switch explicitly after adding the chain
    await provider.request({
      method: "wallet_switchEthereumChain",
      params: [{ chainId: chainIdHex }],
    });
  }
}

export async function signMessage(
  address: Address,
  message: string,
): Promise<Hex> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  const client = createWalletClient({
    account: address,
    transport: custom(provider),
  });
  return client.signMessage({ account: address, message });
}

/** Reads ERC-20 decimals through the injected provider after checking its chain */
export async function readErc20Decimals(
  expectedChainId: number,
  token: Address,
): Promise<number> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  await assertChain(provider, expectedChainId);
  const client = createPublicClient({ transport: custom(provider) });
  return client.readContract({
    address: token,
    abi: erc20Abi,
    functionName: "decimals",
  });
}

/** Reads token allowance for owner and spender on the expected chain */
export async function readErc20Allowance(
  expectedChainId: number,
  token: Address,
  owner: Address,
  spender: Address,
): Promise<bigint> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  await assertChain(provider, expectedChainId);
  const client = createPublicClient({ transport: custom(provider) });
  return client.readContract({
    address: token,
    abi: erc20Abi,
    functionName: "allowance",
    args: [owner, spender],
  });
}

/**
 * Reads the Vault purchase quote, from RWA minimal units to quote-token minimal units.
 * The price may change before execution; buy enforces its separate maxQuoteAmount.
 */
export async function readPreviewBuy(
  expectedChainId: number,
  vault: Address,
  tokenAmount: bigint,
): Promise<bigint> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  await assertChain(provider, expectedChainId);
  const client = createPublicClient({ transport: custom(provider) });
  return client.readContract({
    address: vault,
    abi: vaultAbi,
    functionName: "previewBuy",
    args: [tokenAmount],
  });
}

/**
 * Waits for a successful receipt and throws for a mined revert.
 * Approval-dependent calls must wait for that success before spending.
 */
export async function waitForTxReceipt(hash: Hex): Promise<void> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  const client = createPublicClient({
    transport: custom(provider),
    pollingInterval: 250,
  });
  const receipt = await client.waitForTransactionReceipt({
    hash,
    timeout: 60_000,
  });
  if (receipt.status !== "success") {
    throw new Error(
      `Transaction ${hash} was mined but reverted on-chain. Nothing changed.`,
    );
  }
}

/**
 * Decodes known token revert data into operator messages.
 * Raw-calldata writes have no attached ABI; unavailable or unlisted data falls back
 * to the wallet message, including user rejection and RPC failures.
 */
export function describeWalletError(err: unknown): string {
  const data = revertData(err);
  if (data) {
    try {
      const decoded = decodeErrorResult({ abi: rwaTokenErrorsAbi, data });
      const named = explainTokenError(decoded.errorName, decoded.args);
      if (named) return named;
    } catch {
      // Fall back when this ABI cannot decode the revert
    }
  }
  return err instanceof Error ? err.message : "Transaction failed.";
}

// revertData searches data fields through at most six cause levels
function revertData(err: unknown): Hex | undefined {
  let current: unknown = err;
  for (let depth = 0; current && depth < 6; depth++) {
    const candidate = (current as { data?: unknown }).data;
    if (
      typeof candidate === "string" &&
      candidate.startsWith("0x") &&
      candidate.length >= 10
    ) {
      return candidate as Hex;
    }
    current = (current as { cause?: unknown }).cause;
  }
  return undefined;
}

// explainTokenError describes recognized token errors
function explainTokenError(
  name: string,
  args: readonly unknown[] | undefined,
): string | undefined {
  const first = args?.[0] as string | undefined;
  switch (name) {
    case "ERC7943CannotReceive":
    case "RecipientNotAllowed":
      return `The recipient ${first} is not Allowed in the compliance registry, so the token refused the transfer.`;
    case "SenderNotAllowed":
      return `${first} is not Allowed in the compliance registry and cannot send.`;
    case "ERC7943InsufficientUnfrozenBalance":
      return `${first} does not have that many unfrozen tokens: ${String(args?.[2])} of their balance is movable (in minimal units).`;
    case "SystemAddressCannotBeFrozen":
      return `${first} is the Vault or the redemption escrow, which can never be frozen.`;
    case "SystemAddressCannotBeSeized":
      return `${first} is the Vault or the redemption escrow, which can never be seized from.`;
    case "ForcedTransferToSelf":
      return "A forced transfer must have different sender and recipient.";
    case "ZeroAddress":
      return "The zero address is not a valid party to this call.";
    case "AccessControlUnauthorizedAccount":
      return `${first} does not hold the role this call requires.`;
    default:
      return undefined;
  }
}

/** Sends ERC-20 approve from the connected wallet on the expected chain */
export async function sendErc20Approve(
  expectedChainId: number,
  owner: Address,
  token: Address,
  spender: Address,
  amount: bigint,
): Promise<Hex> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  const chain = projectChain(expectedChainId);
  const client = createWalletClient({
    account: owner,
    chain,
    transport: custom(provider),
  });
  // Pass the expected chain so viem checks identity before submission
  return client.sendTransaction({
    account: owner,
    chain,
    ...erc20ApproveCall(token, spender, amount),
  });
}

/** ERC-20 approve(spender, amount) call */
export function erc20ApproveCall(
  token: Address,
  spender: Address,
  amount: bigint,
): ContractCall {
  return {
    to: token,
    data: encodeFunctionData({
      abi: erc20Abi,
      functionName: "approve",
      args: [spender, amount],
    }),
  };
}

// sendWrite checks chain identity and submits encoded calldata from the wallet;
// contracts enforce privileged-call authorization
async function sendWrite(
  expectedChainId: number,
  from: Address,
  to: Address,
  data: Hex,
): Promise<Hex> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  const chain = projectChain(expectedChainId);
  const client = createWalletClient({
    account: from,
    chain,
    transport: custom(provider),
  });
  return client.sendTransaction({ account: from, chain, to, data });
}

function sendCall(
  expectedChainId: number,
  from: Address,
  call: ContractCall,
): Promise<Hex> {
  return sendWrite(expectedChainId, from, call.to, call.data);
}

/** Encodes RWAToken pause or unpause */
export function pauseCall(token: Address, pause: boolean): ContractCall {
  return {
    to: token,
    data: encodeFunctionData({
      abi: pausableAbi,
      functionName: pause ? "pause" : "unpause",
    }),
  };
}

/** Submits the PAUSER_ROLE pause or unpause action */
export async function sendSetPaused(
  expectedChainId: number,
  from: Address,
  token: Address,
  pause: boolean,
): Promise<Hex> {
  return sendCall(expectedChainId, from, pauseCall(token, pause));
}

/** Encodes a purchase or redemption price update */
export function strategyPriceCall(
  strategy: Address,
  side: "purchase" | "redemption",
  priceMinimalUnits: bigint,
): ContractCall {
  return {
    to: strategy,
    data: encodeFunctionData({
      abi: fixedPriceStrategyAbi,
      functionName:
        side === "purchase" ? "setPurchasePrice" : "setRedemptionPrice",
      args: [priceMinimalUnits],
    }),
  };
}

/** Submits a PRICER_ROLE price update in quote-token minimal units per whole RWA token */
export async function sendSetStrategyPrice(
  expectedChainId: number,
  from: Address,
  strategy: Address,
  side: "purchase" | "redemption",
  priceMinimalUnits: bigint,
): Promise<Hex> {
  return sendCall(
    expectedChainId,
    from,
    strategyPriceCall(strategy, side, priceMinimalUnits),
  );
}

/**
 * Submits an admin freeze replacement in absolute RWA minimal units; zero releases it.
 * The contract rejects positive freezes of Vault or RedemptionEscrow.
 */
export async function sendSetFrozenTokens(
  expectedChainId: number,
  from: Address,
  token: Address,
  account: Address,
  amountMinimalUnits: bigint,
): Promise<Hex> {
  return sendCall(
    expectedChainId,
    from,
    setFrozenTokensCall(token, account, amountMinimalUnits),
  );
}

/** Encodes an absolute frozen-amount replacement; see sendSetFrozenTokens */
export function setFrozenTokensCall(
  token: Address,
  account: Address,
  amountMinimalUnits: bigint,
): ContractCall {
  return {
    to: token,
    data: encodeFunctionData({
      abi: erc7943Abi,
      functionName: "setFrozenTokens",
      args: [account, amountMinimalUnits],
    }),
  };
}

/**
 * Submits an admin forced transfer without holder approval, bypassing sender eligibility
 * and pause while requiring a compliant recipient
 */
export async function sendForcedTransfer(
  expectedChainId: number,
  sender: Address,
  token: Address,
  holder: Address,
  recipient: Address,
  amountMinimalUnits: bigint,
): Promise<Hex> {
  return sendCall(
    expectedChainId,
    sender,
    forcedTransferCall(token, holder, recipient, amountMinimalUnits),
  );
}

/** Encodes forcedTransfer; see sendForcedTransfer */
export function forcedTransferCall(
  token: Address,
  holder: Address,
  recipient: Address,
  amountMinimalUnits: bigint,
): ContractCall {
  return {
    to: token,
    data: encodeFunctionData({
      abi: erc7943Abi,
      functionName: "forcedTransfer",
      args: [holder, recipient, amountMinimalUnits],
    }),
  };
}

/** Encodes a role grant or revocation on one contract */
export function roleChangeCall(
  contract: Address,
  action: "grant" | "revoke",
  role: Hex,
  account: Address,
): ContractCall {
  return {
    to: contract,
    data: encodeFunctionData({
      abi: accessControlAdminAbi,
      functionName: action === "grant" ? "grantRole" : "revokeRole",
      args: [role, account],
    }),
  };
}

/** Submits an admin-authorized role grant or revocation on one contract */
export async function sendRoleChange(
  expectedChainId: number,
  from: Address,
  contract: Address,
  action: "grant" | "revoke",
  role: Hex,
  account: Address,
): Promise<Hex> {
  return sendCall(
    expectedChainId,
    from,
    roleChangeCall(contract, action, role, account),
  );
}

/** Encodes the first admin-transfer step on one contract */
export function beginAdminTransferCall(
  contract: Address,
  newAdmin: Address,
): ContractCall {
  return {
    to: contract,
    data: encodeFunctionData({
      abi: accessControlAdminAbi,
      functionName: "beginDefaultAdminTransfer",
      args: [newAdmin],
    }),
  };
}

/** Schedules the first admin-transfer step on one contract */
export async function sendBeginAdminTransfer(
  expectedChainId: number,
  from: Address,
  contract: Address,
  newAdmin: Address,
): Promise<Hex> {
  return sendCall(
    expectedChainId,
    from,
    beginAdminTransferCall(contract, newAdmin),
  );
}

/** Submits acceptance from the pending admin after the on-chain delay */
export async function sendAcceptAdminTransfer(
  expectedChainId: number,
  from: Address,
  contract: Address,
): Promise<Hex> {
  return sendCall(expectedChainId, from, acceptAdminTransferCall(contract));
}

/** Encodes admin-transfer acceptance on one contract */
export function acceptAdminTransferCall(contract: Address): ContractCall {
  return {
    to: contract,
    data: encodeFunctionData({
      abi: accessControlAdminAbi,
      functionName: "acceptDefaultAdminTransfer",
    }),
  };
}

/**
 * Submits a TREASURER_ROLE withdrawal to configured treasury in quote-token minimal units;
 * no approval is needed for the Vault's own funds
 */
export async function sendWithdrawProceeds(
  expectedChainId: number,
  from: Address,
  vault: Address,
  amount: bigint,
): Promise<Hex> {
  return sendCall(expectedChainId, from, withdrawProceedsCall(vault, amount));
}

/** Encodes a treasury withdrawal; see sendWithdrawProceeds */
export function withdrawProceedsCall(
  vault: Address,
  amount: bigint,
): ContractCall {
  return {
    to: vault,
    data: encodeFunctionData({
      abi: vaultAbi,
      functionName: "withdrawProceeds",
      args: [amount],
    }),
  };
}

/** Submits TREASURER_ROLE funding after the treasurer approves the recorded quote amount */
export async function sendFundRedemption(
  expectedChainId: number,
  from: Address,
  escrow: Address,
  id: bigint,
): Promise<Hex> {
  return sendCall(
    expectedChainId,
    from,
    escrowCall(escrow, "fundRedemption", id),
  );
}

/** Submits REDEMPTION_MANAGER_ROLE rejection with a bytes32 reason from encodeReasonCode */
export async function sendRejectRedemption(
  expectedChainId: number,
  from: Address,
  escrow: Address,
  id: bigint,
  reasonCode: Hex,
): Promise<Hex> {
  return sendCall(
    expectedChainId,
    from,
    rejectRedemptionCall(escrow, id, reasonCode),
  );
}

/** Encodes a redemption funding or permissionless claim call */
export function escrowCall(
  escrow: Address,
  functionName: "fundRedemption" | "claimRedemption",
  id: bigint,
): ContractCall {
  return {
    to: escrow,
    data: encodeFunctionData({
      abi: redemptionEscrowAbi,
      functionName,
      args: [id],
    }),
  };
}

/** Encodes redemption rejection with a bytes32 reason */
export function rejectRedemptionCall(
  escrow: Address,
  id: bigint,
  reasonCode: Hex,
): ContractCall {
  return {
    to: escrow,
    data: encodeFunctionData({
      abi: redemptionEscrowAbi,
      functionName: "rejectRedemption",
      args: [id, reasonCode],
    }),
  };
}

/**
 * Client-built deployment input matching IRWAFactory.ProjectConfig field order.
 * Addresses and bytes32 values use hex; integer widths follow the ABI types.
 */
export interface ProjectConfigInput {
  name: string;
  symbol: string;
  decimals: number;
  profileDigest: Hex;
  projectId: Hex;
  quoteToken: Address;
  purchasePricePerWholeToken: bigint;
  redemptionPricePerWholeToken: bigint;
  redemptionTimeout: bigint;
  admin: Address;
  auditor: Address;
  complianceOperator: Address;
  pricer: Address;
  treasurer: Address;
  redemptionManager: Address;
  treasury: Address;
  // viem uses number through uint48 and bigint for wider integer types
  adminTransferDelay: number;
}

/**
 * Submits permissionless factory deployment from the wallet; config.admin sets authority.
 * The server observes ProjectDeployed without signing or relaying this call.
 */
export async function sendDeployProject(
  expectedChainId: number,
  from: Address,
  factoryAddress: Address,
  config: ProjectConfigInput,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    factoryAddress,
    encodeFunctionData({
      abi: rwaFactoryAbi,
      functionName: "deploy",
      args: [config],
    }),
  );
}

/**
 * Client-built mint input matching ISupplyController.MintAttestation.
 * Addresses and bytes32 values use hex; uint fields use bigint.
 */
export interface MintAttestationInput {
  auditor: Address;
  profileDigest: Hex;
  recordKey: Hex;
  metadataDigest: Hex;
  amount: bigint;
  nonce: bigint;
  validUntil: bigint;
  vault: Address;
}

/**
 * Submits the auditor-authorized mint from the wallet; no caller role is required.
 * The contract checks the attestation and the server projects the Minted event.
 */
export async function sendMint(
  expectedChainId: number,
  from: Address,
  supplyControllerAddress: Address,
  attestation: MintAttestationInput,
  signature: Hex,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    supplyControllerAddress,
    encodeFunctionData({
      abi: supplyControllerAbi,
      functionName: "mint",
      args: [attestation, signature],
    }),
  );
}
