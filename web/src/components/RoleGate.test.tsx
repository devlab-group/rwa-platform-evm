import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { RoleGate } from "./RoleGate";
import { ROLES } from "../lib/roles";
import { installFakeWallet, renderWithWallet } from "../test/walletHarness";

const ME = "0x1111111111111111111111111111111111111111"; // fake wallet's account
const SAFE = "0x2222222222222222222222222222222222222222";

describe("RoleGate per-contract holders", () => {
  let wallet: ReturnType<typeof installFakeWallet>;
  beforeEach(() => {
    wallet = installFakeWallet({ account: ME, chainId: 31337 });
  });
  afterEach(() => wallet.uninstall());

  const roles = { TREASURER_ROLE: [ME, SAFE] };

  it("hides direct funding from vault-only treasurers", async () => {
    renderWithWallet(
      <RoleGate
        roles={roles}
        rolesByContract={{
          vault: { TREASURER_ROLE: [ME, SAFE] },
          redemptionEscrow: { TREASURER_ROLE: [SAFE] },
        }}
        contracts={["redemptionEscrow"]}
        role={ROLES.treasurer}
        action="fund redemptions"
      >
        <button>Fund</button>
      </RoleGate>,
      { connected: true },
    );
    expect(screen.queryByRole("button", { name: "Fund" })).toBeNull();
    expect(screen.getByText(/fund redemptions/)).toBeInTheDocument();
  });

  it("enables wallets holding the role on every target contract", async () => {
    renderWithWallet(
      <RoleGate
        roles={roles}
        rolesByContract={{
          vault: { TREASURER_ROLE: [ME] },
          redemptionEscrow: { TREASURER_ROLE: [ME] },
        }}
        contracts={["vault", "redemptionEscrow"]}
        role={ROLES.treasurer}
        action="x"
      >
        <button>Go</button>
      </RoleGate>,
      { connected: true },
    );
    expect(
      await screen.findByRole("button", { name: "Go" }),
    ).toBeInTheDocument();
  });

  it("lists holders present on every target contract", async () => {
    renderWithWallet(
      <RoleGate
        roles={{
          TREASURER_ROLE: [SAFE, "0x3333333333333333333333333333333333333333"],
        }}
        rolesByContract={{
          vault: {
            TREASURER_ROLE: [
              SAFE,
              "0x3333333333333333333333333333333333333333",
            ],
          },
          redemptionEscrow: { TREASURER_ROLE: [SAFE] },
        }}
        contracts={["vault", "redemptionEscrow"]}
        role={ROLES.treasurer}
        action="x"
      >
        <button>Go</button>
      </RoleGate>,
      { connected: true },
    );
    expect(screen.getByRole("option", { name: SAFE })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /0x3333/ })).toBeNull();
  });

  it("uses union holders without a contract map", async () => {
    renderWithWallet(
      <RoleGate
        roles={roles}
        contracts={["redemptionEscrow"]}
        role={ROLES.treasurer}
        action="x"
      >
        <button>Go</button>
      </RoleGate>,
      { connected: true },
    );
    expect(
      await screen.findByRole("button", { name: "Go" }),
    ).toBeInTheDocument();
  });
});
