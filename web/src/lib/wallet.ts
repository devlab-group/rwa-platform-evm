// Minimal, abstracted wallet integration. Every call the console broadcasts is
// encoded here from a pinned ABI fragment (lib/abis.ts) — the standard ERC-20
// approve plus the admin actions (deploy, mint, roles, pause/price, treasury
// withdrawal, redemption fund/reject). Nothing is ever submitted from
// server-supplied calldata. Deliberately isolated behind this module so the
// rest of the app — and the production build — never needs a live wallet to run.
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

/**
 * Reads the injected wallet's currently-connected chainId without prompting
 * for account access (eth_chainId needs no permission). Used pre-deploy on
 * Setup, where no project (hence no configured chainId) exists yet but the
 * quote token's decimals must still be read on-chain: the operator's wallet
 * must already be on the deployment chain, so its live chain is the one to
 * read against.
 */
export async function readInjectedChainId(): Promise<number> {
  const provider = getInjectedProvider();
  if (!provider)
    throw new Error(
      "No injected wallet found (e.g. MetaMask). Connect a wallet on the deployment chain to read the quote token's decimals.",
    );
  return getInjectedChainId(provider);
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
 * Vault.previewBuy(tokenAmount) — what a purchase of `tokenAmount` RWA minimal
 * units currently costs, in quote-token minimal units. A plain view call: the
 * price lives in the vault's strategy, so the chain is the only authority on
 * it. Quotes are indicative — the price can move before the buy lands, which
 * is why Vault.buy takes its own maxQuoteAmount bound.
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
 * Blocks until a submitted transaction is mined AND succeeded. An ERC-20
 * approval must be confirmed on-chain before the UI trusts it and enables the
 * call that spends it. A short polling interval is fine here: the platform
 * targets a single, fast local/permissioned chain, not mainnet.
 *
 * A mined-but-reverted transaction resolves the receipt normally, so the
 * status has to be checked explicitly: without it a seizure the chain refused
 * (the holder emptied the wallet first, the recipient lost compliance between
 * the gas estimate and inclusion) would be reported to the operator as done.
 * Every caller already surfaces a thrown message, so throwing here is enough.
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
 * Turns a failed wallet call into a sentence, decoding the token's own revert
 * data when the wallet passes it back. `sendWrite` posts raw calldata, so viem
 * has no ABI attached to the call and cannot name the error by itself; the
 * revert bytes still travel on the error object, and `rwaTokenErrorsAbi`
 * covers every error the enforcement calls can raise.
 *
 * Falls back to the raw message whenever there is nothing decodable, which is
 * the common case for a user rejection or an RPC failure.
 */
export function describeWalletError(err: unknown): string {
  const data = revertData(err);
  if (data) {
    try {
      const decoded = decodeErrorResult({ abi: rwaTokenErrorsAbi, data });
      const named = explainTokenError(decoded.errorName, decoded.args);
      if (named) return named;
    } catch {
      // Not one of ours (an OZ error, a plain string revert): fall through.
    }
  }
  return err instanceof Error ? err.message : "Transaction failed.";
}

/** Digs the `0x`-prefixed revert payload out of whatever shape the wallet threw. */
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

/** Plain-language text for the errors an operator can actually trigger. */
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

/**
 * Sends an encoded contract write from the connected wallet on the project's
 * chain. Shared by every helper below, including the Security-page admin
 * actions, which broadcast the privileged call directly (the contract's
 * onlyRole check is the authorization, not the UI — see lib/roles.ts).
 * Mirrors sendErc20Approve's chain-assertion.
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

/** RWAToken.pause()/unpause() — halts or resumes all token transfers (trading). PAUSER_ROLE. */
export async function sendSetPaused(
  expectedChainId: number,
  from: Address,
  token: Address,
  pause: boolean,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    token,
    encodeFunctionData({
      abi: pausableAbi,
      functionName: pause ? "pause" : "unpause",
    }),
  );
}

/** FixedPriceStrategy.setPurchasePrice/setRedemptionPrice — price is quote-token minimal units per whole token. PRICER_ROLE. */
export async function sendSetStrategyPrice(
  expectedChainId: number,
  from: Address,
  strategy: Address,
  side: "purchase" | "redemption",
  priceMinimalUnits: bigint,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    strategy,
    encodeFunctionData({
      abi: fixedPriceStrategyAbi,
      functionName:
        side === "purchase" ? "setPurchasePrice" : "setRedemptionPrice",
      args: [priceMinimalUnits],
    }),
  );
}

/**
 * RWAToken.setFrozenTokens(account, amount) - ERC-7943. `amount` is ABSOLUTE
 * token minimal units, not a delta: it replaces whatever was frozen before,
 * and 0 releases the hold entirely. DEFAULT_ADMIN_ROLE authorizes; the
 * contract also refuses a non-zero amount on the Vault or RedemptionEscrow.
 */
export async function sendSetFrozenTokens(
  expectedChainId: number,
  from: Address,
  token: Address,
  account: Address,
  amountMinimalUnits: bigint,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    token,
    encodeFunctionData({
      abi: erc7943Abi,
      functionName: "setFrozenTokens",
      args: [account, amountMinimalUnits],
    }),
  );
}

