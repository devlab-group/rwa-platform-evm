import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { KycVerificationSection } from "./KycVerification";
import { api, ApiError } from "../../lib/client";
import { setWalletSession } from "../../lib/walletSession";

vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: { ...actual.api, startKYC: vi.fn() },
  };
});

// Both provider SDKs are replaced by stand-ins: the real ones pull code from
// the provider's CDN and drive a camera, neither of which exists under jsdom.
// The stand-ins keep the props contract, so a wrong prop name still fails here.
vi.mock("@sumsub/websdk-react", () => ({
  default: ({
    accessToken,
    onMessage,
  }: {
    accessToken: string;
    onMessage: (type: string, payload: Record<string, unknown>) => void;
  }) => (
    <div>
      <span>sumsub token: {accessToken}</span>
      <button
        type="button"
        onClick={() => onMessage("idCheck.onApplicantSubmitted", {})}
      >
        finish sumsub
      </button>
    </div>
  ),
}));

/** The subset of Onfido.init's parameters these tests assert on. */
interface OnfidoInitParams {
  token: string;
  workflowRunId: string;
  onComplete: () => void;
  onError: (error: { message: string }) => void;
}

const onfidoTearDown = vi.fn().mockResolvedValue(undefined);
const onfidoInit = vi.fn((params: OnfidoInitParams) => {
  void params;
  return { tearDown: onfidoTearDown };
});
vi.mock("onfido-sdk-ui", () => ({
  Onfido: { init: (params: OnfidoInitParams) => onfidoInit(params) },
}));

const ADDRESS = "0x1111111111111111111111111111111111111111";

/** A loaded wallet-status result — `data !== null` is what "has a live session" means. */
function statusProp(
  status: "Unknown" | "Allowed" | "Blocked" | null,
  reload: () => void = vi.fn(),
) {
  return {
    status: "success" as const,
    data: status === null ? null : { address: ADDRESS, status },
    reload,
  };
}

function giveSession() {
  setWalletSession(ADDRESS, {
    token: "session-token",
    expiresAt: new Date(Date.now() + 600_000).toISOString(),
  });
}

describe("KycVerificationSection", () => {
  beforeEach(() => {
    window.localStorage.clear();
    vi.mocked(api.startKYC).mockReset();
    onfidoInit.mockClear();
    onfidoTearDown.mockClear();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("prompts for an ownership challenge instead of offering to start without a session", () => {
    render(
      <KycVerificationSection
        walletAddress={ADDRESS}
        ownWalletStatus={statusProp(null)}
      />,
    );

    expect(
      screen.getByText(/Generate and sign an ownership challenge/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Start verification" }),
    ).toBeNull();
  });

  it("reports a deployment without a provider flow rather than mounting an SDK", async () => {
    giveSession();
    vi.mocked(api.startKYC).mockRejectedValue(
      new ApiError(501, {
        code: "not_implemented",
        message: "kyc provider not configured",
      }),
    );

    render(
      <KycVerificationSection
        walletAddress={ADDRESS}
        ownWalletStatus={statusProp("Unknown")}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Start verification" }));

    await screen.findByText(
      /KYC verification is not enabled on this deployment\./,
    );
    expect(onfidoInit).not.toHaveBeenCalled();
  });

  it("treats the generic provider the same as an unconfigured one", async () => {
    giveSession();
    vi.mocked(api.startKYC).mockResolvedValue({ provider: "generic" });

    render(
      <KycVerificationSection
        walletAddress={ADDRESS}
        ownWalletStatus={statusProp("Unknown")}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Start verification" }));

    await screen.findByText(
      /KYC verification is not enabled on this deployment\./,
    );
  });

  it("mounts Sumsub with the server-issued token and waits for approval once submitted", async () => {
    giveSession();
    const reload = vi.fn();
    vi.mocked(api.startKYC).mockResolvedValue({
      provider: "sumsub",
      token: "sumsub-access-token",
    });

    render(
      <KycVerificationSection
        walletAddress={ADDRESS}
        ownWalletStatus={statusProp("Unknown", reload)}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Start verification" }));

    await screen.findByText("sumsub token: sumsub-access-token");
    expect(api.startKYC).toHaveBeenCalledWith("session-token");

    fireEvent.click(screen.getByRole("button", { name: "finish sumsub" }));
    await screen.findByText(/Verification submitted — awaiting approval/);
  });

  it("mounts Onfido in workflow mode with the returned ref as workflowRunId", async () => {
    giveSession();
    vi.mocked(api.startKYC).mockResolvedValue({
      provider: "onfido",
      token: "onfido-sdk-token",
      ref: "workflow-run-1",
    });

    render(
      <KycVerificationSection
        walletAddress={ADDRESS}
        ownWalletStatus={statusProp("Unknown")}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Start verification" }));

    await waitFor(() => expect(onfidoInit).toHaveBeenCalled());
    const params: OnfidoInitParams = onfidoInit.mock.lastCall![0];
    expect(params).toMatchObject({
      token: "onfido-sdk-token",
      workflowRunId: "workflow-run-1",
    });

    // Completion is signalled by the SDK's own onComplete callback.
    act(() => params.onComplete());
    await screen.findByText(/Verification submitted — awaiting approval/);
  });

  /**
   * Drives the section to the submitted state under fake timers (installed
   * before render so the poll's interval is the fake one), returning the
   * `reload` spy the poll is expected to call.
   */
  async function renderSubmitted(reload: () => void) {
    giveSession();
    vi.mocked(api.startKYC).mockResolvedValue({
      provider: "sumsub",
      token: "t",
    });
    vi.useFakeTimers();

    const rendered = render(
      <KycVerificationSection
        walletAddress={ADDRESS}
        ownWalletStatus={statusProp("Unknown", reload)}
      />,
    );
    await act(async () => {
      fireEvent.click(
        screen.getByRole("button", { name: "Start verification" }),
      );
      await vi.advanceTimersByTimeAsync(0);
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "finish sumsub" }));
    });
    expect(
      screen.getByText(/Verification submitted — awaiting approval/),
    ).toBeInTheDocument();
    return rendered;
  }

  it("polls its own wallet status while a submitted verification is under review", async () => {
    const reload = vi.fn();
    await renderSubmitted(reload);

    reload.mockClear();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(11_000);
    });
    expect(reload).toHaveBeenCalledTimes(2);
  });

  it("stops waiting and reports approval once the wallet turns Allowed", async () => {
    const reload = vi.fn();
    const { rerender } = await renderSubmitted(reload);

    // The poll observes the webhook-driven on-chain approval.
    rerender(
      <KycVerificationSection
        walletAddress={ADDRESS}
        ownWalletStatus={statusProp("Allowed", reload)}
      />,
    );
    expect(
      screen.getByText(/Approved — your wallet is now Allowed/),
    ).toBeInTheDocument();

    reload.mockClear();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(11_000);
    });
    expect(reload).not.toHaveBeenCalled();
  });

  it("gives up polling after a few minutes and offers a manual re-check", async () => {
    const reload = vi.fn();
    await renderSubmitted(reload);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(4 * 60_000);
    });
    expect(screen.getByText(/safe to close this page/)).toBeInTheDocument();

    reload.mockClear();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(reload).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Check again" }));
    expect(reload).toHaveBeenCalledTimes(1);
  });
});
