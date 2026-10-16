import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  vi,
  type Mock,
} from "vitest";
import { Income } from "./Income";
import { api, ApiError } from "../../lib/client";
import type { components } from "../../lib/api-types";
import { exportSafeBatch } from "../../lib/safeBatch";
import {
  findOnChainDistribution,
  readErc20Allowance,
  sendCreateDistribution,
  sendErc20Approve,
  sendExtendClaimDeadline,
  sendReclaimUnclaimed,
  waitForTxReceipt,
} from "../../lib/wallet";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";

type Distribution = components["schemas"]["IncomeDistribution"];

vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      getProject: vi.fn(),
      listIncomeDistributions: vi.fn(),
      prepareIncomeDistribution: vi.fn(),
    },
  };
});

vi.mock("../../lib/safeBatch", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/safeBatch")>();
  return {
    ...actual,
    exportSafeBatch: vi.fn().mockReturnValue("Saved batch."),
  };
});

vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    readErc20Decimals: vi.fn().mockResolvedValue(6),
    findOnChainDistribution: vi.fn().mockResolvedValue(null),
    readErc20Allowance: vi.fn().mockResolvedValue(0n),
    sendErc20Approve: vi.fn().mockResolvedValue("0xapprove"),
    sendCreateDistribution: vi.fn().mockResolvedValue("0xcreate"),
    sendExtendClaimDeadline: vi.fn().mockResolvedValue("0xextend"),
    sendReclaimUnclaimed: vi.fn().mockResolvedValue("0xreclaim"),
    waitForTxReceipt: vi.fn().mockResolvedValue(undefined),
  };
});

const MANAGER = "0x1111111111111111111111111111111111111111"; // fake wallet account
const DISTRIBUTOR = "0x0000000000000000000000000000000000000080";
const QUOTE = "0x0000000000000000000000000000000000000070";
const SAFE = "0x00000000000000000000000000000000000000a1";
const NOW = Math.floor(Date.now() / 1000);
const DAY = 86_400;

const PROJECT = {
  chainId: 31337,
  version: "rwa-v3",
  paused: false,
  quoteDecimals: 6,
  addresses: { incomeDistributor: DISTRIBUTOR, quoteToken: QUOTE },
  roles: { INCOME_MANAGER_ROLE: [MANAGER] },
};

const dist = (over: Partial<Distribution>): Distribution => ({
  preparationId: "prep-aaaa-1111",
  status: "Prepared",
  merkleRoot: `0x${"ab".repeat(32)}`,
  total: "1000000000",
  recordBlock: 100,
  claimDeadline: NOW + 9 * DAY,
  snapshotURI: "ipfs://bafyexample",
  snapshotSha256: `0x${"cd".repeat(32)}`,
  eligibleSupply: "5",
  distributed: "999999999",
  holderCount: 3,
  createdAt: "2026-10-16T00:00:00Z",
  ...over,
});

function page(items: Distribution[], nextCursor?: string) {
  return { items, totalCount: items.length, nextCursor };
}

