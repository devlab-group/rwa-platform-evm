// Stateful mock of the api/openapi.yaml surface via Playwright route
// interception. Each spec gets a fresh in-memory store (installMockApi
// returns it) so it can both drive the UI and assert/mutate server-side
// state directly (e.g. simulate an admin funding a redemption that the
// investor then sees as Claimable after a reload).
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

/** RWAFactory address returned by GET /api/v1/config — where the admin wallet broadcasts deploy(). */
export const FACTORY_ADDRESS = addr("f0");

/** projectId returned by GET /api/v1/config — the operator sets this in the server
 * config; the admin console builds the Asset Profile with it instead of generating one. */
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
  /**
   * projectId -> the persisted profile's derived digest/decimals; create-once.
   * Mirrors the real server: POST /api/v1/project/deploy does
   * NOT accept decimals/profileDigest from the caller (see
   * server/internal/api/project.go), it derives both from whatever was
   * already stored here by POST /api/v1/profile.
   */
  createdProfiles: Map<
    string,
    {
      profileDigest: string;
      tokenDecimals: number;
      // Raw document + derived fields, so GET /api/v1/profile can return a
      // StoredProfile for the admin UI to repopulate after a reload.
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
      // Valid 32-byte hex: it is now encoded on-chain as bytes32 in the
      // MintAttestation the admin broadcasts, so it must be real hex.
      profileDigest: `0x${"de".repeat(32)}`,
      addresses: { ...ADDRESSES },
      paused: false,
      auditor: addr("99"),
      treasury: addr("aa"),
      redemptionManager: addr("bb"),
      // No two-step admin transfer in flight by default; set to an address to
      // exercise the Security-page "Accept admin role" flow in e2e.
      pendingAdmin: "",
      finalityConfirmations: 12,
      bytecodeVerified: true,
      // The connected admin wallet holds every privileged role — the
      // single-admin (deployer-holds-all) model — so role-gated admin actions
      // (treasury withdrawal, redemption fund/reject, and the Security-page
      // actions) render for it. COMPLIANCE_ROLE stays a distinct holder.
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

/** Installs route interception for every `/api/v1/**` call and returns the mutable backing store. */
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
    // Non-sensitive bootstrap for the admin console: the chain id + RWAFactory
    // address the admin needs to assemble ProjectConfig and broadcast
    // RWAFactory.deploy from their own wallet (the server no longer relays the
    // deploy — POST /api/v1/project/deploy was removed).
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
    // GET returns the single stored profile so Setup can repopulate after a
    // reload; 404 while none has been created (the create-once flow's start).
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
      // Valid 32-byte hex: the deploy encodes it on-chain as bytes32.
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
      // recordKey/nonce/validUntil are now exposed so the admin can assemble
      // the SupplyController.MintAttestation and broadcast the mint from their
      // own wallet (the server no longer relays it — POST .../signature was
      // removed; the mint is observed on-chain by the indexer).
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
    // Purchase quotes are not a server endpoint either — the console reads
    // Vault.previewBuy through the connected wallet (see mock-wallet).
    // Treasury withdrawal is not a server calldata endpoint — the console
    // encodes Vault.withdrawProceeds itself and broadcasts it from the
    // connected wallet, asserted via the mock wallet's __getSentTransactions.
    if (path === "/api/v1/sales/purchases" && method === "GET")
      return json(state.purchases);

    // --- redemptions ---------------------------------------------------
    if (path === "/api/v1/redemptions" && method === "GET") {
      const status = url.searchParams.get("status");
      // Public list: filter by status and/or by beneficiary address (the
      // investor page passes its connected wallet), case-insensitive.
      const address = url.searchParams.get("address")?.toLowerCase();
      const list = state.redemptions.filter(
        (r) =>
          (!status || r.status === status) &&
          (!address || r.beneficiary?.toLowerCase() === address),
      );
      return json(list);
    }
    // No redemption call is a server calldata endpoint — request, claim,
    // cancel, fund and reject are all encoded in the browser and broadcast
    // from the connected wallet (approve→fund for funding), asserted via the
    // mock wallet's __getSentTransactions.
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

    // Fail loudly rather than let an un-mocked route silently pass a spec.
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
  // Lives outside `/api/v1/**` (see api/openapi.yaml) so it needs its own
  // route registration. Two steps: POST /auth/challenge returns a message to
  // sign; POST /auth/session takes {address, signature} and returns an admin
  // JWT. There is no DELETE — logout is client-side. Most specs never hit
  // these directly — tests/fixtures/fixtures.ts seeds an already-authenticated
  // session via `window.__RWA_E2E_SESSION__`; tests/specs/auth-session.spec.ts
  // exercises the real connect → sign → verify flow with that bootstrap
  // disabled.
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
      // The mock treats any well-formed request as the admin (a wrong-wallet
      // case is simulated per-spec by overriding this route with a 401).
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
