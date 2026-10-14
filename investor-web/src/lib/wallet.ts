// Wallet integration over the injected EIP-1193 provider (window.ethereum).
// Every call the app broadcasts is encoded here from viem's ERC-20 ABI or a
// pinned fragment (lib/abis.ts): ERC-20 approve and transfer, Vault.buy, and
// RedemptionEscrow request, claim and cancel. No calldata comes from the
// server, and buy and redeem prices are read from the contracts, not quoted by
// the server. Nothing outside this module touches the provider, so the rest of
// the app and the production build run without a live wallet.
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
} from "viem";
import { redemptionEscrowAbi, tokenErrorsAbi, vaultAbi } from "./abis";
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

/**
 * Thrown by the wallet reads in this module when the wallet's current chain
 * differs from the project's chain. A transaction is never silently
 * retargeted: the caller asks the wallet to switch (see requestSwitchChain) and
 * retries.
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

/** Throws ChainMismatchError unless the wallet is currently on `expectedChainId`. */
export async function assertChain(
  provider: InjectedProvider,
  expectedChainId: number,
): Promise<void> {
  const actual = await getInjectedChainId(provider);
  if (actual !== expectedChainId)
    throw new ChainMismatchError(expectedChainId, actual);
}

/**
 * A minimal viem Chain for the project's chain. Transport is always the
 * injected provider, never a direct RPC, so `rpcUrls` is unused. The object
 * exists so viem checks the wallet's live chain against it before sending;
 * with no chain on the client that check is skipped.
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

/**
 * True when a wallet_switchEthereumChain rejection is code 4902, which wallets
 * such as MetaMask use for a chain ID they don't know (EIP-3326 itself defines
 * no error code). MetaMask sometimes nests the code under `data.originalError`,
 * so check both.
 */
function isChainNotAddedError(err: unknown): boolean {
  if (typeof err !== "object" || err === null) return false;
  const top = (err as { code?: unknown }).code;
  if (top === 4902) return true;
  const nested = (err as { data?: { originalError?: { code?: unknown } } }).data
    ?.originalError?.code;
  return nested === 4902;
}

/**
 * Asks the wallet to switch to `expectedChainId` (wallet_switchEthereumChain,
 * EIP-3326). If the wallet doesn't know the chain (4902, e.g. a local anvil
 * node), adds it with wallet_addEthereumChain (EIP-3085) from the allow-listed
 * params in lib/networks.ts and retries the switch. A chain outside the
 * allow-list, or any other error, propagates.
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
    // EIP-3085 does not require the wallet to switch after adding, so switch
    // explicitly
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

/** Reads the token's ERC-20 `decimals()`; the fallback when the project response omits decimals. */
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

/** Reads the current on-chain allowance(owner, spender) for `token`. */
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
 * Vault.previewBuy(tokenAmount): the cost of buying `tokenAmount` RWA at the
 * current on-chain price, in quote-token minimal units. Read from the vault, so
 * the figure the slippage ceiling and approved spend derive from never passes
 * through the server.
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
 * RedemptionEscrow.previewRedeem(rwaAmount): the quote-token payout for
 * redeeming `rwaAmount`, in quote-token minimal units. The slippage floor sent
 * as `minQuoteOut` is derived from it.
 */
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

export function formatWithDecimals(raw: string, decimals: number): string {
  return formatUnits(BigInt(raw), decimals);
}

/**
 * Resolves once a submitted transaction is mined (polling every 250 ms, giving
 * up after 60 s), so the UI can confirm an approval on-chain before it enables
 * the next step.
 */
export async function waitForTxReceipt(hash: Hex): Promise<void> {
  const provider = getInjectedProvider();
  if (!provider) throw new Error("No injected wallet found.");
  const client = createPublicClient({
    transport: custom(provider),
    pollingInterval: 250,
  });
  await client.waitForTransactionReceipt({ hash, timeout: 60_000 });
}

