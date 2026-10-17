import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./client";
import { getSession, setSession } from "./authSession";

// Minimal Response fake for ok, status, headers.get() and json()
function fakeResponse(
  body: unknown,
  {
    status = 200,
    headers = {},
  }: { status?: number; headers?: Record<string, string> } = {},
) {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: { get: (name: string) => headers[name] ?? null },
    json: async () => body,
  } as unknown as Response;
}

describe("client pagination", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("returns items plus X-Total-Count/X-Page-Size/X-Next-Cursor as pagination metadata", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      fakeResponse([{ txHash: "0x1" }, { txHash: "0x2" }], {
        headers: {
          "X-Total-Count": "137",
          "X-Page-Size": "2",
          "X-Next-Cursor": "cursor-abc",
        },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const page = await api.listTransactions({});

    expect(page.items).toEqual([{ txHash: "0x1" }, { txHash: "0x2" }]);
    expect(page.totalCount).toBe(137);
    expect(page.pageSize).toBe(2);
    expect(page.nextCursor).toBe("cursor-abc");
  });

  it("treats an omitted X-Total-Count as unknown, not zero (most-recent-N stores, e.g. audit logs)", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      fakeResponse([{ id: "a" }], {
        headers: { "X-Page-Size": "1" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const page = await api.listAuditLogs({});

    expect(page.totalCount).toBeUndefined();
    expect(page.nextCursor).toBeUndefined();
  });

  it("passes limit/cursor through as query parameters", async () => {
    const fetchMock = vi.fn().mockResolvedValue(fakeResponse([]));
    vi.stubGlobal("fetch", fetchMock);

    await api.listRedemptions({
      limit: 25,
      cursor: "page-2",
      status: "Funded",
    });

    const url = new URL(
      fetchMock.mock.calls[0][0] as string,
      "http://localhost",
    );
    expect(url.searchParams.get("limit")).toBe("25");
    expect(url.searchParams.get("cursor")).toBe("page-2");
    expect(url.searchParams.get("status")).toBe("Funded");
  });
});

describe("client Idempotency-Key", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("sends the caller-supplied key on the Idempotency-Key header for a mutation wrapper", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(fakeResponse({ txHash: "0x1" }));
    vi.stubGlobal("fetch", fetchMock);

    await api.setComplianceStatus(
      { address: "0xabc", status: "Allowed" },
      "compliance-0xabc-Allowed",
    );

    const options = fetchMock.mock.calls[0][1] as RequestInit;
    const headers = options.headers as Record<string, string>;
    expect(headers["Idempotency-Key"]).toBe("compliance-0xabc-Allowed");
  });

  // Required createProfile/setComplianceStatus/createRecord idempotency keys reach the wire
  // unchanged
  it("forwards the exact key value given, without generating or defaulting one", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(fakeResponse({ recordId: "r1" }));
    vi.stubGlobal("fetch", fetchMock);

    await api.createRecord(
      { recordId: "r1", asset: {}, amount: "1", proofs: [] },
      "create-r1",
    );

    const options = fetchMock.mock.calls[0][1] as RequestInit;
    const headers = options.headers as Record<string, string>;
    expect(headers["Idempotency-Key"]).toBe("create-r1");
  });
});

describe("client session handling on error responses", () => {
  const SESSION = {
    token: "jwt",
    expiresAt: Math.floor(Date.now() / 1000) + 3600,
    role: "admin",
    address: "0x1111111111111111111111111111111111111111",
  };

  afterEach(() => {
    vi.unstubAllGlobals();
    setSession(undefined);
  });

  it("clears the admin session when the server answers 401", async () => {
    setSession(SESSION);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(fakeResponse({ code: "unauthorized" }, { status: 401 })),
    );

    await expect(api.getProject({})).rejects.toMatchObject({ status: 401 });

    expect(getSession()).toBeUndefined();
  });

  it.each([403, 500])("keeps the session on a %i response", async (status) => {
    setSession(SESSION);
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(fakeResponse({ code: "nope" }, { status })),
    );

    await expect(api.getProject({})).rejects.toMatchObject({ status });

    expect(getSession()).toEqual(SESSION);
  });

  it("sends the session token as a bearer header", async () => {
    setSession(SESSION);
    const fetchMock = vi.fn().mockResolvedValue(fakeResponse({}));
    vi.stubGlobal("fetch", fetchMock);

    await api.getProject({});

    const headers = (fetchMock.mock.calls[0][1] as RequestInit)
      .headers as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer jwt");
  });
});
