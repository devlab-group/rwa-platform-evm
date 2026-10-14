// Stateful mock of the api/openapi.yaml surface, built on Playwright route
// interception. Each spec gets a fresh in-memory store from installMockApi, so
// it can drive the UI and also assert or change server-side state directly,
// for example to simulate a funded redemption that shows as Claimable after a
// reload.
import type { Page, Route } from "@playwright/test";
import type { components } from "../../src/lib/api-types";
import { MOCK_WALLET_ADDRESS } from "./mock-wallet";

type Project = components["schemas"]["Project"];
type WalletStatus = components["schemas"]["WalletStatus"];
type Challenge = components["schemas"]["Challenge"];
type AssetRecord = components["schemas"]["AssetRecord"];
type Inventory = components["schemas"]["Inventory"];
type Redemption = components["schemas"]["Redemption"];
type Transaction = components["schemas"]["Transaction"];
type WebhookEvent = components["schemas"]["WebhookEvent"];
type AuditLog = components["schemas"]["AuditLog"];
type Purchase = components["schemas"]["Purchase"];
type TxRef = components["schemas"]["TxRef"];

const addr = (pair: string) => `0x${pair.repeat(20)}`;

export const ADDRESSES = {
  token: addr("22"),
  compliance: addr("33"),
  supplyController: addr("44"),
  vault: addr("55"),
  redemptionEscrow: addr("66"),
  strategy: addr("77"),
  quoteToken: addr("88"),
};

export const CHAIN_ID = 31337;

/** RWAFactory address returned by GET /api/v1/config, where the admin wallet broadcasts deploy() */
export const FACTORY_ADDRESS = addr("f0");

/** projectId returned by GET /api/v1/config; the console builds the Asset Profile with it */
export const PROJECT_ID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301";

export interface MockState {
  project: Project;
  wallets: WalletStatus[];
  records: AssetRecord[];
  inventory: Inventory;
  redemptions: Redemption[];
  transactions: Transaction[];
  webhooks: WebhookEvent[];
  auditLogs: AuditLog[];
  purchases: Purchase[];
  challenges: Record<string, Challenge>;
  /** Stored profiles keyed by projectId, with the digest and decimals derived at creation; each can be created once */
  createdProfiles: Map<
    string,
    {
      profileDigest: string;
      tokenDecimals: number;
      // Raw document and derived fields, so GET /api/v1/profile can return a
      // StoredProfile
      profile?: Record<string, unknown>;
      tokenUnit?: string;
      cid?: string;
    }
  >;
}

export function defaultState(): MockState {
  return {
    project: {
      projectId: "demo-project",
      version: "1.0.0",
      chainId: CHAIN_ID,
      decimals: 18,
      tokenUnit: "gram",
      // Real 32-byte hex, because the mint attestation encodes it as bytes32
      profileDigest: `0x${"de".repeat(32)}`,
      addresses: { ...ADDRESSES },
      paused: false,
      auditor: addr("99"),
      treasury: addr("aa"),
      redemptionManager: addr("bb"),
      // No admin transfer is pending by default; set an address to exercise the
      // Security page's "Accept admin role" flow
      pendingAdmin: "",
      finalityConfirmations: 12,
      bytecodeVerified: true,
      // The connected admin wallet holds every privileged role except
      // COMPLIANCE_ROLE, so role-gated actions (treasury withdrawal, redemption
      // fund and reject, Security actions) render for it
      roles: {
        DEFAULT_ADMIN_ROLE: [MOCK_WALLET_ADDRESS],
        PAUSER_ROLE: [MOCK_WALLET_ADDRESS],
        PRICER_ROLE: [MOCK_WALLET_ADDRESS],
        TREASURER_ROLE: [MOCK_WALLET_ADDRESS],
        REDEMPTION_MANAGER_ROLE: [MOCK_WALLET_ADDRESS],
        COMPLIANCE_ROLE: [addr("33")],
      },
    },
    wallets: [
      {
        address: addr("ee"),
        status: "Allowed",
        validUntil: 4102444800,
        ownershipVerified: true,
      },
    ],
    records: [],
    inventory: {
      inventory: "1000000000000000000000",
      quoteBalance: "500000000",
      purchasePrice: "1000000",
      redemptionPrice: "950000",
    },
    redemptions: [],
    transactions: [],
    webhooks: [],
    auditLogs: [],
    purchases: [],
    challenges: {},
    createdProfiles: new Map(),
  };
}

let idCounter = 0;
function nextId(prefix: string): string {
  idCounter += 1;
  return `${prefix}-${idCounter}`;
}

