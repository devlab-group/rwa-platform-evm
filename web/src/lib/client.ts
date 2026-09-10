// Thin, hand-written fetch wrapper typed against the generated OpenAPI schema
// (src/lib/api-types.ts, produced by `npm run gen:api`). Do not hand-edit that
// generated file — extend this one instead.
import type { components, operations } from "./api-types";
import { clearSession, getSession } from "./authSession";

const DEFAULT_BASE_URL = "";

function baseUrl(): string {
  const env = import.meta.env.VITE_API_BASE_URL as string | undefined;
  return env ?? DEFAULT_BASE_URL;
}

export type ApiErrorBody = components["schemas"]["Error"];

/** Thrown for any non-2xx response. Carries the parsed error body when present. */
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

/** Union of the 2xx JSON response bodies declared for an operation. */
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

/** Element type of an array-typed operation response (the list endpoints). */
type ArrayItem<T> = T extends (infer U)[] ? U : never;

/**
 * A page of a cursor-paginated list endpoint. `totalCount` is omitted
 * server-side for most-recent-N stores (e.g. audit logs) — treat its absence
 * as "unknown", not zero. `nextCursor` is present iff another page exists;
 * pass it back as `cursor` to fetch it.
 */
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

/** Caller-facing options for a paginated list wrapper: signal + limit/cursor, plus any endpoint-specific filter. */
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
  // Admin JWT from the wallet-signature login (see lib/authSession.ts),
  // attached as a standard bearer token.
  const session = getSession();
  return session ? { Authorization: `Bearer ${session.token}` } : {};
}

async function throwForErrorResponse(res: Response): Promise<never> {
  // A rejected/expired session is never worth holding onto — drop it so the
  // UI falls back to the login form instead of silently retrying with a dead
  // token.
  if (res.status === 401) clearSession();
  let body: ApiErrorBody | undefined;
  try {
    body = (await res.json()) as ApiErrorBody;
  } catch {
    body = undefined;
  }
  throw new ApiError(res.status, body);
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

  // Every non-204 success response in api/openapi.yaml is application/json.
  // Don't silently swallow an unexpected body (e.g. an SPA-fallback HTML page
  // from a misconfigured proxy) as `undefined` — parsing it as JSON and
  // letting a SyntaxError propagate to the caller as a real failure is safer
  // than handing callers `undefined` disguised as a successful response.
  return (await res.json()) as OpResponse<Op>;
}

/**
 * Like `request`, but for the cursor-paginated list endpoints: reads the
 * X-Total-Count/X-Page-Size/X-Next-Cursor response headers instead of
 * discarding them, so callers can show a total/"more available"
 * indicator and fetch further pages rather than silently truncating at the
 * server's default page size.
 */
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

// ---- Endpoint wrappers, one per operationId in api/openapi.yaml -----------

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

  /**
   * ERC-7943 enforcement state: who is frozen, and the last seizure. Admin-only,
   * unlike getProject, because it names individual wallets.
   */
  getEnforcement: (
    opts: Pick<RequestOptions<operations["getEnforcement"]>, "signal"> = {},
  ) =>
    request<operations["getEnforcement"]>(
      "GET",
      "/api/v1/project/enforcement",
      opts,
    ),

  /**
   * Public, unauthenticated bootstrap for the admin console: the chain id and
   * RWAFactory address the admin needs to assemble ProjectConfig and broadcast
   * RWAFactory.deploy from their own wallet. Used pre-first-deploy, where
   * GET /project still 404s, so it (not /project) is the source of the factory
   * address + chain.
   */
  getConfig: (
    opts: Pick<RequestOptions<operations["getConfig"]>, "signal"> = {},
  ) => request<operations["getConfig"]>("GET", "/api/v1/config", opts),

  validateProfile: (body: OpBody<operations["validateProfile"]>) =>
    request<operations["validateProfile"]>("POST", "/api/v1/profile/validate", {
      body,
    }),

  /** Admin-only — the deployment's single stored profile, or 404 if none yet (so Setup can repopulate after a reload). */
  getProfile: (
    opts: Pick<RequestOptions<operations["getProfile"]>, "signal"> = {},
  ) => request<operations["getProfile"]>("GET", "/api/v1/profile", opts),

  /** Admin-only, create-once — persists the profile. Setup must call this, not just validate. */
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

  /**
   * Fetches the binary .rwa package with the operator's bearer session
   * attached. A plain `<a href>` can't set that header, so the protected
   * endpoint would 403; callers instead turn the Blob into an object URL and
   * trigger the download themselves — see routes/admin/Assets.tsx.
   */
  downloadPackage: async (
    recordId: string,
    opts: Pick<RequestOptions<operations["downloadPackage"]>, "signal"> = {},
  ): Promise<Blob> => {
    const headers: Record<string, string> = {};
    const session = getSession();
    if (session) headers["Authorization"] = `Bearer ${session.token}`;

    const res = await fetch(
      `${baseUrl()}/api/v1/assets/records/${encodeURIComponent(recordId)}/package`,
      { headers, signal: opts.signal },
    );
    if (!res.ok) {
      if (res.status === 401) clearSession();
      let body: ApiErrorBody | undefined;
      try {
        body = (await res.json()) as ApiErrorBody;
      } catch {
        body = undefined;
      }
      throw new ApiError(res.status, body);
    }
    return res.blob();
  },

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

  /**
   * Step 1 of admin login: requests a single-use, time-limited message for
   * the connected wallet to personal_sign. Public/unauthenticated — this is
   * the login entry point (see components/Login.tsx).
   */
  createAuthChallenge: (body: OpBody<operations["createAuthChallenge"]>) =>
    request<operations["createAuthChallenge"]>("POST", "/auth/challenge", {
      body,
    }),

  /**
   * Step 2 of admin login: submits the wallet address + its signature over
   * the challenge message. The server recovers the signer and, if it equals
   * the configured project admin, returns a signed JWT (token/expiresAt/role/
   * address) to store and attach as `Authorization: Bearer <token>`. A 401
   * means the recovered signer is not the admin. Public by design — this *is*
   * the exchange call. Logout is client-side (drop the token); there is no
   * DELETE endpoint.
   */
  createSession: (body: OpBody<operations["createSession"]>) =>
    request<operations["createSession"]>("POST", "/auth/session", {
      body,
    }),
};

export type { components, operations } from "./api-types";
