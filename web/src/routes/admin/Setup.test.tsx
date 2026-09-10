import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { keccak256, toBytes } from "viem";
import { Setup } from "./Setup";
import { api, ApiError } from "../../lib/client";
import {
  readErc20Decimals,
  sendDeployProject,
  waitForTxReceipt,
} from "../../lib/wallet";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";

// Deployment is broadcast from the admin's wallet (RWAFactory.deploy), not sent
// to the server. The form takes the two prices in WHOLE quote-token units and
// must convert them to the quote token's minimal units before assembling
// ProjectConfig. The project doesn't exist yet, so the quote decimals are read
// directly on-chain from the entered quoteToken address (18 here, so a correct
// conversion is distinguishable from a no-op). A missing/unreadable quote token
// or an over-precise price must block the deploy, not submit a mis-scaled one.
vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      getProject: vi.fn(),
      getProfile: vi.fn(),
      getConfig: vi.fn(),
      validateProfile: vi.fn(),
      createProfile: vi.fn(),
    },
  };
});

// readErc20Decimals is stubbed; sendDeployProject/waitForTxReceipt are spied so
// the deploy tests can assert the exact broadcast (chain, factory, ProjectConfig)
// without a chain. The chainId + factory address come from api.getConfig().
vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    readErc20Decimals: vi.fn().mockResolvedValue(18),
    sendDeployProject: vi.fn().mockResolvedValue("0xdeployhash"),
    waitForTxReceipt: vi.fn().mockResolvedValue(undefined),
  };
});

const ADDR = (n: number) =>
  `0x${n.toString(16).padStart(40, "0")}` as `0x${string}`;

const FACTORY = ADDR(0xfac);
// The projectId comes from GET /api/v1/config (the server config), so getConfig
// is mocked to return this fixed UUID and the on-chain projectId (keccak256 of
// the UUID string) is deterministic and assertable.
const FIXED_UUID = "12345678-1234-1234-1234-123456789abc";

// Every required address field + name/symbol; prices default to "1" whole unit.
async function reachDeployFormAndFill(
  prices: { purchase: string; redemption: string } = {
    purchase: "1",
    redemption: "1",
  },
) {
  renderWithWallet(<Setup />, { connected: true });
  // The profile loads on mount (mocked 404 → no profile) and the projectId
  // loads from GET /config; the Validate button stays disabled until the
  // projectId arrives, so wait for it to enable before driving the form.
  const validateBtn = await screen.findByRole("button", {
    name: "Validate profile",
  });
  await waitFor(() => expect(validateBtn).toBeEnabled());
  fireEvent.click(validateBtn);
  await waitFor(() => expect(api.validateProfile).toHaveBeenCalled());
  fireEvent.click(
    screen.getByRole("button", { name: "Create & persist profile" }),
  );
  await screen.findByRole("button", { name: "Review deployment" });

  const fill = (label: string, value: string) =>
    fireEvent.change(screen.getByLabelText(label), { target: { value } });

  fill("Name", "Acme");
  fill("Symbol", "ACME");
  fill("Quote token address", ADDR(2));
  fill(
    "Purchase price per whole token (whole quote-token units)",
    prices.purchase,
  );
  fill(
    "Redemption price per whole token (whole quote-token units)",
    prices.redemption,
  );
  fill("Admin address", ADDR(3));
  fill("Auditor address", ADDR(4));
  fill("Compliance operator address", ADDR(5));
  fill("Pricer address", ADDR(6));
  fill("Treasurer address", ADDR(7));
  fill("Redemption manager address", ADDR(8));
  fill("Treasury address", ADDR(9));

  fireEvent.click(screen.getByRole("button", { name: "Review deployment" }));
  await screen.findByRole("button", { name: "Confirm deploy" });
}

