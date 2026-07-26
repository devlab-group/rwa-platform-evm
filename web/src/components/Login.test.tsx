import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Login } from "./Login";
import { clearSession, getSession } from "../lib/authSession";
import { idbGet } from "../lib/idb";
import { api, ApiError } from "../lib/client";
import { installFakeWallet, renderWithWallet } from "../test/walletHarness";

vi.mock("../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/client")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      createAuthChallenge: vi.fn(),
      createSession: vi.fn(),
    },
  };
});

const ADMIN = "0x1111111111111111111111111111111111111111";
const flush = () => new Promise((r) => setTimeout(r, 0));

describe("Login (wallet-signature)", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    clearSession();
    vi.mocked(api.createAuthChallenge).mockReset();
    vi.mocked(api.createSession).mockReset();
    window.localStorage.clear();
    window.sessionStorage.clear();
    wallet = installFakeWallet({ account: ADMIN, chainId: 31337 });
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("runs challenge → sign → verify and stores the JWT (incl. in IndexedDB)", async () => {
    vi.mocked(api.createAuthChallenge).mockResolvedValue({
      message: "sign this: nonce-abc",
      expiresAt: Math.floor(Date.now() / 1000) + 300,
    });
    vi.mocked(api.createSession).mockResolvedValue({
      token: "admin-jwt",
      expiresAt: Math.floor(Date.now() / 1000) + 3600,
      role: "admin",
      address: ADMIN,
    });

    renderWithWallet(<Login />, { connected: true });
    // The harness connects the wallet on mount (as the real admin does during
    // login), so the sign-in button (not the connect button) appears.
    const signIn = await screen.findByRole("button", {
      name: /sign in with wallet/i,
    });
    fireEvent.click(signIn);

    await waitFor(() => expect(getSession()?.token).toBe("admin-jwt"));
    expect(api.createAuthChallenge).toHaveBeenCalledWith({ address: ADMIN });
    // The wallet signed the challenge message; the signature went to /auth/session.
    const [sessionBody] = vi.mocked(api.createSession).mock.calls[0];
    expect(sessionBody.address).toBe(ADMIN);
    expect(sessionBody.signature).toBe(wallet.signature);

    // Persisted to IndexedDB, not localStorage/sessionStorage.
    await flush();
    expect(await idbGet<{ token: string }>("authSession")).toMatchObject({
      token: "admin-jwt",
    });
    expect(window.localStorage.length).toBe(0);
    expect(window.sessionStorage.length).toBe(0);
  });

  it("shows an admin-mismatch message on 401 and creates no session", async () => {
    vi.mocked(api.createAuthChallenge).mockResolvedValue({
      message: "sign this",
      expiresAt: Math.floor(Date.now() / 1000) + 300,
    });
    vi.mocked(api.createSession).mockRejectedValue(
      new ApiError(401, { code: "unauthorized", message: "not admin" }),
    );

    renderWithWallet(<Login />, { connected: true });
    fireEvent.click(
      await screen.findByRole("button", { name: /sign in with wallet/i }),
    );

    await screen.findByText(/not the project admin/i);
    expect(getSession()).toBeUndefined();
  });

  it("treats a rejected signature as a benign cancellation, not a fault", async () => {
    wallet.uninstall();
    wallet = installFakeWallet({
      account: ADMIN,
      signRejection: { code: 4001, message: "User rejected the request" },
    });
    vi.mocked(api.createAuthChallenge).mockResolvedValue({
      message: "sign this",
      expiresAt: Math.floor(Date.now() / 1000) + 300,
    });

    renderWithWallet(<Login />, { connected: true });
    fireEvent.click(
      await screen.findByRole("button", { name: /sign in with wallet/i }),
    );

    await screen.findByText(/rejected/i);
    expect(getSession()).toBeUndefined();
    expect(api.createSession).not.toHaveBeenCalled();
  });

  it("prompts to connect a wallet when none is connected", async () => {
    wallet.uninstall();
    delete window.ethereum;

    renderWithWallet(<Login />);
    expect(
      await screen.findByRole("button", { name: /connect wallet to sign in/i }),
    ).toBeInTheDocument();
  });
});