/** Sends an ERC-20 `approve(spender, amount)` from the connected wallet. */
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
  // With `chain` set, viem checks the wallet's live chain against it just
  // before sending and throws on a mismatch instead of broadcasting
  return client.sendTransaction({
    account: owner,
    chain,
    to: token,
    data: encodeFunctionData({
      abi: erc20Abi,
      functionName: "approve",
      args: [spender, amount],
    }),
  });
}

/** Sends an ERC-20 `transfer(to, amount)` from the connected wallet. */
export async function sendErc20Transfer(
  expectedChainId: number,
  owner: Address,
  token: Address,
  to: Address,
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
  return client.sendTransaction({
    account: owner,
    chain,
    to: token,
    data: encodeFunctionData({
      abi: erc20Abi,
      functionName: "transfer",
      args: [to, amount],
    }),
  });
}

/**
 * Sends an encoded contract write from the connected wallet on the project's
 * chain, with the same chain check as sendErc20Approve. Shared by the buy and
 * redemption helpers below.
 */
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

/**
 * The Vault.buy(tokenAmount, maxQuoteAmount, recipient, deadline) arguments.
 * `tokenAmount` is in RWA minimal units, `maxQuoteAmount` is the
 * slippage-bounded spend ceiling in quote-token minimal units, and `deadline`
 * is in unix seconds.
 */
export interface BuyInput {
  tokenAmount: bigint;
  maxQuoteAmount: bigint;
  recipient: Address;
  deadline: bigint;
}

/**
 * Broadcasts Vault.buy(...) from the connected investor wallet. The vault pulls
 * the quoted amount (at most `maxQuoteAmount`) of quote token with
 * safeTransferFrom, so the buyer must have approved the vault first; the
 * Investor page approves the full `maxQuoteAmount`. Both the caller and
 * `recipient` must be Allowed.
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
 * The RedemptionEscrow.requestRedemption(rwaAmount, minQuoteOut, deadline)
 * arguments. `rwaAmount` is in RWA minimal units, `minQuoteOut` is the
 * slippage-bounded payout floor in quote-token minimal units, and `deadline` is
 * in unix seconds.
 */
export interface RequestRedemptionInput {
  rwaAmount: bigint;
  minQuoteOut: bigint;
  deadline: bigint;
}

/**
 * Broadcasts RedemptionEscrow.requestRedemption(...) from the connected
 * investor wallet. The escrow pulls `rwaAmount` of the RWA token with
 * safeTransferFrom, so the caller must have approved the escrow first. The
 * request id is assigned on-chain and appears in the server's redemption list
 * once indexed.
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

/**
 * RedemptionEscrow.claimRedemption(id): permissionless once the request is
 * funded. Anyone may send it; the escrow pays the beneficiary recorded at
 * request time, never the caller.
 */
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

/**
 * RedemptionEscrow.cancelRedemption(id): returns the escrowed RWA to the
 * beneficiary of a still-Pending request once the redemption timeout has
 * elapsed. Only the beneficiary can call it, it reverts before the timeout, and
 * it also reverts while the beneficiary is not Allowed.
 */
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

/**
 * Decodes raw-call revert bytes against token errors to explain failed investor
 * transactions. viem has no ABI for the raw send. Frozen-balance failures need
 * this detail because the wallet's full balance includes tokens it cannot send.
 */
export function describeTxError(err: unknown, fallback: string): string {
  const data = revertData(err);
  if (data) {
    try {
      const decoded = decodeErrorResult({ abi: tokenErrorsAbi, data });
      switch (decoded.errorName) {
        case "ERC7943InsufficientUnfrozenBalance":
          return `Part of your balance is frozen by the issuer: only ${String(decoded.args[2])} (in minimal units) can be moved right now. Contact the issuer if you were not expecting this.`;
        case "SenderNotAllowed":
          return "Your wallet is not currently Allowed in the compliance registry, so it cannot send tokens.";
        case "RecipientNotAllowed":
          return "The destination wallet is not Allowed in the compliance registry.";
      }
    } catch {
      // Not one of the token's errors: fall through to the raw message.
    }
  }
  return err instanceof Error ? err.message : fallback;
}

/** Finds the `0x`-prefixed revert payload on the thrown error or its `cause` chain (up to 6 levels). */
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
