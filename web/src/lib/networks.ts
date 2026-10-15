// Allowlisted labels and EIP-3085 parameters; add supported chains here.
// Wallets use rpcUrls when adding chains; app RPC uses the injected provider.
export interface AllowedNetwork {
  chainId: number;
  /** Switcher and networkLabel() label */
  label: string;
  /** wallet_addEthereumChain parameters (EIP-3085) */
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
    // Keep mainnet add-chain parameters for uniform entries; wallets typically know it already
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

/** The allow-listed network for `chainId`, or undefined when it is not offered */
export function findNetwork(chainId: number): AllowedNetwork | undefined {
  return ALLOWED_NETWORKS.find((n) => n.chainId === chainId);
}

/** Label for a chain ID: the allow-listed label, or "Unsupported network" */
export function networkLabel(chainId: number | null): string {
  if (chainId === null) return "Unsupported network";
  return findNetwork(chainId)?.label ?? "Unsupported network";
}

/** EIP-3085 request parameters for wallet_addEthereumChain */
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
