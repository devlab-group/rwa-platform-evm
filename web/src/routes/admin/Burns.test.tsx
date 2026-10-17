import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BurnsSection } from "./Burns";
import { api } from "../../lib/client";
import { connectWallet, sendBurn, waitForTxReceipt } from "../../lib/wallet";
import {
  AUDITOR,
  BURN,
  CONTROLLER,
  PROJECT,
  VAULT,
  signedFor,
} from "../../test/burnFixtures";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";

vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: { ...actual.api, reissueBurn: vi.fn(), downloadBurnPackage: vi.fn() },
  };
});

vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    connectWallet: vi.fn(),
    sendBurn: vi.fn(),
    waitForTxReceipt: vi.fn(),
  };
});

const ADMIN = "0x1111111111111111111111111111111111111111";
const OTHER_AUDITOR = "0x8888888888888888888888888888888888888888";

type Burn = typeof BURN;

function listOf(items: Burn[]) {
  return {
    status: "success" as const,
    data: items,
    reload: vi.fn(),
    loadMore: vi.fn(),
    loadingMore: false,
    loadMoreError: null,
    totalCount: items.length,
    hasMore: false,
  };
}

function file(content: unknown): File {
  return {
    name: "signed-result.json",
    text: () => Promise.resolve(JSON.stringify(content)),
  } as unknown as File;
}

