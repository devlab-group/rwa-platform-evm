import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { encodeErrorResult } from "viem";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";
import { Investor } from "./Investor";
import { api } from "../../lib/client";
import { tokenErrorsAbi } from "../../lib/abis";
import { sendErc20Transfer } from "../../lib/wallet";

vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      getProject: vi.fn(),
      getMyWalletStatus: vi.fn(),
      listRedemptions: vi.fn(),
      listTransactions: vi.fn(),
      isAddressAllowed: vi.fn(),
    },
  };
});

vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    sendErc20Transfer: vi.fn(),
  };
});

const CONNECTED = "0x1111111111111111111111111111111111111111";
const RECIPIENT = "0x2222222222222222222222222222222222222222";
const PROJECT = {
  chainId: 31337,
  decimals: 6,
  tokenUnit: "RWA",
  addresses: {
    token: "0x0000000000000000000000000000000000000001",
    quoteToken: "0x0000000000000000000000000000000000000002",
    vault: "0x0000000000000000000000000000000000000003",
    redemptionEscrow: "0x0000000000000000000000000000000000000004",
  },
};

const INELIGIBLE = "Recipient is ineligible - transfer disabled.";

function fillTransfer(recipient: string, amount: string) {
  fireEvent.change(screen.getByLabelText("Recipient address"), {
    target: { value: recipient },
  });
  fireEvent.change(
    screen.getByLabelText("Amount (whole units)", {
      selector: "#transfer-amount",
    }),
    {
      target: { value: amount },
    },
  );
}

function transferButton() {
  return screen.getByRole("button", { name: "Transfer" });
}

describe("Investor transfer section", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: CONNECTED, chainId: 31337 });
    vi.mocked(api.getProject).mockReset().mockResolvedValue(PROJECT);
    vi.mocked(api.listRedemptions).mockReset().mockResolvedValue({ items: [] });
    vi.mocked(api.listTransactions)
      .mockReset()
      .mockResolvedValue({ items: [] });
    vi.mocked(api.isAddressAllowed).mockReset();
    vi.mocked(sendErc20Transfer)
      .mockReset()
      .mockResolvedValue("0xtransferhash");
    window.localStorage.clear();
  });

  afterEach(() => wallet.uninstall());

  it("enables Transfer only for an explicit allowed: true and sends minimal units to the token", async () => {
    vi.mocked(api.isAddressAllowed).mockResolvedValue({ allowed: true });
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fillTransfer(RECIPIENT, "2.5");
    expect(
      await screen.findByText("Recipient is eligible to receive tokens."),
    ).toBeVisible();
    await waitFor(() => expect(transferButton()).toBeEnabled());
    fireEvent.click(transferButton());

    await waitFor(() => expect(sendErc20Transfer).toHaveBeenCalledTimes(1));
    expect(vi.mocked(sendErc20Transfer).mock.calls[0]).toEqual([
      PROJECT.chainId,
      CONNECTED,
      PROJECT.addresses.token,
      RECIPIENT,
      2_500_000n,
    ]);
    expect(await screen.findByText(/Submitted:/)).toHaveTextContent(
      "0xtransferhash",
    );
  });

  // Fail closed: anything but a literal true keeps the recipient ineligible
  it.each([
    ["an empty response", {}],
    ["allowed: null", { allowed: null }],
    ["allowed: false", { allowed: false }],
    ["a non-boolean allowed", { allowed: "true" }],
  ])(
    "keeps Transfer disabled and shows the ineligible hint for %s",
    async (_name, response) => {
      vi.mocked(api.isAddressAllowed).mockResolvedValue(response as never);
      renderWithWallet(<Investor />, { connected: true });
      await waitFor(() => expect(api.getProject).toHaveBeenCalled());

      fillTransfer(RECIPIENT, "1");
      expect(await screen.findByText(INELIGIBLE)).toBeVisible();
      expect(transferButton()).toBeDisabled();
      expect(sendErc20Transfer).not.toHaveBeenCalled();
    },
  );

  it("keeps Transfer disabled and shows the ineligible hint when the eligibility lookup fails", async () => {
    vi.mocked(api.isAddressAllowed).mockRejectedValue(
      new Error("network down"),
    );
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fillTransfer(RECIPIENT, "1");
    expect(await screen.findByText(INELIGIBLE)).toBeVisible();
    expect(transferButton()).toBeDisabled();
  });

  it("does not look up eligibility until the address is complete", async () => {
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fillTransfer(RECIPIENT.slice(0, 20), "1");
    expect(
      await screen.findByText("Enter a complete address to check eligibility."),
    ).toBeVisible();
    expect(api.isAddressAllowed).not.toHaveBeenCalled();
    expect(transferButton()).toBeDisabled();
  });

  it("rejects an over-precise amount without sending", async () => {
    vi.mocked(api.isAddressAllowed).mockResolvedValue({ allowed: true });
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fillTransfer(RECIPIENT, "1.1234567");
    await waitFor(() => expect(transferButton()).toBeEnabled());
    fireEvent.click(transferButton());

    expect(await screen.findByRole("alert")).toHaveTextContent('"1.1234567"');
    expect(sendErc20Transfer).not.toHaveBeenCalled();
  });

  it("explains a frozen-balance revert and shows no submitted hash", async () => {
    vi.mocked(api.isAddressAllowed).mockResolvedValue({ allowed: true });
    vi.mocked(sendErc20Transfer).mockRejectedValue({
      data: encodeErrorResult({
        abi: tokenErrorsAbi,
        errorName: "ERC7943InsufficientUnfrozenBalance",
        args: [CONNECTED, 5_000_000n, 1_000_000n],
      }),
    });
    renderWithWallet(<Investor />, { connected: true });
    await waitFor(() => expect(api.getProject).toHaveBeenCalled());

    fillTransfer(RECIPIENT, "5");
    await waitFor(() => expect(transferButton()).toBeEnabled());
    fireEvent.click(transferButton());

    expect(
      await screen.findByText(/frozen by the issuer: only 1000000/),
    ).toBeVisible();
    expect(screen.queryByText(/Submitted:/)).toBeNull();
  });
});
