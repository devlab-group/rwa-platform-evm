// Hand-written fetch wrapper typed against the OpenAPI schema. api-types.ts is
// a generated copy of the platform's api/openapi.yaml, shipped as-is and not
// edited by hand; extend this file instead. Only the public and
// X-Wallet-Session endpoints an investor needs are wrapped.
import type { components, operations } from "./api-types";

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
 * A page of a cursor-paginated list endpoint. `totalCount` is absent when the
 * server omits X-Total-Count; treat that as unknown, not zero. `nextCursor` is
 * set only when the server sent X-Next-Cursor, i.e. another page exists; pass
 * it back as `cursor` to fetch it.
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
  /** X-Wallet-Session token for the endpoints scoped to one wallet (see lib/walletSession.ts). */
  walletSessionToken?: string;
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

/**
 * The only credential this app sends: the X-Wallet-Session token minted at
 * challenge-verify. It is bound to one address and authorizes only that
 * address's own wallet status and KYC start. Every other endpoint used here is
 * public, and no admin bearer token is ever sent.
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

  // Every non-204 success response in api/openapi.yaml is application/json. An
  // unexpected body (e.g. an SPA-fallback HTML page from a misconfigured proxy)
  // surfaces as the SyntaxError from res.json() instead of being returned as
  // `undefined`, which would look like a successful response.
  return (await res.json()) as OpResponse<Op>;
}

/**
 * Like `request`, but for the cursor-paginated list endpoints: also reads the
 * X-Total-Count, X-Page-Size and X-Next-Cursor response headers, so callers can
 * show a total or "more available" indicator and fetch further pages instead of
 * silently stopping at the server's default page size.
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

// Endpoint wrappers, one per operationId in api/openapi.yaml

export const api = {
  getProject: (
    opts: Pick<RequestOptions<operations["getProject"]>, "signal"> = {},
  ) => request<operations["getProject"]>("GET", "/api/v1/project", opts),

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

  /** The session wallet's own status, read through its X-Wallet-Session token. */
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
   * Opens a verification with the server's configured KYC provider for the
   * session wallet and returns what the provider's web SDK needs (token, and
   * for Onfido the workflow run ref). The server takes the wallet address from
   * the session, so nothing is sent. Throws ApiError 501 when no provider is
   * configured or the configured one has no server-initiated flow.
   */
  startKYC: (
    walletSessionToken: string,
    opts: Pick<RequestOptions<operations["startKYC"]>, "signal"> = {},
  ) =>
    request<operations["startKYC"]>("POST", "/api/v1/compliance/kyc/start", {
      ...opts,
      walletSessionToken,
    }),

  /** Public, unauthenticated transfer-recipient eligibility preflight. */
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
