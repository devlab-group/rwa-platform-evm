// Injected-provider wallet integration: encodes ERC-20 approve/transfer, Vault buy and forward
// order request/settle/cancel, and escrow request/claim/cancel with viem/pinned ABIs. Reads
// contract prices and accepts no server calldata.
// Wallet RPC runs at call time, so builds need no live wallet.
import {
  createPublicClient,
  createWalletClient,
  custom,
  decodeErrorResult,
  defineChain,
  encodeFunctionData,
  erc20Abi,
  formatUnits,
  type Address,
  type Chain,
  type Hex,
  type TransactionReceipt,
} from "viem";
import {
  escrowErrorsAbi,
  incomeDistributorAbi,
  oracleErrorsAbi,
  oracleForwardAbi,
  priceStrategyAbi,
  redemptionEscrowAbi,
  tokenErrorsAbi,
  vaultAbi,
  vaultErrorsAbi,
} from "./abis";
import { oracleReason } from "./v3";
import { roundFromRevert, type OrderReadArgs, type OrderRound } from "./v4";
import { formatUnixSeconds } from "./format";
import { findNetwork, toAddChainParams } from "./networks";

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

/** Wallet-read error for project-chain mismatch; requestSwitchChain lets the caller switch and retry */
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

/** Throws ChainMismatchError when the wallet is not on expectedChainId */
export async function assertChain(
  provider: InjectedProvider,
  expectedChainId: number,
): Promise<void> {
  const actual = await getInjectedChainId(provider);
  if (actual !== expectedChainId)
    throw new ChainMismatchError(expectedChainId, actual);
}

/**
 * Project chain for viem's send-time chain check; RPC uses the injected transport,
 * so rpcUrls stays empty
 */
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

// Recognizes unknown-chain code 4902 at top level or data.originalError, as used by MetaMask
function isChainNotAddedError(err: unknown): boolean {
  if (typeof err !== "object" || err === null) return false;
  const top = (err as { code?: unknown }).code;
  if (top === 4902) return true;
  const nested = (err as { data?: { originalError?: { code?: unknown } } }).data
    ?.originalError?.code;
  return nested === 4902;
}

/**
 * Requests EIP-3326 switching; on 4902, adds an allowlisted network via EIP-3085 and retries.
 * Unknown allowlist entries and other errors propagate.
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
    if (!network) throw err; // Propagate unsupported-network errors
    await provider.request({
      method: "wallet_addEthereumChain",
      params: [toAddChainParams(network)],
    });
    // Switch explicitly; EIP-3085 add-chain need not switch
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

export async function readErc20Balance(
  expectedChainId: number,
  token: Address,
  holder: Address,
): Promise<string> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  await assertChain(provider, expectedChainId);
  const client = createPublicClient({ transport: custom(provider) });
  const balance = await client.readContract({
    address: token,
    abi: erc20Abi,
    functionName: "balanceOf",
    args: [holder],
  });
  return balance.toString();
}

/** ERC-20 decimals() fallback when project metadata omits decimals */
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

/** Reads token allowance(owner, spender) */
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
 * Reads Vault.previewBuy(tokenAmount) in quote-token minimal units;
 * the on-chain quote supplies the slippage ceiling and approved spend
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

/** Reads escrow.previewRedeem(rwaAmount) in quote-token minimal units for the minQuoteOut floor */
export async function readPreviewRedeem(
  expectedChainId: number,
  escrow: Address,
  rwaAmount: bigint,
): Promise<bigint> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  await assertChain(provider, expectedChainId);
  const client = createPublicClient({ transport: custom(provider) });
  return client.readContract({
    address: escrow,
    abi: redemptionEscrowAbi,
    functionName: "previewRedeem",
    args: [rwaAmount],
  });
}

/**
 * Read the Vault's current strategy and its forwardPriced flag: true for later-round pricing,
 * false for instant pricing. Either read can reject; callers must not guess the purchase mode.
 */
export async function readForwardPriced(
  expectedChainId: number,
  vault: Address,
): Promise<boolean> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  await assertChain(provider, expectedChainId);
  const client = createPublicClient({ transport: custom(provider) });
  const strategy = await client.readContract({
    address: vault,
    abi: vaultAbi,
    functionName: "strategy",
  });
  return client.readContract({
    address: strategy,
    abi: priceStrategyAbi,
    functionName: "forwardPriced",
  });
}

