// Minimal, abstracted wallet integration. Every call the app broadcasts is
// encoded here from a pinned ABI fragment (lib/abis.ts) — the standard ERC-20
// approve/transfer plus the investor's own Vault.buy and RedemptionEscrow
// request/claim/cancel. Nothing is ever submitted from server-supplied
// calldata, and the buy/redeem prices are read from the same contracts rather
// than quoted by the server. Deliberately isolated behind this module so the rest of the app —
// and the production build — never needs a live wallet to run.
import {
  createPublicClient,
  createWalletClient,
  custom,
  defineChain,
  encodeFunctionData,
  erc20Abi,
  formatUnits,
  type Address,
  type Chain,
  type Hex,
} from "viem";
import { redemptionEscrowAbi, vaultAbi } from "./abis";
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
 * Thrown by every chain-touching wallet call when the wallet's
 * currently-connected chain doesn't match the project's configured chain.
 * Never silently retarget a tx — the caller must ask the wallet to switch
 * (see requestSwitchChain) and retry.
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
 * A minimal viem Chain descriptor for the project's configured chain.
 * Transport is always the injected provider (never a direct RPC), so
 * `rpcUrls` is unused; the object exists so the wallet client can assert
 * the wallet's actual chain against it (see the `chain` param below) instead
 * of passing `chain: null`, which disables that check entirely.
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
 * True when a wallet_switchEthereumChain rejection is EIP-3326's 4902
 * ("chain not added to the wallet"). MetaMask sometimes nests the code under
 * `data.originalError`, so check both.
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
 * Requests that the wallet switch to `expectedChainId` (EIP-3326). If the
 * wallet doesn't know that chain yet (4902 — e.g. a local anvil node absent
 * from MetaMask by default), add it via wallet_addEthereumChain using the
 * allow-listed network params (lib/networks.ts) and retry the switch. An
 * unknown chain (not in the allow-list) or any other error propagates.
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
    // Adding usually leaves the wallet on the new chain, but not guaranteed —
    // re-issue the switch so the final state is deterministic.
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

/**
 * Project.decimals is not exposed by GET /api/v1/project (only present on
 * the deploy request), so it's read directly from the token contract —
 * standard ERC-20, robust regardless of that API gap.
 */
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
 * Vault.previewBuy(tokenAmount) — what buying `tokenAmount` RWA costs at the
 * current on-chain price, in quote-token minimal units. Read straight from the
 * vault: the figure the slippage ceiling (and therefore the approved spend) is
 * derived from never passes through the server.
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
 * RedemptionEscrow.previewRedeem(rwaAmount) — the quote-token payout for
 * redeeming `rwaAmount`, in quote-token minimal units. The counterpart of
 * readPreviewBuy; feeds the slippage floor carried in `minQuoteOut`.
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
 * Blocks until a submitted transaction is mined — the buy approval must be
 * confirmed on-chain before the UI trusts it and enables Buy. A short polling
 * interval is fine here: the platform targets a single, fast
 * local/permissioned chain, not mainnet.
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
  // `chain` (not null) makes viem assert the wallet's live chain against it
  // immediately before sending — a mismatch throws instead of broadcasting.
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
 * chain. Shared by every buy/redemption helper below. Mirrors
 * sendErc20Approve's chain-assertion.
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
 * The Vault.buy(tokenAmount, maxQuoteAmount, recipient, deadline) argument.
 * `tokenAmount` is in RWA minimal units, `maxQuoteAmount` the slippage-bounded
 * spend ceiling in quote-token minimal units, and `deadline` unix seconds.
 */
export interface BuyInput {
  tokenAmount: bigint;
  maxQuoteAmount: bigint;
  recipient: Address;
  deadline: bigint;
}

/**
 * Broadcasts Vault.buy(...) from the connected investor wallet. The vault pulls
 * up to `maxQuoteAmount` of the quote token via safeTransferFrom, so the buyer
 * must have approved the vault for at least that much first (see the Investor
 * page's approve→buy flow). Both the caller and `recipient` must be Allowed.
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
 * argument. `rwaAmount` is in RWA minimal units, `minQuoteOut` the
 * slippage-bounded payout floor in quote-token minimal units, `deadline` unix
 * seconds.
 */
export interface RequestRedemptionInput {
  rwaAmount: bigint;
  minQuoteOut: bigint;
  deadline: bigint;
}

/**
 * Broadcasts RedemptionEscrow.requestRedemption(...) from the connected
 * investor wallet. The escrow pulls `rwaAmount` of the RWA token via
 * safeTransferFrom, so the caller must have approved the escrow first. The
 * request id is assigned on-chain; the UI picks it up from the indexed list.
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
 * RedemptionEscrow.claimRedemption(id) — permissionless once the request is
 * funded. Anyone may broadcast it; the escrow always pays the beneficiary
 * recorded at request time, never the caller.
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
 * RedemptionEscrow.cancelRedemption(id) — refunds the escrowed RWA to the
 * beneficiary of a still-Pending request whose timeout has elapsed. Reverts
 * before then, and (like every RWA transfer) needs both parties Allowed.
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