/**
 * RWAToken.forcedTransfer(from, to, amount) - ERC-7943 seizure. Moves tokens
 * without the holder's signature, bypassing their own eligibility and an
 * active pause; the recipient must still be compliance-Allowed, which the
 * contract enforces. DEFAULT_ADMIN_ROLE authorizes.
 */
export async function sendForcedTransfer(
  expectedChainId: number,
  sender: Address,
  token: Address,
  holder: Address,
  recipient: Address,
  amountMinimalUnits: bigint,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    sender,
    token,
    encodeFunctionData({
      abi: erc7943Abi,
      functionName: "forcedTransfer",
      args: [holder, recipient, amountMinimalUnits],
    }),
  );
}

/** AccessControl.grantRole/revokeRole(role, account) on one contract. DEFAULT_ADMIN_ROLE authorizes. */
export async function sendRoleChange(
  expectedChainId: number,
  from: Address,
  contract: Address,
  action: "grant" | "revoke",
  role: Hex,
  account: Address,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    contract,
    encodeFunctionData({
      abi: accessControlAdminAbi,
      functionName: action === "grant" ? "grantRole" : "revokeRole",
      args: [role, account],
    }),
  );
}

/** AccessControlDefaultAdminRules.beginDefaultAdminTransfer(newAdmin) on one contract (step 1 of 2). */
export async function sendBeginAdminTransfer(
  expectedChainId: number,
  from: Address,
  contract: Address,
  newAdmin: Address,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    contract,
    encodeFunctionData({
      abi: accessControlAdminAbi,
      functionName: "beginDefaultAdminTransfer",
      args: [newAdmin],
    }),
  );
}

/**
 * AccessControlDefaultAdminRules.acceptDefaultAdminTransfer() on one contract
 * (step 2 of 2). Broadcast by the PENDING (incoming) admin to complete a
 * transfer begun with beginDefaultAdminTransfer; reverts until the on-chain
 * delay has elapsed. No args — the contract already recorded the pending admin.
 */
export async function sendAcceptAdminTransfer(
  expectedChainId: number,
  from: Address,
  contract: Address,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    contract,
    encodeFunctionData({
      abi: accessControlAdminAbi,
      functionName: "acceptDefaultAdminTransfer",
    }),
  );
}

/**
 * Vault.withdrawProceeds(amount) — TREASURER_ROLE. Pays out the vault's
 * accumulated quote-token proceeds; no approve needed (the vault holds and
 * transfers the funds). `amount` is in quote-token minimal units.
 */
export async function sendWithdrawProceeds(
  expectedChainId: number,
  from: Address,
  vault: Address,
  amount: bigint,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    vault,
    encodeFunctionData({
      abi: vaultAbi,
      functionName: "withdrawProceeds",
      args: [amount],
    }),
  );
}

/**
 * RedemptionEscrow.fundRedemption(id) — TREASURER_ROLE. The escrow pulls the
 * request's quoteAmount from the treasurer via safeTransferFrom, so the caller
 * MUST have approved the escrow for that amount of the quote token first (see
 * the Redemptions page's approve→fund flow).
 */
export async function sendFundRedemption(
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
      functionName: "fundRedemption",
      args: [id],
    }),
  );
}

/** RedemptionEscrow.rejectRedemption(id, reasonCode) — REDEMPTION_MANAGER_ROLE. reasonCode is a bytes32 (see encodeReasonCode). */
export async function sendRejectRedemption(
  expectedChainId: number,
  from: Address,
  escrow: Address,
  id: bigint,
  reasonCode: Hex,
): Promise<Hex> {
  return sendWrite(
    expectedChainId,
    from,
    escrow,
    encodeFunctionData({
      abi: redemptionEscrowAbi,
      functionName: "rejectRedemption",
      args: [id, reasonCode],
    }),
  );
}

/**
 * The full RWAFactory.deploy(ProjectConfig) argument, assembled client-side on
 * Setup. Field names/order mirror IRWAFactory.ProjectConfig (see rwaFactoryAbi);
 * bytes32/address fields are 0x-hex strings and the uint fields are bigint.
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
  // uint48 — viem's ABI types map integer widths <= 48 bits to `number`
  // (uint8 decimals above too), while the wider uint64/uint256 fields are bigint.
  adminTransferDelay: number;
}

/**
 * Broadcasts RWAFactory.deploy(config) from the connected admin wallet. deploy
 * is permissionless (no on-chain role) — the `admin` field inside `config`
 * becomes DEFAULT_ADMIN_ROLE. The server observes the resulting ProjectDeployed
 * event; it never signs or relays the deploy.
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
 * The SupplyController.mint(MintAttestation, signature) attestation, assembled
 * client-side on Assets from the record + project. Field names/order mirror
 * ISupplyController.MintAttestation (see supplyControllerAbi); bytes32/address
 * fields are 0x-hex strings and the uint fields are bigint.
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
 * Broadcasts SupplyController.mint(attestation, signature) from the connected
 * admin wallet. There is no on-chain role gate — the auditor's EIP-712
 * `signature` over the attestation (bound to an unused nonce) is the
 * authorization, verified on-chain. The server observes the Minted event and
 * advances the record; it no longer relays the mint.
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