/**
 * Reads the Vault minimum recorded on an order when it was requested. Settlement checks the
 * quote against this figure, not the Vault's current minimum.
 */
export async function readOrderMinQuote(
  expectedChainId: number,
  vault: Address,
  id: bigint,
): Promise<bigint> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  await assertChain(provider, expectedChainId);
  const client = createPublicClient({ transport: custom(provider) });
  const order = await client.readContract({
    address: vault,
    abi: vaultAbi,
    functionName: "getPurchase",
    args: [id],
  });
  return order.minPurchaseQuote;
}

/**
 * Read quotePurchaseAfter on the strategy stored with the order. Revert data maps to a round
 * state; failures without revert data, such as an unavailable RPC or empty contract response,
 * reject the read.
 */
export async function readOrderRound(
  expectedChainId: number,
  strategy: Address,
  args: OrderReadArgs,
): Promise<OrderRound> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  await assertChain(provider, expectedChainId);
  // A revert is an expected answer here, so skip viem's retry backoff for it
  const client = createPublicClient({
    transport: custom(provider, { retryCount: 0 }),
  });
  try {
    const [quote, roundId] = await client.readContract({
      address: strategy,
      abi: oracleForwardAbi,
      functionName: "quotePurchaseAfter",
      args: [args.tokenAmount, args.baseRound, args.createdAt],
    });
    return { state: "priced", quote, roundId: BigInt(roundId) };
  } catch (err) {
    const data = revertData(err);
    if (!data) throw err;
    let name: string;
    try {
      name = decodeErrorResult({
        abi: [...oracleErrorsAbi, ...oracleForwardAbi],
        data,
      }).errorName;
    } catch {
      name = "Unknown";
    }
    return roundFromRevert(name);
  }
}

export function formatWithDecimals(raw: string, decimals: number): string {
  return formatUnits(BigInt(raw), decimals);
}

/**
 * Wait for a receipt with 250 ms polls and a 60 s timeout. Reject mined reverts so callers
 * can refresh state after a successful receipt.
 */
export async function waitForTxReceipt(hash: Hex): Promise<TransactionReceipt> {
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
  if (receipt.status === "reverted") {
    throw new Error(`Transaction ${hash} was mined but reverted on-chain.`);
  }
  return receipt;
}

/** Sends ERC-20 approve(spender, amount) through the wallet */
export async function sendErc20Approve(
  expectedChainId: number,
  owner: Address,
  token: Address,
  spender: Address,
  amount: bigint,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    owner,
    token,
    encodeFunctionData({
      abi: erc20Abi,
      functionName: "approve",
      args: [spender, amount],
    }),
  );
}

/** Sends ERC-20 transfer(to, amount) through the wallet */
export async function sendErc20Transfer(
  expectedChainId: number,
  owner: Address,
  token: Address,
  to: Address,
  amount: bigint,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    owner,
    token,
    encodeFunctionData({
      abi: erc20Abi,
      functionName: "transfer",
      args: [to, amount],
    }),
  );
}

// Sends every investor write: simulates it, checks the chain, then asks the wallet
async function sendWrite(
  expectedChainId: number,
  from: Address,
  to: Address,
  data: Hex,
): Promise<Hex> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  await simulateWrite(provider, expectedChainId, from, to, data);
  const chain = projectChain(expectedChainId);
  const client = createWalletClient({
    account: from,
    chain,
    transport: custom(provider),
  });
  // Setting chain enables viem's live-chain check before sending
  return client.sendTransaction({ account: from, chain, to, data });
}

// Simulate with eth_call before the wallet prompt to catch compliance, pause, minimum or
// deadline reverts. Skip other chains; sendTransaction rejects the mismatch.
async function simulateWrite(
  provider: InjectedProvider,
  expectedChainId: number,
  from: Address,
  to: Address,
  data: Hex,
): Promise<void> {
  const client = createPublicClient({ transport: custom(provider) });
  if ((await getInjectedChainId(provider)) !== expectedChainId) return;
  try {
    await client.call({ account: from, to, data });
  } catch (err) {
    throw new Error(
      `Transaction simulation failed: ${revertReason(err)}. Nothing was sent.`,
      { cause: err },
    );
  }
}

