// Investor fetch wrapper for public and X-Wallet-Session endpoints.
// Extend this file; regenerate api-types.ts from api/openapi.yaml.
import type { components, operations } from "./api-types";

const DEFAULT_BASE_URL = "";

function baseUrl(): string {
  const env = import.meta.env.VITE_API_BASE_URL as string | undefined;
  return env ?? DEFAULT_BASE_URL;
}

export type ApiErrorBody = components["schemas"]["Error"];

/** Non-2xx response with its parsed error body, when available */
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

/**
 * Cursor page: absent totalCount means unknown, not zero. Pass nextCursor as cursor
 * for another page; it comes from X-Next-Cursor.
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
  // Wallet-scoped X-Wallet-Session token; see lib/walletSession.ts
  walletSessionToken?: string;
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

/**
 * Sends X-Wallet-Session only for own-wallet status and KYC start;
 * other wrapped endpoints are public and no admin bearer token is sent
 */
function authHeaders(walletSessionToken?: string): Record<string, string> {
  return walletSessionToken ? { "X-Wallet-Session": walletSessionToken } : {};
}

async function throwForErrorResponse(res: Response): Promise<never> {
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

  const headers: Record<string, string> = authHeaders(
    options.walletSessionToken,
  );
  if (options.body !== undefined) headers["Content-Type"] = "application/json";

  const res = await fetch(url, {
    method,
    headers,
    body: options.body !== undefined ? JSON.stringify(options.body) : undefined,
    signal: options.signal,
  });

  if (!res.ok) return throwForErrorResponse(res);

  if (res.status === 204) return undefined as OpResponse<Op>;

  // Parse non-204 successes as JSON; malformed bodies such as SPA-fallback HTML reject
  // instead of returning undefined
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
    headers: authHeaders(options.walletSessionToken),
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
  getProject: (
    opts: Pick<RequestOptions<operations["getProject"]>, "signal"> = {},
  ) => request<operations["getProject"]>("GET", "/api/v1/project", opts),

  getInventory: (
    opts: Pick<RequestOptions<operations["getInventory"]>, "signal"> = {},
  ) =>
    request<operations["getInventory"]>("GET", "/api/v1/sales/inventory", opts),

  listIncomeClaims: (
    address: string,
    opts: Pick<RequestOptions<operations["listIncomeClaims"]>, "signal"> = {},
  ) =>
    request<operations["listIncomeClaims"]>(
      "GET",
      "/api/v1/income/claims/{address}",
      { ...opts, path: { address } },
    ),

  createChallenge: (body: OpBody<operations["createChallenge"]>) =>
    request<operations["createChallenge"]>(
      "POST",
      "/api/v1/compliance/challenge",
      { body },
    ),

  verifyChallenge: (body: OpBody<operations["verifyChallenge"]>) =>
    request<operations["verifyChallenge"]>(
      "POST",
      "/api/v1/compliance/challenge/verify",
      {
        body,
      },
    ),

  // Reads the session wallet's own status
  getMyWalletStatus: (
    walletSessionToken: string,
    opts: Pick<RequestOptions<operations["getMyWalletStatus"]>, "signal"> = {},
  ) =>
    request<operations["getMyWalletStatus"]>(
      "GET",
      "/api/v1/me/wallet-status",
      { ...opts, walletSessionToken },
    ),

  /**
   * Starts KYC for the session wallet; returns the SDK token and, for Onfido, workflow ref.
   * Sends no address body. ApiError 501 means no configured server-initiated flow.
   */
  startKYC: (
    walletSessionToken: string,
    opts: Pick<RequestOptions<operations["startKYC"]>, "signal"> = {},
  ) =>
    request<operations["startKYC"]>("POST", "/api/v1/compliance/kyc/start", {
      ...opts,
      walletSessionToken,
    }),

  // Public transfer-recipient eligibility preflight
  isAddressAllowed: (
    address: string,
    opts: Pick<RequestOptions<operations["isAddressAllowed"]>, "signal"> = {},
  ) =>
    request<operations["isAddressAllowed"]>(
      "GET",
      "/api/v1/compliance/allowed/{address}",
      { ...opts, path: { address } },
    ),

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
};

export type { components, operations } from "./api-types";