describe("Setup deploy price conversion", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    vi.mocked(api.getProject).mockReset().mockRejectedValue(new Error("404"));
    // No stored profile by default — the create flow is what these exercise.
    vi.mocked(api.getProfile)
      .mockReset()
      .mockRejectedValue(
        new ApiError(404, { code: "not_found", message: "no profile" }),
      );
    vi.mocked(api.getConfig).mockReset().mockResolvedValue({
      chainId: 31337,
      factoryAddress: FACTORY,
      projectId: FIXED_UUID,
    });
    vi.mocked(api.validateProfile)
      .mockReset()
      .mockResolvedValue({ valid: true, profileDigest: "0xdig", cid: "cid" });
    vi.mocked(api.createProfile)
      .mockReset()
      .mockResolvedValue({ profileDigest: "0xdig", cid: "cid" });
    vi.mocked(sendDeployProject).mockClear().mockResolvedValue("0xdeployhash");
    vi.mocked(waitForTxReceipt).mockClear().mockResolvedValue(undefined);
    vi.mocked(readErc20Decimals).mockClear().mockResolvedValue(18);
    // Admin is wallet-connected on chain 31337 — the deploy form reads the
    // quote decimals against that chain and broadcasts from that wallet.
    wallet = installFakeWallet({ chainId: 31337 });
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("broadcasts a well-formed ProjectConfig with prices scaled by the on-chain quote decimals", async () => {
    await reachDeployFormAndFill({ purchase: "1", redemption: "2.5" });

    fireEvent.click(screen.getByRole("button", { name: "Confirm deploy" }));

    await waitFor(() => expect(sendDeployProject).toHaveBeenCalled());
    // Factory address + chain come from GET /config; decimals are read on-chain
    // from the entered quoteToken (against the config chain), not assumed.
    expect(api.getConfig).toHaveBeenCalled();
    expect(readErc20Decimals).toHaveBeenCalledWith(31337, ADDR(2));
    const [chainId, from, factory, config] =
      vi.mocked(sendDeployProject).mock.calls[0];
    expect(chainId).toBe(31337);
    expect(from).toBe(wallet.account);
    expect(factory).toBe(FACTORY);
    // projectId is the keccak256 of the profile's UUID string (server parity).
    expect(config.projectId).toBe(keccak256(toBytes(FIXED_UUID)));
    // 1 and 2.5 whole quote-token units at 18 decimals, as bigint.
    expect(config.purchasePricePerWholeToken).toBe(1000000000000000000n);
    expect(config.redemptionPricePerWholeToken).toBe(2500000000000000000n);
    // Every required address field carried through from the form.
    expect(config.quoteToken).toBe(ADDR(2));
    expect(config.admin).toBe(ADDR(3));
    expect(config.auditor).toBe(ADDR(4));
    expect(config.treasury).toBe(ADDR(9));
  });

  it("blocks the deploy when the quote token's decimals can't be read on-chain", async () => {
    vi.mocked(readErc20Decimals).mockRejectedValue(new Error("not a contract"));
    await reachDeployFormAndFill();

    fireEvent.click(screen.getByRole("button", { name: "Confirm deploy" }));

    await screen.findByText(/couldn't read the quote token's decimals/i);
    expect(sendDeployProject).not.toHaveBeenCalled();
  });

  it("blocks the deploy when a price has more precision than the quote token allows", async () => {
    // Quote token has 6 decimals but the operator typed 7 fractional digits —
    // parseUnits would silently round, so it must be rejected instead.
    vi.mocked(readErc20Decimals).mockResolvedValue(6);
    await reachDeployFormAndFill({ purchase: "1.2345678", redemption: "1" });

    fireEvent.click(screen.getByRole("button", { name: "Confirm deploy" }));

    await screen.findByText(/not a valid amount/i);
    expect(sendDeployProject).not.toHaveBeenCalled();
  });
});

describe("Setup load-existing-profile on mount", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    vi.mocked(api.getProject).mockReset().mockRejectedValue(new Error("404"));
    vi.mocked(api.getProfile).mockReset();
    vi.mocked(api.createProfile).mockReset();
    vi.mocked(api.getConfig).mockReset().mockResolvedValue({
      chainId: 31337,
      factoryAddress: FACTORY,
      projectId: "proj-1",
    });
    wallet = installFakeWallet({ chainId: 31337 });
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("prefills a persisted profile and unlocks Deployment without re-creating", async () => {
    vi.mocked(api.getProfile).mockResolvedValue({
      // The raw profile is an untyped object in the OpenAPI schema
      // (Record<string, never> once generated), so cast the concrete document.
      profile: {
        profileVersion: "1.0",
        projectId: "proj-1",
        assetType: "gold",
        tokenUnit: "OZ",
        tokenDecimals: 6,
      } as unknown as Record<string, never>,
      projectId: "proj-1",
      profileDigest: "0xstoreddigest",
      cid: "cid-stored",
      decimals: 6,
      tokenUnit: "OZ",
    });

    renderWithWallet(<Setup />, { connected: true });

    // The persisted profile view (not the empty create form) is shown, and the
    // raw JSON is visible read-only.
    await screen.findByText(
      /Persisted — immutable for the life of this deployment/i,
    );
    const storedJson = screen.getByLabelText(
      "Stored Asset Profile JSON",
    ) as HTMLTextAreaElement;
    expect(storedJson.value).toContain("proj-1");
    expect(storedJson).toHaveAttribute("readonly");

    // Deployment is unlocked (Review deployment is the deploy form's first
    // button) without any re-creation.
    await screen.findByRole("button", { name: "Review deployment" });
    expect(
      screen.queryByRole("button", { name: "Validate profile" }),
    ).toBeNull();
    expect(api.createProfile).not.toHaveBeenCalled();
  });

  it("shows the empty create form when no profile is stored yet (404)", async () => {
    vi.mocked(api.getProfile).mockRejectedValue(
      new ApiError(404, { code: "not_found", message: "no profile" }),
    );

    renderWithWallet(<Setup />, { connected: true });

    // The create flow is shown, and Deployment stays locked behind it.
    await screen.findByRole("button", { name: "Validate profile" });
    expect(
      screen.getByText(
        /Create and persist an Asset Profile above before deploying/i,
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Review deployment" }),
    ).toBeNull();
  });
});