// Names a revert by its decoded error and arguments, else its selector or message
function revertReason(err: unknown): string {
  const data = revertData(err);
  if (!data) return err instanceof Error ? err.message : "unknown error";
  try {
    const decoded = decodeErrorResult({
      abi: [
        ...tokenErrorsAbi,
        ...vaultErrorsAbi,
        ...escrowErrorsAbi,
        ...oracleErrorsAbi,
        ...oracleForwardAbi,
        ...incomeDistributorAbi,
      ],
      data,
    });
    return `${decoded.errorName}(${(decoded.args ?? []).map(String).join(", ")})`;
  } catch {
    return data.slice(0, 10);
  }
}

/**
 * Vault.buy arguments: tokenAmount in RWA minimal units, maxQuoteAmount in quote-token
 * minimal units (slippage ceiling), deadline in Unix seconds
 */
export interface BuyInput {
  tokenAmount: bigint;
  maxQuoteAmount: bigint;
  recipient: Address;
  deadline: bigint;
}

/**
 * Broadcasts Vault.buy; caller and recipient must be Allowed. Vault pulls at most maxQuoteAmount
 * of quote tokens; the Investor page approves that ceiling first.
 */
export async function sendBuy(
  expectedChainId: number,
  from: Address,
  vault: Address,
  input: BuyInput,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    vault,
    encodeFunctionData({
      abi: vaultAbi,
      functionName: "buy",
      args: [
        input.tokenAmount,
        input.maxQuoteAmount,
        input.recipient,
        input.deadline,
      ],
    }),
  );
}

/**
 * Vault.requestPurchase arguments: tokenAmount in RWA minimal units, maxQuoteAmount in
 * quote-token minimal units (the deposit and slippage ceiling), deadline in Unix seconds
 */
export interface RequestPurchaseInput {
  tokenAmount: bigint;
  maxQuoteAmount: bigint;
  recipient: Address;
  deadline: bigint;
}

/**
 * Broadcasts Vault.requestPurchase on a forward-priced project; caller and recipient must be
 * Allowed. The Vault pulls maxQuoteAmount as a deposit, so approve it first. The order ID comes
 * from the Vault event and appears in the server list after indexing.
 */
export async function sendRequestPurchase(
  expectedChainId: number,
  from: Address,
  vault: Address,
  input: RequestPurchaseInput,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    vault,
    encodeFunctionData({
      abi: vaultAbi,
      functionName: "requestPurchase",
      args: [
        input.tokenAmount,
        input.maxQuoteAmount,
        input.recipient,
        input.deadline,
      ],
    }),
  );
}

/** Permissionless settlement of a priced order; delivers to the recorded recipient, refunds the buyer */
export async function sendSettlePurchase(
  expectedChainId: number,
  from: Address,
  vault: Address,
  id: bigint,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    vault,
    encodeFunctionData({
      abi: vaultAbi,
      functionName: "settlePurchase",
      args: [id],
    }),
  );
}

/** Returns a Pending order's deposit to its buyer after the timeout, only while no usable round exists */
export async function sendCancelPurchase(
  expectedChainId: number,
  from: Address,
  vault: Address,
  id: bigint,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    vault,
    encodeFunctionData({
      abi: vaultAbi,
      functionName: "cancelPurchase",
      args: [id],
    }),
  );
}

/**
 * Escrow request arguments: rwaAmount in RWA minimal units, minQuoteOut in quote-token
 * minimal units (slippage floor), deadline in Unix seconds
 */
export interface RequestRedemptionInput {
  rwaAmount: bigint;
  minQuoteOut: bigint;
  deadline: bigint;
}

/**
 * Broadcasts requestRedemption after RWA approval; escrow pulls rwaAmount and assigns
 * the request ID, which appears in the server list after indexing
 */
export async function sendRequestRedemption(
  expectedChainId: number,
  from: Address,
  escrow: Address,
  input: RequestRedemptionInput,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    escrow,
    encodeFunctionData({
      abi: redemptionEscrowAbi,
      functionName: "requestRedemption",
      args: [input.rwaAmount, input.minQuoteOut, input.deadline],
    }),
  );
}

/** Permissionless funded claim; pays the recorded beneficiary regardless of caller */
export async function sendClaimRedemption(
  expectedChainId: number,
  from: Address,
  escrow: Address,
  id: bigint,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    escrow,
    encodeFunctionData({
      abi: redemptionEscrowAbi,
      functionName: "claimRedemption",
      args: [id],
    }),
  );
}

