// Typed fetch wrapper; extend this file and regenerate api-types.ts with npm run gen:api
import type { components, operations } from "./api-types";
import { clearSession, getSession } from "./authSession";

const DEFAULT_BASE_URL = "";

function baseUrl(): string {
  const env = import.meta.env.VITE_API_BASE_URL as string | undefined;
  return env ?? DEFAULT_BASE_URL;
}

export type ApiErrorBody = components["schemas"]["Error"];

/** Thrown for any non-2xx response, carrying the parsed error body when there is one */
export class ApiError extends Error {
  readonly status: number;
  readonly body: ApiErrorBody | undefined;

  constructor(status: number, body: ApiErrorBody | undefined) {
    super(body?.message ?? `Request failed with status ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
  }
}

type SuccessStatus = 200 | 201 | 202 | 204;

type JsonContent<T> = T extends { content: { "application/json": infer J } }
  ? J
  : T extends { content: infer C }
    ? C
    : never;

// Declared 200/201/202/204 JSON bodies, falling back to raw content maps
type OpResponse<Op> = Op extends { responses: infer R }
  ? R extends Record<PropertyKey, unknown>
    ? JsonContent<R[Extract<keyof R, SuccessStatus>]>
    : never
  : never;

type OpBody<Op> = Op extends { requestBody?: infer B }
  ? B extends { content: { "application/json": infer J } }
    ? J
    : never
  : never;

type OpPathParams<Op> = Op extends {
  parameters: { path?: infer P };
}
  ? P
  : never;

type OpQueryParams<Op> = Op extends {
  parameters: { query?: infer Q };
}
  ? Q
  : never;

// Element of an array response
type ArrayItem<T> = T extends (infer U)[] ? U : never;

/** Cursor page: absent totalCount means unknown; pass nextCursor as cursor for another page */
export interface PageResult<T> {
  items: T[];
  totalCount?: number;
  pageSize?: number;
  nextCursor?: string;
}

interface RequestOptions<Op> {
  path?: OpPathParams<Op>;
  query?: OpQueryParams<Op>;
  body?: OpBody<Op>;
  idempotencyKey?: string;
  signal?: AbortSignal;
}

// Pagination signal, limit/cursor and endpoint filters
type PageOptions<Op, Extra extends object = object> = Pick<
  RequestOptions<Op>,
  "signal"
> & { limit?: number; cursor?: string } & Extra;

function fillPath(
  template: string,
  params: Record<string, string> | undefined,
): string {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (_match, key: string) => {
    const value = params[key];
    if (value === undefined) {
      throw new Error(`Missing path parameter "${key}" for ${template}`);
    }
    return encodeURIComponent(value);
  });
}

function appendQuery(
  url: string,
  params: Record<string, unknown> | undefined,
): string {
  if (!params) return url;
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null) continue;
    search.set(key, String(value));
  }
  const qs = search.toString();
  return qs ? `${url}?${qs}` : url;
}

function authHeaders(): Record<string, string> {
  // Admin JWT from the wallet-signature login (lib/authSession.ts)
  const session = getSession();
  return session ? { Authorization: `Bearer ${session.token}` } : {};
}

async function throwForErrorResponse(res: Response): Promise<never> {
  // Clear rejected sessions on 401 so the shell requests a fresh login
  if (res.status === 401) clearSession();
  let body: ApiErrorBody | undefined;
  try {
    body = (await res.json()) as ApiErrorBody;
  } catch {
    body = undefined;
  }
  throw new ApiError(res.status, body);
}

// Authenticated GET as a Blob
async function fetchBlob(path: string, signal?: AbortSignal): Promise<Blob> {
  const res = await fetch(`${baseUrl()}${path}`, {
    headers: authHeaders(),
    signal,
  });
  if (!res.ok) await throwForErrorResponse(res);
  return res.blob();
}

async function request<Op>(
  method: "GET" | "POST" | "DELETE",
  pathTemplate: string,
  options: RequestOptions<Op> = {},
): Promise<OpResponse<Op>> {
  const filled = fillPath(
    pathTemplate,
    options.path as Record<string, string> | undefined,
  );
  const url = appendQuery(
    `${baseUrl()}${filled}`,
    options.query as Record<string, unknown> | undefined,
  );

  const headers: Record<string, string> = authHeaders();
  if (options.idempotencyKey)
    headers["Idempotency-Key"] = options.idempotencyKey;
  if (options.body !== undefined) headers["Content-Type"] = "application/json";

  const res = await fetch(url, {
    method,
    headers,
    body: options.body !== undefined ? JSON.stringify(options.body) : undefined,
    signal: options.signal,
  });

  if (!res.ok) return throwForErrorResponse(res);

  if (res.status === 204) return undefined as OpResponse<Op>;

  // Parse non-204 successes as JSON; malformed bodies such as SPA-fallback HTML reject. Downloads
  // use Blob.
  return (await res.json()) as OpResponse<Op>;
}

// Reads list items plus X-Total-Count, X-Page-Size and X-Next-Cursor for pagination
async function requestPage<Op>(
  pathTemplate: string,
  options: RequestOptions<Op> = {},
): Promise<PageResult<ArrayItem<OpResponse<Op>>>> {
  const filled = fillPath(
    pathTemplate,
    options.path as Record<string, string> | undefined,
  );
  const url = appendQuery(
    `${baseUrl()}${filled}`,
    options.query as Record<string, unknown> | undefined,
  );

  const res = await fetch(url, {
    method: "GET",
    headers: authHeaders(),
    signal: options.signal,
  });

  if (!res.ok) return throwForErrorResponse(res);

  const items = (await res.json()) as ArrayItem<OpResponse<Op>>[];
  const totalCount = res.headers.get("X-Total-Count");
  const pageSize = res.headers.get("X-Page-Size");
  const nextCursor = res.headers.get("X-Next-Cursor");
  return {
    items,
    totalCount: totalCount === null ? undefined : Number(totalCount),
    pageSize: pageSize === null ? undefined : Number(pageSize),
    nextCursor: nextCursor ?? undefined,
  };
}

// Wrappers named after the supported OpenAPI operationIds

export const api = {
  getHealth: (
    opts: Pick<RequestOptions<operations["getHealth"]>, "signal"> = {},
  ) => request<operations["getHealth"]>("GET", "/healthz", opts),

  getReady: (
    opts: Pick<RequestOptions<operations["getReady"]>, "signal"> = {},
  ) => request<operations["getReady"]>("GET", "/readyz", opts),

  getProject: (
    opts: Pick<RequestOptions<operations["getProject"]>, "signal"> = {},
  ) => request<operations["getProject"]>("GET", "/api/v1/project", opts),

  // Admin-only frozen balances and latest forced transfer, which identify wallets
  getEnforcement: (
    opts: Pick<RequestOptions<operations["getEnforcement"]>, "signal"> = {},
  ) =>
    request<operations["getEnforcement"]>(
      "GET",
      "/api/v1/project/enforcement",
      opts,
    ),

  /*
   * Public bootstrap chain/factory values for wallet deployment, available before GET /project
   * exists
   */
  getConfig: (
    opts: Pick<RequestOptions<operations["getConfig"]>, "signal"> = {},
  ) => request<operations["getConfig"]>("GET", "/api/v1/config", opts),

  validateProfile: (body: OpBody<operations["validateProfile"]>) =>
    request<operations["validateProfile"]>("POST", "/api/v1/profile/validate", {
      body,
    }),

  /** Admin-only. The deployment's stored profile, or 404 when none exists yet */
  getProfile: (
    opts: Pick<RequestOptions<operations["getProfile"]>, "signal"> = {},
  ) => request<operations["getProfile"]>("GET", "/api/v1/profile", opts),

  // Admin-only, create-once storage per projectId; validateProfile persists nothing
  createProfile: (
    body: OpBody<operations["createProfile"]>,
    idempotencyKey: string,
  ) =>
    request<operations["createProfile"]>("POST", "/api/v1/profile", {
      body,
      idempotencyKey,
    }),

  listWallets: (opts: PageOptions<operations["listWallets"]> = {}) => {
    const { signal, limit, cursor } = opts;
    return requestPage<operations["listWallets"]>(
      "/api/v1/compliance/wallets",
      { signal, query: { limit, cursor } },
    );
  },

  createChallenge: (body: OpBody<operations["createChallenge"]>) =>
    request<operations["createChallenge"]>(
      "POST",
      "/api/v1/compliance/challenge",
      { body },
    ),

  listWebhookEvents: (
    opts: PageOptions<operations["listWebhookEvents"]> = {},
  ) => {
    const { signal, limit, cursor } = opts;
    return requestPage<operations["listWebhookEvents"]>(
      "/api/v1/compliance/webhooks",
      { signal, query: { limit, cursor } },
    );
  },

  listAuditLogs: (
    opts: PageOptions<operations["listAuditLogs"], { category?: string }> = {},
  ) => {
    const { signal, limit, cursor, category } = opts;
    return requestPage<operations["listAuditLogs"]>("/api/v1/audit-logs", {
      signal,
      query: { category, limit, cursor },
    });
  },

  setComplianceStatus: (
    body: OpBody<operations["setComplianceStatus"]>,
    idempotencyKey: string,
  ) =>
    request<operations["setComplianceStatus"]>(
      "POST",
      "/api/v1/compliance/status",
      {
        body,
        idempotencyKey,
      },
    ),

  listRecords: (opts: PageOptions<operations["listRecords"]> = {}) => {
    const { signal, limit, cursor } = opts;
    return requestPage<operations["listRecords"]>("/api/v1/assets/records", {
      signal,
      query: { limit, cursor },
    });
  },

  createRecord: (
    body: OpBody<operations["createRecord"]>,
    idempotencyKey: string,
  ) =>
    request<operations["createRecord"]>("POST", "/api/v1/assets/records", {
      body,
      idempotencyKey,
    }),

  /*
   * Fetches a protected .rwa package as Blob; callers download through an object URL
   * because an anchor cannot attach the bearer header (routes/admin/Assets.tsx)
   */
  downloadPackage: (
    recordId: string,
    opts: Pick<RequestOptions<operations["downloadPackage"]>, "signal"> = {},
  ): Promise<Blob> =>
    fetchBlob(
      `/api/v1/assets/records/${encodeURIComponent(recordId)}/package`,
      opts.signal,
    ),

  listBurns: (opts: PageOptions<operations["listBurns"]> = {}) => {
    const { signal, limit, cursor } = opts;
    return requestPage<operations["listBurns"]>("/api/v1/assets/burns", {
      signal,
      query: { limit, cursor },
    });
  },

  createBurn: (
    body: OpBody<operations["createBurn"]>,
    idempotencyKey: string,
  ) =>
    request<operations["createBurn"]>("POST", "/api/v1/assets/burns", {
      body,
      idempotencyKey,
    }),

  downloadBurnPackage: (
    burnId: string,
    opts: Pick<
      RequestOptions<operations["downloadBurnPackage"]>,
      "signal"
    > = {},
  ): Promise<Blob> =>
    fetchBlob(
      `/api/v1/assets/burns/${encodeURIComponent(burnId)}/package`,
      opts.signal,
    ),

  reissueBurn: (burnId: string, idempotencyKey: string) =>
    request<operations["reissueBurn"]>(
      "POST",
      "/api/v1/assets/burns/{burnId}/reissue",
      { path: { burnId }, idempotencyKey },
    ),

  // Preparations plus indexed rwa-v3 distribution state
  listIncomeDistributions: (
    opts: PageOptions<operations["listIncomeDistributions"]> = {},
  ) => {
    const { signal, limit, cursor } = opts;
    return requestPage<operations["listIncomeDistributions"]>(
      "/api/v1/income/distributions",
      { signal, query: { limit, cursor } },
    );
  },

  prepareIncomeDistribution: (
    body: OpBody<operations["prepareIncomeDistribution"]>,
    idempotencyKey: string,
  ) =>
    request<operations["prepareIncomeDistribution"]>(
      "POST",
      "/api/v1/income/distributions",
      { body, idempotencyKey },
    ),

  getInventory: (
    opts: Pick<RequestOptions<operations["getInventory"]>, "signal"> = {},
  ) =>
    request<operations["getInventory"]>("GET", "/api/v1/sales/inventory", opts),

  listPurchases: (opts: PageOptions<operations["listPurchases"]> = {}) => {
    const { signal, limit, cursor } = opts;
    return requestPage<operations["listPurchases"]>("/api/v1/sales/purchases", {
      signal,
      query: { limit, cursor },
    });
  },

  listRedemptions: (
    opts: PageOptions<
      operations["listRedemptions"],
      { status?: string; address?: string }
    > = {},
  ) => {
    const { signal, limit, cursor, status, address } = opts;
    return requestPage<operations["listRedemptions"]>("/api/v1/redemptions", {
      signal,
      query: { status, address, limit, cursor },
    });
  },

  getRedemption: (id: string) =>
    request<operations["getRedemption"]>("GET", "/api/v1/redemptions/{id}", {
      path: { id },
    }),

  listTransactions: (
    opts: PageOptions<
      operations["listTransactions"],
      { address?: string }
    > = {},
  ) => {
    const { signal, limit, cursor, address } = opts;
    return requestPage<operations["listTransactions"]>("/api/v1/transactions", {
      signal,
      query: { address, limit, cursor },
    });
  },

  // Public admin-login step 1: requests a single-use, expiring personal_sign challenge
  createAuthChallenge: (body: OpBody<operations["createAuthChallenge"]>) =>
    request<operations["createAuthChallenge"]>("POST", "/auth/challenge", {
      body,
    }),

  /*
   * Public admin-login step 2: exchange address/signature for a JWT when the signer matches
   * the configured admin. Logout is client-side; there is no logout endpoint.
   */
  createSession: (body: OpBody<operations["createSession"]>) =>
    request<operations["createSession"]>("POST", "/auth/session", {
      body,
    }),
};

export type { components, operations } from "./api-types";
