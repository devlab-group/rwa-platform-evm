// Fixed allow-list of networks the in-app switcher offers. Extend by adding an
// entry — each carries both the UI label and the full EIP-3085
// (wallet_addEthereumChain) parameters, so a chain the wallet doesn't know yet
// (e.g. a local anvil node) can be added on demand before switching to it.
//
// Transports always run over the injected provider (custom(provider), see
// lib/wallet.ts), never a direct RPC, so `rpcUrls` here is used only as
// wallet_addEthereumChain input — it's what the wallet itself dials, not the app.
export interface AllowedNetwork {
  chainId: number;
  /** Short label shown in the switcher and by networkLabel(). */
  label: string;
  /** wallet_addEthereumChain params (EIP-3085). */
  chainName: string;
  nativeCurrency: { name: string; symbol: string; decimals: number };
  rpcUrls: string[];
  blockExplorerUrls: string[];
}

const ETH: AllowedNetwork["nativeCurrency"] = {
  name: "Ether",
  symbol: "ETH",
  decimals: 18,
};

export const ALLOWED_NETWORKS: AllowedNetwork[] = [
  {
    chainId: 1,
    label: "Ethereum",
    chainName: "Ethereum Mainnet",
    nativeCurrency: ETH,
    // Mainnet is always present in wallets, so add-chain is never actually
    // invoked for it; the params are here only for completeness/consistency.
    rpcUrls: ["https://cloudflare-eth.com"],
    blockExplorerUrls: ["https://etherscan.io"],
  },
  {
    chainId: 31337,
    label: "Local",
    chainName: "Local (Anvil)",
    nativeCurrency: ETH,
    rpcUrls: ["http://localhost:8545"],
    blockExplorerUrls: [],
  },
];

/** The allow-listed network for `chainId`, or undefined if it isn't one we offer. */
export function findNetwork(chainId: number): AllowedNetwork | undefined {
  return ALLOWED_NETWORKS.find((n) => n.chainId === chainId);
}

/** Human label for a chainId — the allow-listed label, or "Unsupported network". */
export function networkLabel(chainId: number | null): string {
  if (chainId === null) return "Unsupported network";
  return findNetwork(chainId)?.label ?? "Unsupported network";
}

/** EIP-3085 request params for wallet_addEthereumChain. */
export function toAddChainParams(network: AllowedNetwork): {
  chainId: string;
  chainName: string;
  nativeCurrency: AllowedNetwork["nativeCurrency"];
  rpcUrls: string[];
  blockExplorerUrls: string[];
} {
  return {
    chainId: `0x${network.chainId.toString(16)}`,
    chainName: network.chainName,
    nativeCurrency: network.nativeCurrency,
    rpcUrls: network.rpcUrls,
    blockExplorerUrls: network.blockExplorerUrls,
  };
}
