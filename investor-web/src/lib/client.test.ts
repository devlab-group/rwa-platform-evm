import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "./client";

const ADDRESS = "0x1111111111111111111111111111111111111111";

function json(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    headers: { "Content-Type": "application/json" },
    ...init,
  });
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  // A local .env must not change the URLs under test
  vi.stubEnv("VITE_API_BASE_URL", "");
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

// Method, URL and headers of the one request made
function sent() {
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  return { url, init, headers: (init.headers ?? {}) as Record<string, string> };
}

describe("api request building", () => {
  it("sends X-Wallet-Session only on session endpoints", async () => {
    fetchMock.mockResolvedValue(json({}));
    await api.getMyWalletStatus("tok-1");
    const { url, init, headers } = sent();
    expect(url).toBe("/api/v1/me/wallet-status");
    expect(init.method).toBe("GET");
    expect(headers).toEqual({ "X-Wallet-Session": "tok-1" });
  });

  it("sends no session or authorization header on public endpoints", async () => {
    fetchMock.mockResolvedValue(json({}));
    await api.getProject();
    expect(sent().headers).toEqual({});
  });

  it("posts startKYC with the session header and no body", async () => {
    fetchMock.mockResolvedValue(json({ token: "sdk" }));
    await api.startKYC("tok-2");
    const { url, init, headers } = sent();
    expect(url).toBe("/api/v1/compliance/kyc/start");
    expect(init.method).toBe("POST");
    expect(init.body).toBeUndefined();
    expect(headers).toEqual({ "X-Wallet-Session": "tok-2" });
  });

  it("posts a JSON body with a JSON content type", async () => {
    fetchMock.mockResolvedValue(json({}));
    await api.createChallenge({ address: ADDRESS } as never);
    const { init, headers } = sent();
    expect(init.method).toBe("POST");
    expect(headers["Content-Type"]).toBe("application/json");
    expect(JSON.parse(init.body as string)).toEqual({ address: ADDRESS });
  });

  it("percent-encodes path parameters", async () => {
    fetchMock.mockResolvedValue(json({ allowed: true }));
    await api.isAddressAllowed("a/b c");
    expect(sent().url).toBe("/api/v1/compliance/allowed/a%2Fb%20c");
  });

  it("builds list queries, dropping unset filters", async () => {
    fetchMock.mockResolvedValue(json([]));
    await api.listRedemptions({ address: ADDRESS, limit: 25 });
    expect(sent().url).toBe(`/api/v1/redemptions?address=${ADDRESS}&limit=25`);
  });
});

describe("api responses", () => {
  it("returns undefined for 204", async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    await expect(api.getProject()).resolves.toBeUndefined();
  });

  it("rejects a 200 body that is not JSON instead of returning undefined", async () => {
    fetchMock.mockResolvedValue(
      new Response("<html>spa</html>", { status: 200 }),
    );
    await expect(api.getProject()).rejects.toThrow();
  });

  it("reads pagination headers from list responses", async () => {
    fetchMock.mockResolvedValue(
      json([{ id: "1" }], {
        headers: {
          "Content-Type": "application/json",
          "X-Total-Count": "42",
          "X-Page-Size": "25",
          "X-Next-Cursor": "abc",
        },
      }),
    );
    await expect(api.listTransactions()).resolves.toEqual({
      items: [{ id: "1" }],
      totalCount: 42,
      pageSize: 25,
      nextCursor: "abc",
    });
  });

  it("leaves pagination fields undefined when the headers are absent", async () => {
    fetchMock.mockResolvedValue(json([]));
    await expect(api.listTransactions()).resolves.toEqual({
      items: [],
      totalCount: undefined,
      pageSize: undefined,
      nextCursor: undefined,
    });
  });
});

describe("api error mapping", () => {
  it("maps a 401 with an error body to ApiError carrying status and body", async () => {
    const body = { code: "unauthorized", message: "session expired" };
    fetchMock.mockResolvedValue(json(body, { status: 401 }));
    const err = await api.getMyWalletStatus("stale").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({
      status: 401,
      body,
      message: "session expired",
    });
  });

  it("maps a non-JSON error response to ApiError with a status message and no body", async () => {
    fetchMock.mockResolvedValue(new Response("bad gateway", { status: 502 }));
    const err = await api.getProject().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({
      status: 502,
      body: undefined,
      message: "Request failed with status 502",
    });
  });

  it("maps list endpoint errors the same way", async () => {
    fetchMock.mockResolvedValue(json({ message: "nope" }, { status: 500 }));
    await expect(api.listRedemptions()).rejects.toMatchObject({
      name: "ApiError",
      status: 500,
      message: "nope",
    });
  });
});