/** A stored profile so the deploy gate has a profile to (potentially) deploy. */
function storedProfileResponse() {
  return {
    profile: {
      profileVersion: "1.0",
      projectId: "proj-1",
      tokenUnit: "OZ",
      tokenDecimals: 6,
    } as unknown as Record<string, never>,
    projectId: "proj-1",
    profileDigest: "0xdig",
    cid: "cid",
    decimals: 6,
    tokenUnit: "OZ",
  };
}

const DEPLOY_POLL_MS = 3000; // must match Setup.tsx

describe("Setup deployment status gating", () => {
  beforeEach(() => {
    vi.mocked(api.getProject).mockReset();
    vi.mocked(api.getProfile)
      .mockReset()
      .mockResolvedValue(storedProfileResponse());
    vi.mocked(api.getConfig).mockReset().mockResolvedValue({
      chainId: 31337,
      factoryAddress: FACTORY,
      projectId: "proj-1",
    });
  });

  it("shows the deploy form when the project is not yet deployed (Undeployed)", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      projectId: "proj-1",
      chainId: 31337,
      status: "Undeployed",
    });

    renderWithWallet(<Setup />);

    await screen.findByRole("button", { name: "Review deployment" });
    expect(screen.queryByText(/Deployment in progress/i)).toBeNull();
  });

  it("hides the deploy form and shows an in-progress indicator while Deploying/Verifying", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      projectId: "proj-1",
      chainId: 31337,
      status: "Verifying",
    });

    renderWithWallet(<Setup />);

    await screen.findByText(/Deployment in progress — Verifying/i);
    expect(
      screen.queryByRole("button", { name: "Review deployment" }),
    ).toBeNull();
  });

  it("hides the deploy form and shows a deployed confirmation when Active", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      projectId: "proj-1",
      chainId: 31337,
      status: "Active",
    });

    renderWithWallet(<Setup />);

    await screen.findByText(/Deployed —/i);
    expect(
      screen.queryByRole("button", { name: "Review deployment" }),
    ).toBeNull();
  });

  it("shows the failure with its note and re-shows the deploy form when Failed", async () => {
    vi.mocked(api.getProject).mockResolvedValue({
      projectId: "proj-1",
      chainId: 31337,
      status: "Failed",
      verificationNote: "bytecode mismatch",
    });

    renderWithWallet(<Setup />);

    await screen.findByText(/Deployment failed: bytecode mismatch/i);
    // The form is available again so the admin can retry.
    await screen.findByRole("button", { name: "Review deployment" });
  });

  it("polls GET /project while Deploying and flips to the deployed state on Active", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(api.getProject)
        .mockResolvedValueOnce({
          projectId: "proj-1",
          chainId: 31337,
          status: "Deploying",
        })
        .mockResolvedValue({
          projectId: "proj-1",
          chainId: 31337,
          status: "Active",
        });

      renderWithWallet(<Setup />);

      // Flush the initial loads → Deploying (form hidden, indicator shown).
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(
        screen.getByText(/Deployment in progress — Deploying/i),
      ).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "Review deployment" }),
      ).toBeNull();
      expect(api.getProject).toHaveBeenCalledTimes(1);

      // One poll interval later, GET /project reports Active → UI flips.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(DEPLOY_POLL_MS);
      });
      expect(api.getProject).toHaveBeenCalledTimes(2);
      expect(screen.getByText(/Deployed —/i)).toBeInTheDocument();
      expect(screen.queryByText(/Deployment in progress/i)).toBeNull();

      // Terminal state → polling stops (no further GET /project calls).
      await act(async () => {
        await vi.advanceTimersByTimeAsync(DEPLOY_POLL_MS * 3);
      });
      expect(api.getProject).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });
});
