// Stateful mock of the investor-facing slice of api/openapi.yaml, via
// Playwright route interception. Each spec gets a fresh in-memory store
// (installMockApi returns it) so it can both drive the UI and mutate
// server-side state directly — e.g. simulate the issuer funding a redemption
// that the investor then sees as Claimable after a reload.
//
// Only the endpoints this app calls are mocked; anything else 501s loudly so
// an un-mocked route can never silently pass a spec.
import type { Page, Route } from "@playwright/test";
import type { components } from "../../src/lib/api-types";

type Project = components["schemas"]["Project"];
type WalletStatus = components["schemas"]["WalletStatus"];
type Challenge = components["schemas"]["Challenge"];
type Redemption = components["schemas"]["Redemption"];
type Transaction = components["schemas"]["Transaction"];

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

// Buy/redemption prices are not part of this store: the app quotes them from
// the Vault/escrow preview views, which the mock wallet answers (mock-wallet.ts).
export interface MockState {
  project: Project;
  wallets: WalletStatus[];
  redemptions: Redemption[];
  transactions: Transaction[];
  challenges: Record<string, Challenge>;
}

export function defaultState(): MockState {
  return {
    project: {
      projectId: "demo-project",
      version: "1.0.0",
      chainId: CHAIN_ID,
      decimals: 18,
      tokenUnit: "gram",
      profileDigest: `0x${"de".repeat(32)}`,
      addresses: { ...ADDRESSES },
      paused: false,
      auditor: addr("99"),
      treasury: addr("aa"),
      redemptionManager: addr("bb"),
      finalityConfirmations: 12,
      bytecodeVerified: true,
    },
    wallets: [
      {
        address: addr("ee"),
        status: "Allowed",
        validUntil: 4102444800,
        ownershipVerified: true,
      },
    ],
    redemptions: [],
    transactions: [],
    challenges: {},
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

    const json = (data: unknown, status = 200) =>
      route.fulfill({
        status,
        contentType: "application/json",
        body: JSON.stringify(data),
      });
    // --- project -----------------------------------------------------
    if (path === "/api/v1/project" && method === "GET")
      return json(state.project);
    // --- compliance ----------------------------------------------------
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
    if (path === "/api/v1/compliance/challenge/verify" && method === "POST") {
      const body = req.postDataJSON() as {
        address: string;
        nonce: string;
        signature: string;
      };
      let wallet = state.wallets.find(
        (w) => w.address?.toLowerCase() === body.address.toLowerCase(),
      );
      if (!wallet) {
        wallet = {
          address: body.address,
          status: "Unknown",
          ownershipVerified: false,
        };
        state.wallets.push(wallet);
      }
      wallet.ownershipVerified = true;
      // Session token deliberately encodes the address so the mock's
      // GET /api/v1/me/wallet-status handler below can resolve it without a
      // real server-side session store.
      return json({
        ...wallet,
        sessionToken: `mock-session:${wallet.address?.toLowerCase()}`,
        sessionExpiresAt: new Date(Date.now() + 15 * 60_000).toISOString(),
      });
    }
    if (path === "/api/v1/me/wallet-status" && method === "GET") {
      const session = await req.headerValue("x-wallet-session");
      const address = session?.startsWith("mock-session:")
        ? session.slice("mock-session:".length)
        : undefined;
      const wallet =
        address &&
        state.wallets.find((w) => w.address?.toLowerCase() === address);
      if (!wallet)
        return json(
          {
            code: "unauthorized",
            message: "missing or expired wallet session",
          },
          401,
        );
      return json(wallet);
    }
    const allowedMatch = path.match(
      /^\/api\/v1\/compliance\/allowed\/([^/]+)$/,
    );
    if (allowedMatch && method === "GET") {
      const address = decodeURIComponent(allowedMatch[1]).toLowerCase();
      const wallet = state.wallets.find(
        (w) => w.address?.toLowerCase() === address,
      );
      return json({ allowed: wallet?.status === "Allowed" });
    }
    // --- redemptions (specific routes before the generic /{id}) --------
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

  return state;
}
