import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
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

// Deployment prices scale by on-chain quote decimals before ProjectConfig assembly;
// the 18-decimal fixture catches unscaled inputs. Missing/unreadable quote tokens and
// excess precision block deployment.
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

// Stub quote decimals and spy on deploy/receipt; assert chain/factory from getConfig and exact
// ProjectConfig
vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    readErc20Decimals: vi.fn(async () => 18),
    sendDeployProject: vi.fn(async () => "0xdeployhash"),
    waitForTxReceipt: vi.fn(async () => undefined),
  };
});

const ADDR = (n: number) =>
  `0x${n.toString(16).padStart(40, "0")}` as `0x${string}`;

const FACTORY = ADDR(0xfac);
// Fix getConfig projectId so its UUID keccak256 is deterministic
const FIXED_UUID = "12345678-1234-1234-1234-123456789abc";

// Restore the undeployed project and vi.fn wallet defaults before each test
beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.getProject).mockRejectedValue(new Error("404"));
});

// Every required address field plus name and symbol; each price defaults to 1 whole unit
async function reachDeployFormAndFill(
  prices: { purchase: string; redemption: string } = {
    purchase: "1",
    redemption: "1",
  },
  tokenDecimals?: number,
) {
  renderWithWallet(<Setup />, { connected: true });
  if (tokenDecimals !== undefined) {
    fireEvent.change(await screen.findByLabelText("Token decimals"), {
      target: { value: String(tokenDecimals) },
    });
  }
  // Wait for configured projectId to enable Validate; profile lookup returns 404 for creation tests
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
    // No stored profile by default; these tests exercise the create flow
    vi.mocked(api.getProfile)
      .mockReset()
      .mockRejectedValue(
        new ApiError(404, { code: "not_found", message: "no profile" }),
      );
    vi.mocked(api.getConfig).mockReset().mockResolvedValue({
      chainId: 31337,
      factoryAddress: FACTORY,
      factoryVersion: "rwa-v2",
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
    // Connected chain 31337 supplies quote-decimal reads and deployment
    wallet = installFakeWallet({ chainId: 31337 });
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("broadcasts a well-formed ProjectConfig with prices scaled by the on-chain quote decimals", async () => {
    await reachDeployFormAndFill({ purchase: "1", redemption: "2.5" }, 8);

    fireEvent.click(screen.getByRole("button", { name: "Confirm deploy" }));

    await waitFor(() => expect(sendDeployProject).toHaveBeenCalled());
    // Use getConfig chain/factory and read entered quoteToken decimals on that chain
    expect(api.getConfig).toHaveBeenCalled();
    expect(readErc20Decimals).toHaveBeenCalledWith(31337, ADDR(2));
    const [chainId, from, factory, config] =
      vi.mocked(sendDeployProject).mock.calls[0];
    expect(chainId).toBe(31337);
    expect(from).toBe(wallet.account);
    expect(factory).toBe(FACTORY);
    // projectId is the keccak256 of the profile's UUID string, as on the server
    expect(config.projectId).toBe(keccak256(toBytes(FIXED_UUID)));
    // 1 and 2.5 whole quote-token units at 18 decimals, as bigint
    expect(config.purchasePricePerWholeToken).toBe(1000000000000000000n);
    expect(config.redemptionPricePerWholeToken).toBe(2500000000000000000n);
    // Every required address field carries through from the form
    expect(config.quoteToken).toBe(ADDR(2));
    expect(config.admin).toBe(ADDR(3));
    expect(config.auditor).toBe(ADDR(4));
    expect(config.treasury).toBe(ADDR(9));
    // Decimals and digest come from the profile the admin just persisted
    expect(config.decimals).toBe(8);
    expect(config.profileDigest).toBe("0xdig");
  });

  it("blocks the deploy when the quote token's decimals can't be read on-chain", async () => {
    vi.mocked(readErc20Decimals).mockRejectedValue(new Error("not a contract"));
    await reachDeployFormAndFill();

    fireEvent.click(screen.getByRole("button", { name: "Confirm deploy" }));

    await screen.findByText(/Cannot read quote-token decimals/i);
    expect(sendDeployProject).not.toHaveBeenCalled();
  });

  it("blocks the deploy when a price has more precision than the quote token allows", async () => {
    // Reject 7 fractional digits on a 6-decimal quote token before parseUnits rounds
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
      factoryVersion: "rwa-v2",
      projectId: "proj-1",
    });
    wallet = installFakeWallet({ chainId: 31337 });
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("prefills a persisted profile and unlocks Deployment without re-creating", async () => {
    vi.mocked(api.getProfile).mockResolvedValue({
      // The OpenAPI schema types the raw profile as Record<string, never>, so
      // cast the concrete document
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

    // Show stored profile with read-only JSON instead of creation form
    await screen.findByText(/Persisted - immutable for this project/i);
    const storedJson = screen.getByLabelText(
      "Stored Asset Profile JSON",
    ) as HTMLTextAreaElement;
    expect(storedJson.value).toContain("proj-1");
    expect(storedJson).toHaveAttribute("readonly");

    // Unlock Review deployment without recreating the profile
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

    // The create flow shows, and Deployment stays locked
    await screen.findByRole("button", { name: "Validate profile" });
    expect(
      screen.getByText(/Create the Asset Profile above to supply project ID/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Review deployment" }),
    ).toBeNull();
  });
});

// Supply a stored profile to unlock deployment
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

const DEPLOY_POLL_MS = 3000; // matches Setup.tsx

describe("Setup deployment status gating", () => {
  beforeEach(() => {
    vi.mocked(api.getProject).mockReset();
    vi.mocked(api.getProfile)
      .mockReset()
      .mockResolvedValue(storedProfileResponse());
    vi.mocked(api.getConfig).mockReset().mockResolvedValue({
      chainId: 31337,
      factoryAddress: FACTORY,
      factoryVersion: "rwa-v2",
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

    await screen.findByText(/Deployment in progress - Verifying/i);
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

    await screen.findByText(/Deployed -/i);
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
    // The form shows again so the admin can retry
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

      // Flush loads; Deploying shows progress in place of the form
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(
        screen.getByText(/Deployment in progress - Deploying/i),
      ).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "Review deployment" }),
      ).toBeNull();
      expect(api.getProject).toHaveBeenCalledTimes(1);

      // A poll returning Active updates the UI
      await act(async () => {
        await vi.advanceTimersByTimeAsync(DEPLOY_POLL_MS);
      });
      expect(api.getProject).toHaveBeenCalledTimes(2);
      expect(screen.getByText(/Deployed -/i)).toBeInTheDocument();
      expect(screen.queryByText(/Deployment in progress/i)).toBeNull();

      // Active stops Setup's deployment polling
      await act(async () => {
        await vi.advanceTimersByTimeAsync(DEPLOY_POLL_MS * 3);
      });
      expect(api.getProject).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("Setup legal descriptor", () => {
  async function openForm() {
    vi.mocked(api.getProfile).mockRejectedValue(
      new ApiError(404, { code: "not_found", message: "none" }),
    );
    vi.mocked(api.getConfig).mockResolvedValue({
      projectId: FIXED_UUID,
    } as never);
    vi.mocked(api.validateProfile).mockResolvedValue({
      valid: true,
      errors: [],
    } as never);
    renderWithWallet(<Setup />, { connected: true });
    const btn = await screen.findByRole("button", { name: "Validate profile" });
    await waitFor(() => expect(btn).toBeEnabled());
    return btn;
  }

  it("omits legal when the wrapper is None", async () => {
    const btn = await openForm();
    fireEvent.click(btn);
    await waitFor(() => expect(api.validateProfile).toHaveBeenCalled());
    const sent = vi.mocked(api.validateProfile).mock.calls.at(-1)![0];
    expect("legal" in sent).toBe(false);
  });

  it("sends only the filled legal members", async () => {
    const btn = await openForm();
    fireEvent.change(screen.getByLabelText("Wrapper"), {
      target: { value: "share" },
    });
    fireEvent.change(screen.getByLabelText("Holder rights"), {
      target: { value: "Pro-rata claim" },
    });
    fireEvent.change(screen.getByLabelText("Maturity date"), {
      target: { value: "2030-12-31" },
    });
    fireEvent.click(btn);
    await waitFor(() => expect(api.validateProfile).toHaveBeenCalled());
    const sent = vi
      .mocked(api.validateProfile)
      .mock.calls.at(-1)![0] as unknown as {
      legal: unknown;
    };
    expect(sent.legal).toEqual({
      wrapper: "share",
      holderRights: "Pro-rata claim",
      maturityDate: "2030-12-31",
    });
  });

  it("shows a stored descriptor and links only https references", async () => {
    vi.mocked(api.getConfig).mockResolvedValue({
      projectId: FIXED_UUID,
    } as never);
    vi.mocked(api.getProfile).mockResolvedValue({
      profileDigest: "0xabc",
      cid: "bafy",
      profile: {
        projectId: FIXED_UUID,
        tokenDecimals: 18,
        tokenUnit: "T",
        legal: {
          wrapper: "claim",
          redemptionTerms: {
            uri: "https://a.example/t",
            sha256: "ab".repeat(32),
          },
          rating: {
            provider: "R",
            uri: "javascript:alert(1)",
            sha256: "cd".repeat(32),
          },
        },
      },
    } as never);
    renderWithWallet(<Setup />, { connected: true });
    const link = await screen.findByRole("link", {
      name: "https://a.example/t",
    });
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(screen.getByText("javascript:alert(1)").closest("a")).toBeNull();
    expect(screen.getByText(`sha256: ${"cd".repeat(32)}`)).toBeInTheDocument();
  });
});

describe("Setup by factory version", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    vi.mocked(api.getProject).mockReset().mockRejectedValue(new Error("404"));
    vi.mocked(api.getProfile)
      .mockReset()
      .mockResolvedValue(storedProfileResponse());
    vi.mocked(sendDeployProject).mockClear().mockResolvedValue("0xdeployhash");
    vi.mocked(waitForTxReceipt).mockClear().mockResolvedValue(undefined);
    vi.mocked(readErc20Decimals).mockClear().mockResolvedValue(6);
    wallet = installFakeWallet({ chainId: 31337 });
  });

  afterEach(() => {
    wallet.uninstall();
  });

  const config = (factoryVersion: string) =>
    vi.mocked(api.getConfig).mockReset().mockResolvedValue({
      chainId: 31337,
      factoryAddress: FACTORY,
      factoryVersion,
      projectId: "proj-1",
    });

  const fill = (label: string, value: string) =>
    fireEvent.change(screen.getByLabelText(label), { target: { value } });

  function fillCommon() {
    fill("Name", "Acme");
    fill("Symbol", "ACME");
    fill("Quote token address", ADDR(2));
    fill("Admin address", ADDR(3));
    fill("Auditor address", ADDR(4));
    fill("Compliance operator address", ADDR(5));
    fill("Treasurer address", ADDR(7));
    fill("Redemption manager address", ADDR(8));
    fill("Treasury address", ADDR(9));
  }

  it.each([
    ["", /could not read the configured factory's version/],
    ["rwa-v9", /unsupported version \(rwa-v9\)/],
  ])("blocks deployment for factory version %j", async (version, message) => {
    config(version);
    renderWithWallet(<Setup />, { connected: true });

    const alert = (await screen.findByText(message)).closest(
      '[role="alert"]',
    ) as HTMLElement;
    expect(
      screen.queryByRole("button", { name: "Review deployment" }),
    ).toBeNull();
    expect(sendDeployProject).not.toHaveBeenCalled();
    // the version read may fail on a transient RPC error, so the gate offers a retry
    expect(
      within(alert).getByRole("button", { name: "Retry" }),
    ).toBeInTheDocument();
  });

  it("offers no v3 fields on an rwa-v2 factory", async () => {
    config("rwa-v2");
    renderWithWallet(<Setup />, { connected: true });

    await screen.findByRole("button", { name: "Review deployment" });
    for (const label of [
      "Price strategy",
      "Income manager address",
      /Minimum purchase/,
    ]) {
      expect(screen.queryByLabelText(label)).toBeNull();
    }
  });

  it("broadcasts a Fixed rwa-v3 config with the minimum scaled by quote decimals", async () => {
    config("rwa-v3");
    renderWithWallet(<Setup />, { connected: true });
    await screen.findByRole("button", { name: "Review deployment" });

    fillCommon();
    fill("Purchase price per whole token (whole quote-token units)", "2");
    fill("Redemption price per whole token (whole quote-token units)", "1.5");
    fill("Minimum purchase (whole quote-token units)", "10");
    fill("Pricer address", ADDR(6));
    fill("Income manager address", ADDR(10));
    fireEvent.click(screen.getByRole("button", { name: "Review deployment" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Confirm deploy" }),
    );

    await waitFor(() => expect(sendDeployProject).toHaveBeenCalled());
    const sent = vi.mocked(sendDeployProject).mock
      .calls[0][3] as unknown as Record<string, unknown>;
    expect(sent).toMatchObject({
      strategyKind: 0,
      purchasePricePerWholeToken: 2_000_000n,
      redemptionPricePerWholeToken: 1_500_000n,
      minPurchaseQuote: 10_000_000n,
      pricer: ADDR(6),
      incomeManager: ADDR(10),
      // Token decimals and digest come from the stored profile, not from the form
      decimals: 6,
      profileDigest: "0xdig",
      projectId: keccak256(toBytes("proj-1")),
    });
  });

  it("broadcasts an Oracle config with a zero pricer and the oracle parameters", async () => {
    config("rwa-v3");
    renderWithWallet(<Setup />, { connected: true });
    await screen.findByRole("button", { name: "Review deployment" });

    fillCommon();
    fill("Price strategy", "Oracle");
    // Fixed-only inputs disappear for an oracle
    expect(screen.queryByLabelText("Pricer address")).toBeNull();
    expect(
      screen.queryByLabelText(/Purchase price per whole token/),
    ).toBeNull();
    fill("Price feed address", ADDR(0xfee));
    fill("Max feed age (seconds)", "3600");
    fill("Purchase spread (bps)", "50");
    fill("Redemption spread (bps)", "75");
    fill("Income manager address", ADDR(10));
    fireEvent.click(screen.getByRole("button", { name: "Review deployment" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Confirm deploy" }),
    );

    await waitFor(() => expect(sendDeployProject).toHaveBeenCalled());
    const sent = vi.mocked(sendDeployProject).mock
      .calls[0][3] as unknown as Record<string, unknown>;
    expect(sent).toMatchObject({
      strategyKind: 1,
      pricer: `0x${"0".repeat(40)}`,
      purchasePricePerWholeToken: 0n,
      redemptionPricePerWholeToken: 0n,
      priceFeed: ADDR(0xfee),
      priceMaxAge: 3600n,
      purchaseSpreadBps: 50,
      redemptionSpreadBps: 75,
    });
  });

  it("refuses to review without an income manager or, for an oracle, a feed", async () => {
    config("rwa-v3");
    renderWithWallet(<Setup />, { connected: true });
    await screen.findByRole("button", { name: "Review deployment" });

    fillCommon();
    fill("Purchase price per whole token (whole quote-token units)", "2");
    fill("Redemption price per whole token (whole quote-token units)", "1");
    fill("Pricer address", ADDR(6));
    const review = screen.getByRole("button", { name: "Review deployment" });
    expect(review).toBeDisabled();
    fill("Income manager address", ADDR(10));
    expect(review).toBeEnabled();

    fill("Price strategy", "Oracle");
    expect(review).toBeDisabled();
    fill("Price feed address", ADDR(0xfee));
    fill("Max feed age (seconds)", "60");
    expect(review).toBeEnabled();
  });

  it("blocks a mismatched sequencer feed and grace period before broadcast", async () => {
    config("rwa-v3");
    renderWithWallet(<Setup />, { connected: true });
    await screen.findByRole("button", { name: "Review deployment" });

    fillCommon();
    fill("Price strategy", "Oracle");
    fill("Price feed address", ADDR(0xfee));
    fill("Max feed age (seconds)", "60");
    fill("Sequencer uptime feed (optional)", ADDR(0xfed));
    fill("Income manager address", ADDR(10));
    fireEvent.click(screen.getByRole("button", { name: "Review deployment" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Confirm deploy" }),
    );

    await screen.findByText(
      /sequencer uptime feed and the grace period together/i,
    );
    expect(sendDeployProject).not.toHaveBeenCalled();
  });
});