/** Returns a Pending request's RWA after timeout; caller must be its Allowed beneficiary */
export async function sendCancelRedemption(
  expectedChainId: number,
  from: Address,
  escrow: Address,
  id: bigint,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    escrow,
    encodeFunctionData({
      abi: redemptionEscrowAbi,
      functionName: "cancelRedemption",
      args: [id],
    }),
  );
}

/** Sends IncomeDistributor.claim; the contract checks the caller's proof, deadline and compliance */
export async function sendIncomeClaim(
  expectedChainId: number,
  from: Address,
  distributor: Address,
  id: bigint,
  amount: bigint,
  proof: Hex[],
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    distributor,
    encodeFunctionData({
      abi: incomeDistributorAbi,
      functionName: "claim",
      args: [id, amount, proof],
    }),
  );
}

const INCOME_ERROR_TEXT: Record<string, string> = {
  ProjectPaused: "The project is paused; claims resume when it is unpaused.",
  UnknownDistribution: "This distribution does not exist on-chain.",
  ClaimWindowClosed: "The claim window for this distribution has closed.",
  AlreadyClaimed: "This entitlement has already been claimed.",
  CallerNotAllowed:
    "Your wallet is not Allowed in the compliance registry and cannot claim.",
  ZeroAmount: "The claim amount is zero.",
  InvalidProof: "The proof does not match this distribution.",
  ExceedsTotal: "This claim would exceed the distribution total.",
};

// Vault and escrow revert wording; args follow the error's ABI inputs
function contractErrorText(
  name: string,
  args: readonly unknown[] | undefined,
): string {
  const arg = (i: number) => String(args?.[i]);
  switch (name) {
    case "QuoteBelowMinimum":
      return `This purchase is below the project minimum: it quotes ${arg(0)} and the minimum is ${arg(1)} (quote-token minimal units).`;
    case "ProjectPaused":
      return "The project is paused. Try again after it is unpaused.";
    case "CallerNotAllowed":
      return "Your wallet is not Allowed in the compliance registry.";
    case "BeneficiaryNotAllowed":
      return "The beneficiary wallet is not Allowed in the compliance registry, so this request cannot proceed until it is Allowed again.";
    case "DeadlineExpired":
      return `The transaction deadline passed at ${formatUnixSeconds(Number(args?.[0]))}. Preview the quote again and retry.`;
    case "ZeroAmount":
      return "The amount is zero. Enter an amount above zero.";
    case "InsufficientInventory":
      return `The vault holds ${arg(1)} RWA minimal units but this purchase needs ${arg(0)}. Reduce the amount.`;
    case "QuoteAboveMax":
      return `The price rose above your limit: it quotes ${arg(0)} and your maximum is ${arg(1)} (quote-token minimal units). Preview the quote again or raise the slippage.`;
    case "QuoteBelowMin":
      return `The redemption now quotes ${arg(0)}, below your minimum of ${arg(1)} (quote-token minimal units). Preview the quote again or lower the slippage.`;
    case "ZeroQuote":
      return "The redemption quotes zero, so the amount is too small. Increase the amount.";
    case "QuoteDeltaMismatch":
    case "RwaDeltaMismatch":
      return `The token moved a different amount than expected (expected ${arg(0)}, got ${arg(1)} minimal units), so the call was rejected.`;
    case "InvalidDealingCutoff":
      return `The price cut-off of ${arg(0)} seconds is not shorter than the ${arg(1)} second timeout, so this project cannot take forward-priced redemption requests until the issuer fixes its configuration.`;
    case "ForwardPriced":
      return "This project prices purchases at a later feed round, so a direct buy is not possible. Reload the page and place an order instead.";
    case "NotForwardPriced":
      return "This project prices purchases instantly, so an order cannot be placed. Reload the page and buy directly instead.";
    case "NotPending":
      return "This request or order is no longer pending.";
    case "NotFunded":
      return "This redemption request has not been funded, so it cannot be claimed.";
    case "NotBuyer":
      return "Only the wallet that placed this order can cancel it.";
    case "NotBeneficiary":
      return "Only the wallet that made this request can cancel it.";
    case "BuyerNotAllowed":
      return "The buyer wallet is not Allowed in the compliance registry, so this order cannot be settled or cancelled until it is Allowed again.";
    case "TimeoutNotReached":
      return `The timeout has not elapsed yet; cancelling opens at ${formatUnixSeconds(Number(args?.[0]))}.`;
    case "RoundAvailable":
      return "A price round is available for this order, so it cannot be cancelled. Settle it instead.";
    default:
      return `The contract rejected the call (${name}).`;
  }
}