describe("BurnsSection", () => {
  let wallet: ReturnType<typeof installFakeWallet>;
  const onBurned = vi.fn();

  beforeEach(() => {
    vi.resetAllMocks();
    wallet = installFakeWallet({ account: ADMIN, chainId: 31337 });
    vi.mocked(connectWallet).mockResolvedValue({ address: ADMIN } as never);
    vi.mocked(sendBurn).mockResolvedValue("0xburnhash");
    vi.mocked(waitForTxReceipt).mockResolvedValue(undefined);
  });
  afterEach(() => wallet.uninstall());

  function renderSection(
    burns: Burn[],
    project: typeof PROJECT | null = PROJECT,
  ) {
    const list = listOf(burns);
    renderWithWallet(
      <BurnsSection
        burns={list}
        project={project ?? undefined}
        rwaDecimals={18}
        onBurned={onBurned}
      />,
      { connected: true },
    );
    return list;
  }

  async function upload(content: unknown) {
    fireEvent.change(
      await screen.findByLabelText(/Upload signed result for burn 11111111/),
      { target: { files: [file(content)] } },
    );
  }

  it("broadcasts the attested burn to the SupplyController and refreshes", async () => {
    renderSection([BURN]);
    await upload(signedFor(BURN));
    await screen.findByText("Confirm burn");
    // The confirmation shows whole units and the operation being retired
    expect(
      screen.getByText(/0\.25 \(250000000000000000 minimal units\)/),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Confirm and burn" }));

    await screen.findByText(/Burn broadcast/);
    expect(sendBurn).toHaveBeenCalledTimes(1);
    expect(vi.mocked(sendBurn).mock.calls[0]).toEqual([
      31337,
      ADMIN,
      CONTROLLER,
      {
        auditor: AUDITOR,
        profileDigest: PROJECT.profileDigest,
        operationId: BURN.operationId,
        metadataDigest: BURN.metadataDigest,
        amount: 250000000000000000n,
        nonce: 9n,
        validUntil: 4102444800n,
        vault: VAULT,
      },
      signedFor(BURN).signature,
    ]);
    expect(waitForTxReceipt).toHaveBeenCalledWith("0xburnhash");
    expect(screen.getByText("0xburnhash")).toBeInTheDocument();
    expect(onBurned).toHaveBeenCalledTimes(1);
  });

  it("refuses a result signed by another auditor and never opens the confirmation", async () => {
    renderSection([BURN]);
    await upload(signedFor(BURN, { auditor: OTHER_AUDITOR }));
    await screen.findByText(new RegExp(`names ${OTHER_AUDITOR}`));
    expect(screen.queryByText("Confirm burn")).toBeNull();
    expect(sendBurn).not.toHaveBeenCalled();
  });

  it("refuses a mint result uploaded for a burn", async () => {
    renderSection([BURN]);
    await upload(signedFor(BURN, { primaryType: "MintAttestation" }));
    await screen.findByText(/not a BurnAttestation/);
    expect(screen.queryByText("Confirm burn")).toBeNull();
    expect(sendBurn).not.toHaveBeenCalled();
  });

  it("refuses a digest for a different amount", async () => {
    renderSection([BURN]);
    await upload(signedFor({ ...BURN, amount: "1" }));
    await screen.findByText(/different burn digest/);
    expect(sendBurn).not.toHaveBeenCalled();
  });

  it("refuses a file that is not a signed result", async () => {
    renderSection([BURN]);
    await upload({ hello: "world" });
    await screen.findByText(/missing field/);
    expect(sendBurn).not.toHaveBeenCalled();
  });

  it("asks to wait while the project is still loading", async () => {
    renderSection([BURN], null);
    await upload(signedFor(BURN));
    await screen.findByText(/Project still loading/);
    expect(sendBurn).not.toHaveBeenCalled();
  });

  it("shows the wallet failure, keeps the confirmation and does not refresh", async () => {
    vi.mocked(sendBurn).mockRejectedValue(
      new Error("insufficient vault balance"),
    );
    renderSection([BURN]);
    await upload(signedFor(BURN));
    fireEvent.click(
      await screen.findByRole("button", { name: "Confirm and burn" }),
    );

    await screen.findByText(/insufficient vault balance/);
    expect(screen.queryByText(/Burn broadcast/)).toBeNull();
    expect(screen.getByText("Confirm burn")).toBeInTheDocument();
    expect(onBurned).not.toHaveBeenCalled();
  });

  it("does not broadcast when no wallet can be connected", async () => {
    wallet.uninstall();
    vi.mocked(connectWallet).mockRejectedValue(new Error("no wallet"));
    const list = listOf([BURN]);
    renderWithWallet(
      <BurnsSection
        burns={list}
        project={PROJECT}
        rwaDecimals={18}
        onBurned={onBurned}
      />,
    );
    await upload(signedFor(BURN));
    fireEvent.click(
      await screen.findByRole("button", { name: "Confirm and burn" }),
    );

    await screen.findByText(/Connect a wallet on the project chain to burn/);
    expect(sendBurn).not.toHaveBeenCalled();
  });

  it("cancelling the confirmation sends nothing", async () => {
    renderSection([BURN]);
    await upload(signedFor(BURN));
    await screen.findByText("Confirm burn");
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByText("Confirm burn")).toBeNull();
    expect(sendBurn).not.toHaveBeenCalled();
  });

  it("offers Reissue, not upload, for an expired Pending burn and reissues it idempotently", async () => {
    vi.mocked(api.reissueBurn).mockResolvedValue(BURN);
    const expired = { ...BURN, validUntil: 1_000 };
    const list = renderSection([expired]);

    expect(screen.queryByLabelText(/Upload signed result/)).toBeNull();
    fireEvent.click(
      await screen.findByRole("button", { name: /Reissue burn 11111111/ }),
    );

    await waitFor(() => expect(list.reload).toHaveBeenCalledTimes(1));
    expect(api.reissueBurn).toHaveBeenCalledWith(
      BURN.burnId,
      `reissue-${BURN.burnId}-1000`,
    );
  });

  it("shows the reissue failure on its row", async () => {
    vi.mocked(api.reissueBurn).mockRejectedValue(new Error("boom"));
    const list = renderSection([{ ...BURN, validUntil: 1_000 }]);
    fireEvent.click(
      await screen.findByRole("button", { name: /Reissue burn/ }),
    );
    await screen.findByText("Reissue failed.");
    expect(list.reload).not.toHaveBeenCalled();
  });

  it("offers neither upload nor reissue for a Burned burn and lists its transaction", async () => {
    renderSection([{ ...BURN, status: "Burned", burnTxHash: "0xdeadbeef" }]);
    expect(await screen.findByText("0xdeadbeef")).toBeInTheDocument();
    expect(screen.getByText("Burned")).toBeInTheDocument();
    expect(screen.queryByLabelText(/Upload signed result/)).toBeNull();
    expect(screen.queryByRole("button", { name: /Reissue/ })).toBeNull();
  });
});