describe("Income page", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: MANAGER, chainId: 31337 });
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue(PROJECT as never);
    vi.mocked(api.listIncomeDistributions)
      .mockReset()
      .mockResolvedValue(page([]));
    vi.mocked(api.prepareIncomeDistribution).mockReset();
    for (const fn of [
      sendErc20Approve,
      sendCreateDistribution,
      sendExtendClaimDeadline,
      sendReclaimUnclaimed,
      exportSafeBatch,
    ]) {
      (fn as unknown as Mock).mockClear();
    }
    vi.mocked(waitForTxReceipt).mockClear().mockResolvedValue(undefined);
    vi.mocked(findOnChainDistribution).mockReset().mockResolvedValue(null);
    vi.mocked(readErc20Allowance).mockReset().mockResolvedValue(0n);
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("explains that an rwa-v2 project has no income distribution", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...PROJECT, version: "rwa-v2" } as never);
    renderWithWallet(<Income />, { connected: true });

    await screen.findByText(/needs an rwa-v3 project \(this one is rwa-v2\)/);
    expect(
      screen.queryByRole("button", { name: "Prepare distribution" }),
    ).toBeNull();
  });

  async function fillPrepare(deadlineSeconds: number, total = "1250.5") {
    renderWithWallet(<Income />, { connected: true });
    fireEvent.change(await screen.findByLabelText("Record block"), {
      target: { value: "100" },
    });
    fireEvent.change(screen.getByLabelText(/Total to distribute/), {
      target: { value: total },
    });
    const d = new Date(deadlineSeconds * 1000);
    const pad = (n: number) => String(n).padStart(2, "0");
    fireEvent.change(screen.getByLabelText("Claim deadline"), {
      target: {
        value: `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`,
      },
    });
  }

  it("rejects a deadline under 7 days and 1 hour before calling the server", async () => {
    await fillPrepare(NOW + 7 * DAY);
    fireEvent.click(
      screen.getByRole("button", { name: "Prepare distribution" }),
    );

    await screen.findByText(/at least 7 days and 1 hour/);
    expect(api.prepareIncomeDistribution).not.toHaveBeenCalled();
  });

  it("posts the scaled total with an idempotency key and offers the prepared distribution", async () => {
    const prepared = dist({});
    vi.mocked(api.prepareIncomeDistribution).mockResolvedValue(prepared);
    await fillPrepare(NOW + 9 * DAY);
    fireEvent.click(
      screen.getByRole("button", { name: "Prepare distribution" }),
    );

    await waitFor(() =>
      expect(api.prepareIncomeDistribution).toHaveBeenCalled(),
    );
    const [body, key] = vi.mocked(api.prepareIncomeDistribution).mock.calls[0];
    expect(body).toMatchObject({ recordBlock: 100, total: "1250500000" });
    expect(Math.abs(body.claimDeadline - (NOW + 9 * DAY))).toBeLessThan(120);
    expect(key).toMatch(/^income-100-/);
    // The send panel opens for the new distribution
    expect(
      await screen.findByRole("heading", {
        name: "Send distribution on-chain",
      }),
    ).toBeInTheDocument();
    expect(api.listIncomeDistributions).toHaveBeenCalledTimes(2);
  });

  it("shows the server's rejection", async () => {
    vi.mocked(api.prepareIncomeDistribution).mockRejectedValue(
      new ApiError(409, {
        code: "record_block_not_final",
        message: "Record block is not final yet",
      }),
    );
    await fillPrepare(NOW + 9 * DAY);
    fireEvent.click(
      screen.getByRole("button", { name: "Prepare distribution" }),
    );
    await screen.findByText("Record block is not final yet");
  });

  async function openSend(d: Distribution) {
    vi.mocked(api.listIncomeDistributions)
      .mockReset()
      .mockResolvedValue(page([d]));
    renderWithWallet(<Income />, { connected: true });
    fireEvent.click(
      await screen.findByRole("button", {
        name: /Send distribution .* on-chain/,
      }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Review and send" }),
    );
  }

  it("approves the exact total, then creates the distribution, from the income manager's wallet", async () => {
    const d = dist({});
    await openSend(d);
    // The review shows chain, addresses and amounts before any approval
    const panel = screen
      .getByRole("heading", { name: "Confirm income distribution" })
      .closest(".tx-preview") as HTMLElement;
    expect(within(panel).getByText(DISTRIBUTOR)).toBeInTheDocument();
    expect(within(panel).getByText(QUOTE)).toBeInTheDocument();
    expect(
      within(panel).getByText(/1,000 \(1000000000 minimal units\)/),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Approve and create" }));

    await waitFor(() => expect(sendCreateDistribution).toHaveBeenCalled());
    expect(sendErc20Approve).toHaveBeenCalledWith(
      31337,
      MANAGER,
      QUOTE,
      DISTRIBUTOR,
      1_000_000_000n,
    );
    expect(sendCreateDistribution).toHaveBeenCalledWith(
      31337,
      MANAGER,
      DISTRIBUTOR,
      {
        merkleRoot: d.merkleRoot,
        total: 1_000_000_000n,
        recordBlock: 100n,
        claimDeadline: BigInt(d.claimDeadline),
        snapshotURI: "ipfs://bafyexample",
        snapshotSha256: d.snapshotSha256,
      },
    );
    await screen.findByText(/Distribution created/);
    // Approval is mined before the create is sent
    expect(
      vi.mocked(sendErc20Approve).mock.invocationCallOrder[0],
    ).toBeLessThan(
      vi.mocked(sendCreateDistribution).mock.invocationCallOrder[0],
    );
  });

  it("does not send createDistribution when the approval fails", async () => {
    vi.mocked(sendErc20Approve).mockRejectedValueOnce(
      new Error("User rejected the request."),
    );
    await openSend(dist({}));
    fireEvent.click(screen.getByRole("button", { name: "Approve and create" }));

    await screen.findByText("User rejected the request.");
    expect(sendCreateDistribution).not.toHaveBeenCalled();
  });

  it("says the approval stays in place when createDistribution fails after it", async () => {
    vi.mocked(sendCreateDistribution).mockRejectedValueOnce(new Error("boom"));
    await openSend(dist({}));
    fireEvent.click(screen.getByRole("button", { name: "Approve and create" }));

    await screen.findByText(
      /approval went through but createDistribution failed: boom/,
    );
  });

  // A retry after a failed create must not approve again: a USDT-style token reverts when a
  // nonzero allowance is changed
  it("reuses an allowance that already covers the total", async () => {
    vi.mocked(readErc20Allowance).mockResolvedValue(1_000_000_000n);
    await openSend(dist({}));
    fireEvent.click(screen.getByRole("button", { name: "Approve and create" }));

    await screen.findByText(/Distribution created/);
    expect(readErc20Allowance).toHaveBeenCalledWith(
      31337,
      QUOTE,
      MANAGER,
      DISTRIBUTOR,
    );
    expect(sendErc20Approve).not.toHaveBeenCalled();
    expect(sendCreateDistribution).toHaveBeenCalled();
  });

  // The row stays Prepared until the server indexes the creation; the contract would accept
  // and fund the same snapshot again
  it("refuses to create a snapshot that an on-chain distribution already pays", async () => {
    vi.mocked(findOnChainDistribution).mockResolvedValue(3n);
    const d = dist({});
    await openSend(d);
    fireEvent.click(screen.getByRole("button", { name: "Approve and create" }));

    await screen.findByText(/Distribution 3 already pays this snapshot/);
    expect(findOnChainDistribution).toHaveBeenCalledWith(
      31337,
      DISTRIBUTOR,
      d.merkleRoot,
      d.snapshotSha256,
    );
    expect(sendErc20Approve).not.toHaveBeenCalled();
    expect(sendCreateDistribution).not.toHaveBeenCalled();
  });

  it("refuses to send when the existing-distribution check cannot read the chain", async () => {
    vi.mocked(findOnChainDistribution).mockRejectedValue(
      new Error("wrong network"),
    );
    await openSend(dist({}));
    fireEvent.click(screen.getByRole("button", { name: "Approve and create" }));

    await screen.findByText(/Could not check the income distributor/);
    expect(sendErc20Approve).not.toHaveBeenCalled();
  });

  it("refuses to send once the deadline is too close to the 7-day minimum", async () => {
    await openSend(dist({ claimDeadline: NOW + 7 * DAY + 10 }));
    fireEvent.click(screen.getByRole("button", { name: "Approve and create" }));

    await screen.findByText(/too close/);
    expect(sendErc20Approve).not.toHaveBeenCalled();
  });

  it("gates the send panel on INCOME_MANAGER_ROLE and exports a Safe batch for a Safe holder", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({
        ...PROJECT,
        roles: { INCOME_MANAGER_ROLE: [SAFE] },
      } as never);
    vi.mocked(api.listIncomeDistributions)
      .mockReset()
      .mockResolvedValue(page([dist({})]));
    renderWithWallet(<Income />, { connected: true });
    fireEvent.click(
      await screen.findByRole("button", {
        name: /Send distribution .* on-chain/,
      }),
    );

    const heading = await screen.findByRole("heading", {
      name: "Send distribution on-chain",
    });
    const section = heading.closest("section") as HTMLElement;
    // The connected wallet lacks the role, so nothing is sendable until a Safe is picked
    expect(
      within(section).getByText("INCOME_MANAGER_ROLE"),
    ).toBeInTheDocument();
    expect(
      within(section).queryByRole("button", { name: "Review and send" }),
    ).toBeNull();

    fireEvent.change(within(section).getByRole("combobox"), {
      target: { value: SAFE },
    });
    fireEvent.click(
      within(section).getByRole("button", { name: "Review and send" }),
    );
    fireEvent.click(
      within(section).getByRole("button", { name: "Save Safe batch" }),
    );

    await waitFor(() => expect(exportSafeBatch).toHaveBeenCalled());
    const [chainId, safe, , calls] = vi.mocked(exportSafeBatch).mock.calls[0];
    expect([chainId, safe]).toEqual([31337, SAFE]);
    expect(calls.map((c) => c.to)).toEqual([QUOTE, DISTRIBUTOR]);
    expect(sendErc20Approve).not.toHaveBeenCalled();
  });

  // A Safe export needs no injected wallet, so an unreadable chain warns the owners instead of
  // blocking the export
  it("exports a Safe batch with a warning when the existing-distribution check cannot run", async () => {
    vi.mocked(findOnChainDistribution).mockRejectedValue(
      new Error("No injected wallet found."),
    );
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({
        ...PROJECT,
        roles: { INCOME_MANAGER_ROLE: [SAFE] },
      } as never);
    vi.mocked(api.listIncomeDistributions)
      .mockReset()
      .mockResolvedValue(page([dist({})]));
    renderWithWallet(<Income />, { connected: true });
    fireEvent.click(
      await screen.findByRole("button", {
        name: /Send distribution .* on-chain/,
      }),
    );
    const section = (
      await screen.findByRole("heading", { name: "Send distribution on-chain" })
    ).closest("section") as HTMLElement;
    fireEvent.change(within(section).getByRole("combobox"), {
      target: { value: SAFE },
    });
    fireEvent.click(
      within(section).getByRole("button", { name: "Review and send" }),
    );
    fireEvent.click(
      within(section).getByRole("button", { name: "Save Safe batch" }),
    );

    await within(section).findByText(
      /could not check the income distributor .*Before executing, confirm/,
    );
    expect(exportSafeBatch).toHaveBeenCalled();
  });

  // An exported batch is executed later by the owners; a second one for a snapshot that is
  // already on-chain would pay it again
  it("does not export a Safe batch for a snapshot that is already on-chain", async () => {
    vi.mocked(findOnChainDistribution).mockResolvedValue(0n);
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({
        ...PROJECT,
        roles: { INCOME_MANAGER_ROLE: [SAFE] },
      } as never);
    vi.mocked(api.listIncomeDistributions)
      .mockReset()
      .mockResolvedValue(page([dist({})]));
    renderWithWallet(<Income />, { connected: true });
    fireEvent.click(
      await screen.findByRole("button", {
        name: /Send distribution .* on-chain/,
      }),
    );
    const section = (
      await screen.findByRole("heading", { name: "Send distribution on-chain" })
    ).closest("section") as HTMLElement;
    fireEvent.change(within(section).getByRole("combobox"), {
      target: { value: SAFE },
    });
    fireEvent.click(
      within(section).getByRole("button", { name: "Review and send" }),
    );
    fireEvent.click(
      within(section).getByRole("button", { name: "Save Safe batch" }),
    );

    await within(section).findByText(
      /Distribution 0 already pays this snapshot/,
    );
    expect(exportSafeBatch).not.toHaveBeenCalled();
  });

  it("lists distributions with totals, claimed and snapshot, linking only https snapshots", async () => {
    vi.mocked(api.listIncomeDistributions)
      .mockReset()
      .mockResolvedValue(
        page([
          dist({
            preparationId: "p1",
            status: "Active",
            distributionId: "4",
            claimed: "250000000",
            snapshotURI: "https://gw.example/snap.json",
          }),
          dist({
            preparationId: "p2",
            status: "Prepared",
            snapshotURI: "javascript:alert(1)",
          }),
        ]),
      );
    renderWithWallet(<Income />, { connected: true });

    const link = await screen.findByRole("link", {
      name: "https://gw.example/snap.json",
    });
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(screen.getByText("javascript:alert(1)").closest("a")).toBeNull();
    const active = screen.getByText("Active").closest("tr") as HTMLElement;
    expect(within(active).getByText("1,000")).toBeInTheDocument();
    expect(within(active).getByText("250")).toBeInTheDocument();
    expect(
      within(active).getByText(`0x${"cd".repeat(32)}`),
    ).toBeInTheDocument();
  });

  it("loads the next page of distributions", async () => {
    vi.mocked(api.listIncomeDistributions)
      .mockReset()
      .mockResolvedValueOnce(page([dist({ preparationId: "p1" })], "cur-2"))
      .mockResolvedValueOnce(
        page([
          dist({
            preparationId: "p2",
            status: "Reclaimed",
            distributionId: "1",
          }),
        ]),
      );
    renderWithWallet(<Income />, { connected: true });

    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await screen.findByText("Reclaimed");
    expect(
      vi.mocked(api.listIncomeDistributions).mock.calls[1][0],
    ).toMatchObject({
      cursor: "cur-2",
    });
  });

  it("extends a deadline from the income manager's wallet, later and in the future only", async () => {
    const d = dist({ status: "Active", distributionId: "4" });
    vi.mocked(api.listIncomeDistributions)
      .mockReset()
      .mockResolvedValue(page([d]));
    renderWithWallet(<Income />, { connected: true });
    fireEvent.click(
      await screen.findByRole("button", {
        name: /Extend deadline of distribution 4/,
      }),
    );

    const input = await screen.findByLabelText("New claim deadline");
    const local = (s: number) => {
      const x = new Date(s * 1000);
      const pad = (n: number) => String(n).padStart(2, "0");
      return `${x.getFullYear()}-${pad(x.getMonth() + 1)}-${pad(x.getDate())}T${pad(x.getHours())}:${pad(x.getMinutes())}`;
    };
    const extend = () =>
      fireEvent.click(screen.getByRole("button", { name: "Extend deadline" }));

    fireEvent.change(input, {
      target: { value: local(d.claimDeadline - DAY) },
    });
    extend();
    await screen.findByText(/must be later than the current one/);
    expect(sendExtendClaimDeadline).not.toHaveBeenCalled();

    const later = d.claimDeadline + 5 * DAY;
    fireEvent.change(input, { target: { value: local(later) } });
    extend();
    await waitFor(() => expect(sendExtendClaimDeadline).toHaveBeenCalled());
    const args = vi.mocked(sendExtendClaimDeadline).mock.calls[0];
    expect(args.slice(0, 4)).toEqual([31337, MANAGER, DISTRIBUTOR, 4n]);
    await screen.findByText("Claim deadline extended.");
    expect(Math.abs(Number(args[4]) - later)).toBeLessThan(120);
  });

  it("reclaims after the deadline from any wallet, and not before it", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...PROJECT, roles: {} } as never);
    vi.mocked(api.listIncomeDistributions)
      .mockReset()
      .mockResolvedValue(
        page([
          dist({
            preparationId: "open",
            status: "Active",
            distributionId: "1",
          }),
          dist({
            preparationId: "done",
            status: "Expired",
            distributionId: "2",
            claimDeadline: NOW - DAY,
          }),
        ]),
      );
    renderWithWallet(<Income />, { connected: true });

    const open = await screen.findByRole("button", {
      name: /Reclaim unclaimed income of distribution 1/,
    });
    expect(open).toBeDisabled();
    const ready = screen.getByRole("button", {
      name: /Reclaim unclaimed income of distribution 2/,
    });
    expect(ready).toBeEnabled();
    fireEvent.click(ready);

    await waitFor(() =>
      expect(sendReclaimUnclaimed).toHaveBeenCalledWith(
        31337,
        MANAGER,
        DISTRIBUTOR,
        2n,
      ),
    );
    await screen.findByText(/Unclaimed income reclaimed/);
  });

  it("disables reclaim while the project is paused", async () => {
    vi.mocked(api.getProject)
      .mockReset()
      .mockResolvedValue({ ...PROJECT, paused: true } as never);
    vi.mocked(api.listIncomeDistributions)
      .mockReset()
      .mockResolvedValue(
        page([
          dist({
            status: "Expired",
            distributionId: "2",
            claimDeadline: NOW - DAY,
          }),
        ]),
      );
    renderWithWallet(<Income />, { connected: true });

    const reclaim = await screen.findByRole("button", {
      name: /Reclaim unclaimed income of distribution 2/,
    });
    expect(reclaim).toBeDisabled();
    expect(reclaim).toHaveAttribute("title", expect.stringMatching(/paused/));
  });

  it("offers no reclaim for a prepared or reclaimed distribution", async () => {
    vi.mocked(api.listIncomeDistributions)
      .mockReset()
      .mockResolvedValue(
        page([
          dist({ preparationId: "a", status: "Prepared" }),
          dist({
            preparationId: "b",
            status: "Reclaimed",
            distributionId: "3",
            claimDeadline: NOW - DAY,
          }),
        ]),
      );
    renderWithWallet(<Income />, { connected: true });
    await screen.findByText("Reclaimed");
    expect(
      screen.queryByRole("button", { name: /Reclaim unclaimed/ }),
    ).toBeNull();
  });
});
