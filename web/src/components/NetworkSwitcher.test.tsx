import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { NetworkSwitcher } from "./NetworkSwitcher";
import { installFakeWallet, renderWithWallet } from "../test/walletHarness";

const ETH_MAINNET = 1;
const LOCAL = 31337;
const LOCAL_HEX = "0x7a69";

/** Finds every request call for a given EIP-1193 method. */
function callsFor(
  wallet: ReturnType<typeof installFakeWallet>,
  method: string,
): Array<{ method: string; params?: unknown[] }> {
  return wallet.request.mock.calls
    .map((c) => c[0] as { method: string; params?: unknown[] })
    .filter((arg) => arg.method === method);
}

describe("NetworkSwitcher", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  afterEach(() => {
    wallet.uninstall();
  });

  it("is absent when no wallet is connected", () => {
    wallet = installFakeWallet({ chainId: LOCAL });
    renderWithWallet(<NetworkSwitcher />); // not connected
    expect(screen.queryByRole("combobox", { name: "Network" })).toBeNull();
  });

  it("renders the wallet's current network as the selected option", async () => {
    wallet = installFakeWallet({ chainId: LOCAL });
    renderWithWallet(<NetworkSwitcher />, { connected: true });

    const select = (await screen.findByRole("combobox", {
      name: "Network",
    })) as HTMLSelectElement;
    expect(select).toHaveValue(String(LOCAL));
    const localOption = screen.getByRole("option", {
      name: "Local",
    }) as HTMLOptionElement;
    const ethOption = screen.getByRole("option", {
      name: "Ethereum",
    }) as HTMLOptionElement;
    expect(localOption.selected).toBe(true);
    expect(ethOption.selected).toBe(false);
  });

  it("shows 'Unsupported network' when on a chain that isn't allow-listed", async () => {
    wallet = installFakeWallet({ chainId: 999 });
    renderWithWallet(<NetworkSwitcher />, { connected: true });

    const select = (await screen.findByRole("combobox", {
      name: "Network",
    })) as HTMLSelectElement;
    expect(select).toHaveValue("unsupported");
    expect(
      screen.getByRole("option", { name: "Unsupported network" }),
    ).toBeInTheDocument();
  });

  it("switches to a selected, already-known network via wallet_switchEthereumChain", async () => {
    wallet = installFakeWallet({ chainId: ETH_MAINNET });
    renderWithWallet(<NetworkSwitcher />, { connected: true });

    const select = (await screen.findByRole("combobox", {
      name: "Network",
    })) as HTMLSelectElement;
    await waitFor(() => expect(select).toHaveValue(String(ETH_MAINNET)));

    fireEvent.change(select, { target: { value: String(LOCAL) } });

    // The switch was requested for the target chain...
    await waitFor(() =>
      expect(callsFor(wallet, "wallet_switchEthereumChain")).toContainEqual(
        expect.objectContaining({ params: [{ chainId: LOCAL_HEX }] }),
      ),
    );
    // ...no add-chain needed (the chain was already known)...
    expect(callsFor(wallet, "wallet_addEthereumChain")).toHaveLength(0);
    // ...and the current-network display follows the chainChanged event.
    await waitFor(() => expect(select).toHaveValue(String(LOCAL)));
  });

  it("adds an unknown chain (4902) then retries the switch (Local anvil)", async () => {
    // Wallet is on mainnet and doesn't know the Local chain yet.
    wallet = installFakeWallet({
      chainId: ETH_MAINNET,
      notAddedChains: [LOCAL],
    });
    renderWithWallet(<NetworkSwitcher />, { connected: true });

    const select = (await screen.findByRole("combobox", {
      name: "Network",
    })) as HTMLSelectElement;
    await waitFor(() => expect(select).toHaveValue(String(ETH_MAINNET)));

    fireEvent.change(select, { target: { value: String(LOCAL) } });

    // wallet_addEthereumChain was called with the Local network's EIP-3085 params.
    await waitFor(() =>
      expect(callsFor(wallet, "wallet_addEthereumChain")).toHaveLength(1),
    );
    const addParams = callsFor(wallet, "wallet_addEthereumChain")[0]
      .params?.[0] as {
      chainId: string;
      chainName: string;
      rpcUrls: string[];
    };
    expect(addParams.chainId).toBe(LOCAL_HEX);
    expect(addParams.chainName).toBe("Local (Anvil)");
    expect(addParams.rpcUrls).toEqual(["http://localhost:8545"]);

    // The switch was issued again after the add, and the display now shows Local.
    expect(
      callsFor(wallet, "wallet_switchEthereumChain").length,
    ).toBeGreaterThanOrEqual(2);
    await waitFor(() => expect(select).toHaveValue(String(LOCAL)));
  });

  it("reflects a network change made in the wallet itself (chainChanged)", async () => {
    wallet = installFakeWallet({ chainId: ETH_MAINNET });
    renderWithWallet(<NetworkSwitcher />, { connected: true });

    const select = (await screen.findByRole("combobox", {
      name: "Network",
    })) as HTMLSelectElement;
    await waitFor(() => expect(select).toHaveValue(String(ETH_MAINNET)));

    act(() => wallet.emitChainChanged(LOCAL));
    await waitFor(() => expect(select).toHaveValue(String(LOCAL)));
  });
});
