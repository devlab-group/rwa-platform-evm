// Allowlisted network labels and EIP-3085 parameters for the switcher.
// Add entries here to support another chain. rpcUrls is input to
// wallet_addEthereumChain; the wallet dials it while app RPC uses the injected provider.
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
    // Wallets already include Ethereum mainnet, so add-chain is not expected
    // for it; the params keep the entries uniform
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

/** The allow-listed network for `chainId`, or undefined if it isn't offered. */
export function findNetwork(chainId: number): AllowedNetwork | undefined {
  return ALLOWED_NETWORKS.find((n) => n.chainId === chainId);
}

/** Human label for a chainId: the allow-listed label, or "Unsupported network". */
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