/** Oracle error name when err carries oracle revert data, so callers can stop new purchases */
export function oracleErrorName(err: unknown): string | undefined {
  const data = revertData(err);
  if (!data) return undefined;
  try {
    return decodeErrorResult({ abi: oracleErrorsAbi, data }).errorName;
  } catch {
    return undefined;
  }
}

/**
 * Decodes token revert bytes for raw sends, whose viem errors lack an ABI.
 * Frozen-balance detail explains why the full wallet balance may not be spendable.
 * Pass "income" for IncomeDistributor.claim to get claim-specific wording.
 */
export function describeTxError(
  err: unknown,
  fallback: string,
  context?: "income",
): string {
  if (isUserRejection(err)) return "Request cancelled in wallet.";
  const data = revertData(err);
  if (data) {
    const oracle = oracleErrorName(err);
    if (oracle) return `Prices unavailable: ${oracleReason(oracle)}.`;
    // Selectors such as ProjectPaused() and CallerNotAllowed(address) are shared with
    // IncomeDistributor, whose claim wording applies there
    if (context !== "income") {
      try {
        const decoded = decodeErrorResult({
          abi: [...vaultErrorsAbi, ...escrowErrorsAbi],
          data,
        });
        return contractErrorText(decoded.errorName, decoded.args);
      } catch {
        // Not a Vault or escrow error
      }
    }
    try {
      const round = decodeErrorResult({ abi: oracleForwardAbi, data });
      if (round.errorName === "RoundNotAvailable") {
        return "No price has been published for this order yet. Try again after the next feed update.";
      }
    } catch {
      // Not a forward-pricing error
    }
    if (context === "income") {
      try {
        const income = decodeErrorResult({ abi: incomeDistributorAbi, data });
        const text = INCOME_ERROR_TEXT[income.errorName];
        if (text) return text;
      } catch {
        // Not an income error
      }
    }
    try {
      const decoded = decodeErrorResult({ abi: tokenErrorsAbi, data });
      switch (decoded.errorName) {
        case "ERC7943InsufficientUnfrozenBalance":
          return `Part of your balance is frozen by the issuer: only ${String(decoded.args[2])} minimal units are available. Contact the issuer about an unexpected hold.`;
        case "SenderNotAllowed":
          return "Your wallet is not Allowed in the compliance registry and cannot send tokens.";
        case "RecipientNotAllowed":
          return "The destination wallet is not Allowed in the compliance registry.";
      }
    } catch {
      // Unknown token error; use the raw message
    }
  }
  return err instanceof Error ? viemMessage(err) : fallback;
}

// EIP-1193 code 4001, which viem surfaces as UserRejectedRequestError, anywhere on the cause chain
function isUserRejection(err: unknown): boolean {
  let current: unknown = err;
  for (let depth = 0; current && depth < 6; depth++) {
    const e = current as { code?: unknown; name?: unknown };
    if (e.code === 4001 || e.name === "UserRejectedRequestError") return true;
    current = (current as { cause?: unknown }).cause;
  }
  return false;
}

// viem errors carry a one-line shortMessage; their message adds request arguments and a version
// footer. The RPC detail is kept when the short form is generic.
function viemMessage(err: Error): string {
  const { shortMessage, details } = err as {
    shortMessage?: unknown;
    details?: unknown;
  };
  if (typeof shortMessage !== "string" || !shortMessage) return err.message;
  return typeof details === "string" &&
    details &&
    !shortMessage.includes(details)
    ? `${shortMessage} ${details}`
    : shortMessage;
}

// Finds 0x revert data on the error or cause chain, examining up to six objects
function revertData(err: unknown): Hex | undefined {
  let current: unknown = err;
  for (let depth = 0; current && depth < 6; depth++) {
    let candidate =
      (current as { data?: unknown }).data ??
      (current as { raw?: unknown }).raw;
    // MetaMask nests the revert data one level down: { data: { data: "0x..." } }
    if (candidate && typeof candidate === "object") {
      candidate = (candidate as { data?: unknown }).data;
    }
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
