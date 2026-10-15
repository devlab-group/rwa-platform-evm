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
  supplyControllerErrorsAbi,
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

/** Expected-chain mismatch; switch and retry without retargeting the write */
export class ChainMismatchError extends Error {
  constructor(
    public readonly expectedChainId: number,
    public readonly actualChainId: number,
  ) {
    super(
      `Wallet is connected to chain ${actualChainId}, but this project requires chain ${expectedChainId}. Switch networks in your wallet and retry.`,
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

/** Reads chain ID without account access, including pre-deployment Setup */
export async function readInjectedChainId(): Promise<number> {
  const provider = getInjectedProvider();
  if (!provider)
    throw new Error(
      "No injected wallet found (e.g. MetaMask). Connect one on the deployment chain to read quote-token decimals.",
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

// Expected chain for viem's send-time check; RPC uses the injected transport
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

/** Switch to expectedChainId; on 4902 add an allowlisted chain and retry, propagating other errors */
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
    if (!network) throw err; // Propagate unsupported-network errors
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
 * Quotes RWA minimal units in quote-token minimal units; execution enforces maxQuoteAmount if price
 * changes
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

/** Waits for a successful receipt; mined reverts throw before approval-dependent spending */
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
      `Transaction ${hash} was mined but reverted on-chain. Contract state is unchanged.`,
    );
  }
}

/** Decodes token reverts for raw writes; unknown/unavailable data falls back to wallet/RPC errors */
export function describeWalletError(err: unknown): string {
  const data = revertData(err);
  if (data) {
    try {
      const decoded = decodeErrorResult({
        abi: [...rwaTokenErrorsAbi, ...supplyControllerErrorsAbi],
        data,
      });
      const named = explainTokenError(decoded.errorName, decoded.args);
      if (named) return named;
    } catch {
      // Fall back when this ABI cannot decode the revert
    }
  }
  return err instanceof Error ? err.message : "Transaction failed.";
}

// Search data on the error and cause chain, examining up to six objects
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
      return `The recipient ${first} is not Allowed in the compliance registry; the transfer was refused.`;
    case "SenderNotAllowed":
      return `${first} is not Allowed in the compliance registry and cannot send.`;
    case "ERC7943InsufficientUnfrozenBalance":
      return `${first} does not have that many unfrozen tokens: ${String(args?.[2])} of their balance is movable (in minimal units).`;
    case "SystemAddressCannotBeFrozen":
      return `${first} is the Vault or redemption escrow, which can never be frozen with a nonzero amount.`;
    case "SystemAddressCannotBeSeized":
      return `${first} is the Vault or redemption escrow, which cannot be a seizure source.`;
    case "ForcedTransferToSelf":
      return "A forced transfer must have different sender and recipient.";
    case "ZeroAddress":
      return "The zero address is not a valid party to this call.";
    case "InsufficientVaultInventory":
      return `The Vault holds ${String(args?.[1])} minimal units, less than the ${String(first)} requested. Nothing was burned.`;
    case "OperationIdAlreadyUsed":
      return "This burn was already executed; its operation ID cannot be reused.";
    case "NonceAlreadyUsed":
      return "This attestation's nonce was already used.";
    case "AttestationExpired":
      return "The auditor's attestation has expired. Obtain a new signed attestation.";
    case "InvalidSignature":
      return "The signature does not match this attestation's auditor.";
    case "WrongAuditor":
    case "WrongProfileDigest":
    case "WrongVault":
      return `The attestation does not match this deployment (${name}).`;
    case "ProjectPaused":
      return "The project is paused. Unpause it before changing supply.";
    case "ZeroAmount":
      return "The attestation amount is zero.";
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
  // Setting chain enables viem's live-chain check before sending
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

// Checks chain and submits local calldata; contracts enforce authorization
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
 * Replaces absolute frozen RWA minimal units; zero releases, and positive system-address freezes
 * revert
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
 * Admin transfer without holder approval; bypasses sender eligibility, freeze and pause but
 * requires eligible recipient
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
 * TREASURER_ROLE withdrawal to configured treasury in quote minimal units; Vault funds need no
 * approval
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
 * Browser-built IRWAFactory.ProjectConfig: ABI field order, hex addresses/bytes32 and declared
 * integer widths
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
 * Permissionless wallet deployment with config.admin authority; server observes ProjectDeployed
 * without relaying
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

/** Browser-built MintAttestation: hex addresses/bytes32 and bigint uint fields */
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

/** Permissionless wallet mint with a valid auditor attestation; server projects Minted */
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

/** BurnAttestation with hex addresses/bytes32 and bigint uint fields */
export interface BurnAttestationInput {
  auditor: Address;
  profileDigest: Hex;
  operationId: Hex;
  metadataDigest: Hex;
  amount: bigint;
  nonce: bigint;
  validUntil: bigint;
  vault: Address;
}

/** Encodes SupplyController.burn(attestation, signature) */
export function burnCall(
  supplyControllerAddress: Address,
  attestation: BurnAttestationInput,
  signature: Hex,
): ContractCall {
  return {
    to: supplyControllerAddress,
    data: encodeFunctionData({
      abi: supplyControllerAbi,
      functionName: "burn",
      args: [attestation, signature],
    }),
  };
}

/** Submits a permissionless burn of Vault inventory using the auditor's attestation */
export function sendBurn(
  expectedChainId: number,
  from: Address,
  supplyControllerAddress: Address,
  attestation: BurnAttestationInput,
  signature: Hex,
): Promise<Hex> {
  return sendCall(
    expectedChainId,
    from,
    burnCall(supplyControllerAddress, attestation, signature),
  );
}