/** Intercepts every `/api/v1/**` call and returns the mutable backing store */
export async function installMockApi(
  page: Page,
  overrides: Partial<MockState> = {},
): Promise<MockState> {
  const state: MockState = { ...defaultState(), ...overrides };

  await page.route("**/api/v1/**", async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname;
    const method = req.method();
    const idemKey = (await req.headerValue("idempotency-key")) ?? undefined;

    const json = (data: unknown, status = 200) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(data),
      });
    const txRef = (txHash: string): TxRef => ({
      txHash,
      status: "submitted",
      idempotencyKey: idemKey,
    });

    // --- project -----------------------------------------------------
    if (path === "/api/v1/project" && method === "GET")
      return json(state.project);
    // Bootstrap values the admin needs to broadcast RWAFactory.deploy from their
    // own wallet: the chain ID, factory address and projectId
    if (path === "/api/v1/config" && method === "GET")
      return json({
        chainId: CHAIN_ID,
        factoryAddress: FACTORY_ADDRESS,
        projectId: PROJECT_ID,
      });
    if (path === "/api/v1/profile/validate" && method === "POST") {
      return json({
        valid: true,
        errors: [],
        profileDigest: "0xvaliddigest",
        cid: "bafymockcid",
      });
    }
    // Returns the stored profile, or 404 while none has been created
    if (path === "/api/v1/profile" && method === "GET") {
      const [entry] = [...state.createdProfiles.values()];
      if (!entry) {
        return json(
          { code: "not_found", message: "no profile has been created yet" },
          404,
        );
      }
      return json({
        profile: entry.profile ?? {},
        projectId: [...state.createdProfiles.keys()][0],
        profileDigest: entry.profileDigest,
        cid: entry.cid ?? "bafymockcreated",
        decimals: entry.tokenDecimals,
        tokenUnit: entry.tokenUnit ?? "",
      });
    }
    if (path === "/api/v1/profile" && method === "POST") {
      const body = req.postDataJSON() as {
        projectId?: unknown;
        tokenDecimals?: unknown;
        tokenUnit?: unknown;
      };
      const projectId =
        typeof body.projectId === "string" ? body.projectId : undefined;
      if (projectId && state.createdProfiles.has(projectId)) {
        return json(
          {
            code: "conflict",
            message:
              "a profile already exists for this projectId and is immutable",
          },
          409,
        );
      }
      // Real 32-byte hex, because the deploy encodes it as bytes32
      const profileDigest = `0x${"ce".repeat(32)}`;
      const tokenDecimals =
        typeof body.tokenDecimals === "number" ? body.tokenDecimals : 18;
      const cid = "bafymockcreated";
      if (projectId)
        state.createdProfiles.set(projectId, {
          profileDigest,
          tokenDecimals,
          profile: body as Record<string, unknown>,
          tokenUnit:
            typeof body.tokenUnit === "string" ? body.tokenUnit : undefined,
          cid,
        });
      return json({ valid: true, errors: [], profileDigest, cid }, 201);
    }

    // --- compliance ----------------------------------------------------
    if (path === "/api/v1/compliance/wallets" && method === "GET")
      return json(state.wallets);
    if (path === "/api/v1/compliance/challenge" && method === "POST") {
      const body = req.postDataJSON() as { address: string };
      const nonce = nextId("nonce");
      const challenge: Challenge = {
        address: body.address,
        nonce,
        message: `Sign to verify ownership of ${body.address} (nonce ${nonce})`,
        expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(),
      };
      state.challenges[nonce] = challenge;
      return json(challenge);
    }
    if (path === "/api/v1/compliance/webhooks" && method === "GET")
      return json(state.webhooks);
    if (path === "/api/v1/audit-logs" && method === "GET")
      return json(state.auditLogs);
    if (path === "/api/v1/compliance/status" && method === "POST") {
      const body = req.postDataJSON() as {
        address: string;
        status: WalletStatus["status"];
        validUntil?: number;
      };
      let wallet = state.wallets.find(
        (w) => w.address?.toLowerCase() === body.address.toLowerCase(),
      );
      if (!wallet) {
        wallet = { address: body.address, ownershipVerified: false };
        state.wallets.push(wallet);
      }
      wallet.status = body.status;
      wallet.validUntil = body.validUntil;
      return json(txRef(`0x${nextId("status")}`), 202);
    }

    // --- assets ----------------------------------------------------------
    if (path === "/api/v1/assets/records" && method === "GET")
      return json(state.records);
    if (path === "/api/v1/assets/records" && method === "POST") {
      const body = req.postDataJSON() as { recordId: string; amount: string };
      // recordKey, nonce and validUntil let the admin assemble the
      // SupplyController.MintAttestation and broadcast the mint from their own
      // wallet
      const record: AssetRecord = {
        recordId: body.recordId,
        status: "Pending",
        metadataDigest: `0x${"cd".repeat(32)}`,
        cid: "bafymockrecord",
        amount: body.amount,
        createdAt: new Date().toISOString(),
        recordKey: `0x${"ab".repeat(32)}`,
        nonce: "1",
        validUntil: 4102444800,
      };
      state.records.push(record);
      return json(record, 201);
    }
    const packageMatch = path.match(
      /^\/api\/v1\/assets\/records\/([^/]+)\/package$/,
    );
    if (packageMatch && method === "GET") {
      return route.fulfill({
        status: 200,
        contentType: "application/zip",
        body: "mock-rwa-package",
      });
    }

    // --- sales -------------------------------------------------------------
    if (path === "/api/v1/sales/inventory" && method === "GET")
      return json(state.inventory);
    // The server has no quote or withdrawal endpoint. The console reads
    // Vault.previewBuy through the connected wallet (see mock-wallet), and
    // encodes and broadcasts Vault.withdrawProceeds itself; specs assert it
    // through the mock wallet's __getSentTransactions.
    if (path === "/api/v1/sales/purchases" && method === "GET")
      return json(state.purchases);

    // --- redemptions ---------------------------------------------------
    if (path === "/api/v1/redemptions" && method === "GET") {
      const status = url.searchParams.get("status");
      // Public list; filters by status and by beneficiary address, ignoring case
      const address = url.searchParams.get("address")?.toLowerCase();
      const list = state.redemptions.filter(
        (r) =>
          (!status || r.status === status) &&
          (!address || r.beneficiary?.toLowerCase() === address),
      );
      return json(list);
    }
    // The server has no redemption write endpoints. The console encodes fund,
    // reject and the preceding approve in the browser and broadcasts them from
    // the connected wallet; specs assert them through the mock wallet's
    // __getSentTransactions.
    const idMatch = path.match(/^\/api\/v1\/redemptions\/([^/]+)$/);
    if (idMatch && method === "GET") {
      const redemption = state.redemptions.find(
        (r) => r.id === decodeURIComponent(idMatch[1]),
      );
      if (!redemption)
        return json(
          { code: "not_found", message: "redemption not found" },
          404,
        );
      return json(redemption);
    }

    // --- transactions --------------------------------------------------
    if (path === "/api/v1/transactions" && method === "GET") {
      const address = url.searchParams.get("address")?.toLowerCase();
      const list = address
        ? state.transactions.filter((t) =>
            JSON.stringify(t).toLowerCase().includes(address),
          )
        : state.transactions;
      return json(list);
    }

    // Fail loudly so an unmocked route cannot pass a spec silently
    return route.fulfill({
      status: 501,
      contentType: "application/json",
      body: JSON.stringify({
        code: "unmocked_route",
        message: `${method} ${path} not mocked`,
      }),
    });
  });

  // --- auth (wallet-signature admin login) ------------------------------
  // These routes sit outside `/api/v1/**` (see api/openapi.yaml), so they need
  // their own registration. POST /auth/challenge returns a message to sign, and
  // POST /auth/session takes {address, signature} and returns an admin JWT.
  // Most specs never call them, because tests/fixtures/fixtures.ts seeds a
  // session through `window.__RWA_E2E_SESSION__`; tests/specs/auth-session.spec.ts
  // disables that seed to run the real flow.
  await page.route("**/auth/challenge", async (route: Route) => {
    const req = route.request();
    if (req.method() !== "POST") return route.fallback();
    const body = req.postDataJSON() as { address?: string };
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        message: `Sign in to RWA Platform as ${body.address ?? ""} (nonce: mock-nonce)`,
        expiresAt: Math.floor(Date.now() / 1000) + 300,
      }),
    });
  });

  await page.route("**/auth/session", async (route: Route) => {
    const req = route.request();
    const method = req.method();
    const json = (data: unknown, status = 200) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(data),
      });

    if (method === "POST") {
      const body = req.postDataJSON() as {
        address?: string;
        signature?: string;
      };
      if (!body.address || !body.signature) {
        return json(
          { code: "unauthorized", message: "missing address or signature" },
          401,
        );
      }
      // The mock treats any well-formed request as the admin; a spec simulates
      // a wrong wallet by overriding this route with a 401
      return json({
        token: `mock-jwt:${body.address}`,
        expiresAt: Math.floor(Date.now() / 1000) + 3600,
        role: "admin",
        address: body.address,
      });
    }
    return route.fulfill({
      status: 501,
      contentType: "application/json",
      body: JSON.stringify({
        code: "unmocked_route",
        message: `${method} /auth/session not mocked`,
      }),
    });
  });

  return state;
}
