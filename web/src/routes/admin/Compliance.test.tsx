import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Compliance } from "./Compliance";
import { api, ApiError } from "../../lib/client";

vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      listWallets: vi.fn(),
      listWebhookEvents: vi.fn(),
      listAuditLogs: vi.fn(),
      setComplianceStatus: vi.fn(),
      createChallenge: vi.fn(),
    },
  };
});

const ADDR = "0x1234567890123456789012345678901234567890";
const FUTURE = Math.floor(Date.now() / 1000) + 86_400 * 365;
const PAST = Math.floor(Date.now() / 1000) - 86_400;

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.listWallets).mockResolvedValue({ items: [] });
  vi.mocked(api.listWebhookEvents).mockResolvedValue({ items: [] });
  vi.mocked(api.listAuditLogs).mockResolvedValue({ items: [] });
});

function section(name: string): HTMLElement {
  return screen
    .getByRole("heading", { name })
    .closest("section") as HTMLElement;
}

describe("Compliance wallet table", () => {
  it("shows the effective status: Allowed, Expired, Blocked and Not onboarded", async () => {
    vi.mocked(api.listWallets).mockResolvedValue({
      items: [
        {
          address: "0x00000000000000000000000000000000000000a1",
          status: "Allowed",
          validUntil: FUTURE,
          ownershipVerified: true,
        },
        {
          address: "0x00000000000000000000000000000000000000a2",
          status: "Allowed",
          validUntil: PAST,
          ownershipVerified: false,
        },
        {
          address: "0x00000000000000000000000000000000000000a3",
          status: "Blocked",
          ownershipVerified: false,
        },
        {
          address: "0x00000000000000000000000000000000000000a4",
          status: "Unknown",
          ownershipVerified: false,
        },
      ],
    });
    render(<Compliance />);

    const rows = await within(section("Wallets")).findAllByRole("row");
    // Header row plus one row per wallet
    expect(rows).toHaveLength(5);
    const cells = (i: number) =>
      within(rows[i])
        .getAllByRole("cell")
        .map((c) => c.textContent);
    expect(cells(1)).toEqual([
      "0x0000…00a1",
      "Allowed",
      expect.any(String),
      "Yes",
    ]);
    expect(cells(2)[1]).toBe("Expired");
    expect(cells(2)[3]).toBe("No");
    expect(cells(3)[1]).toBe("Blocked");
    expect(cells(4)[1]).toBe("Not onboarded");
  });

  it("shows empty labels for wallets, webhooks and the audit log", async () => {
    render(<Compliance />);
    expect(
      await screen.findByText("No wallets recorded yet."),
    ).toBeInTheDocument();
    expect(
      screen.getByText("No KYC webhook deliveries recorded yet."),
    ).toBeInTheDocument();
    expect(
      screen.getByText("No audit log entries recorded yet."),
    ).toBeInTheDocument();
  });

  it("reports a failed wallet load and retries", async () => {
    vi.mocked(api.listWallets).mockRejectedValueOnce(
      new ApiError(500, { code: "internal", message: "db down" }),
    );
    render(<Compliance />);

    expect(await screen.findByText("db down")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(
      await screen.findByText("No wallets recorded yet."),
    ).toBeInTheDocument();
    expect(api.listWallets).toHaveBeenCalledTimes(2);
  });
});

describe("Compliance webhook history and audit log", () => {
  it("renders webhook outcome and applied flag and audit entries", async () => {
    vi.mocked(api.listWebhookEvents).mockResolvedValue({
      items: [
        {
          id: "w1",
          address: ADDR,
          provider: "sumsub",
          outcome: "Blocked",
          applied: false,
          receivedAt: "2026-10-01T00:00:00Z",
        },
      ],
    });
    vi.mocked(api.listAuditLogs).mockResolvedValue({
      items: [
        {
          id: "a1",
          category: "compliance",
          actor: ADDR,
          action: "setStatus",
          target: ADDR,
          createdAt: "2026-10-02T00:00:00Z",
        },
      ],
    });
    render(<Compliance />);

    const hook = await within(section("Webhook history")).findAllByRole("cell");
    expect(hook.map((c) => c.textContent)).toEqual([
      "0x1234…7890",
      "sumsub",
      "Blocked",
      "No",
      "2026-10-01T00:00:00Z",
    ]);
    const audit = await within(section("Audit log")).findAllByRole("cell");
    expect(audit.map((c) => c.textContent)).toEqual([
      "compliance",
      "0x1234…7890",
      "setStatus",
      "0x1234…7890",
      "2026-10-02T00:00:00Z",
    ]);
  });
});

describe("Compliance manual allow / block", () => {
  function fill(address: string, status?: string, validUntil?: string) {
    fireEvent.change(
      screen.getByLabelText("Address", { selector: "#wallet-address" }),
      { target: { value: address } },
    );
    if (status)
      fireEvent.change(screen.getByLabelText("Status"), {
        target: { value: status },
      });
    if (validUntil !== undefined) {
      fireEvent.change(screen.getByLabelText(/Valid until/), {
        target: { value: validUntil },
      });
    }
  }

  it("disables review until an address is entered", async () => {
    render(<Compliance />);
    const review = await screen.findByRole("button", {
      name: "Review status change",
    });
    expect(review).toBeDisabled();
    fill(ADDR);
    expect(review).toBeEnabled();
  });

  it("sends nothing before the confirmation step and lets the admin go back", async () => {
    render(<Compliance />);
    fill(ADDR, "Blocked");
    fireEvent.click(
      await screen.findByRole("button", { name: "Review status change" }),
    );

    expect(
      screen.getByRole("heading", { name: "Confirm compliance status change" }),
    ).toBeInTheDocument();
    expect(screen.getByText("(no expiry set)")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Back" }));

    expect(api.setComplianceStatus).not.toHaveBeenCalled();
    expect(
      screen.getByRole("button", { name: "Review status change" }),
    ).toBeInTheDocument();
  });

  it("submits the status with a numeric expiry and a per-change idempotency key, then reloads wallets", async () => {
    vi.mocked(api.setComplianceStatus).mockResolvedValue({ txHash: "0xabc" });
    render(<Compliance />);
    await waitFor(() => expect(api.listWallets).toHaveBeenCalledTimes(1));
    fill(ADDR, "Allowed", "1900000000");
    fireEvent.click(
      screen.getByRole("button", { name: "Review status change" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Confirm set status" }));

    expect(await screen.findByText("0xabc")).toBeInTheDocument();
    expect(api.setComplianceStatus).toHaveBeenCalledWith(
      { address: ADDR, status: "Allowed", validUntil: 1900000000 },
      expect.stringMatching(
        new RegExp(`^compliance-${ADDR}-Allowed-[0-9a-f-]{36}$`),
      ),
    );
    await waitFor(() => expect(api.listWallets).toHaveBeenCalledTimes(2));
    // The confirmation panel closes after a successful submit
    expect(
      screen.getByRole("button", { name: "Review status change" }),
    ).toBeInTheDocument();
  });

  it("uses a new idempotency key for each submit, so a repeated change is not replayed", async () => {
    vi.mocked(api.setComplianceStatus).mockResolvedValue({ txHash: "0xabc" });
    render(<Compliance />);
    fill(ADDR, "Allowed");
    for (let i = 0; i < 2; i++) {
      fireEvent.click(
        await screen.findByRole("button", { name: "Review status change" }),
      );
      fireEvent.click(
        screen.getByRole("button", { name: "Confirm set status" }),
      );
      await waitFor(() =>
        expect(api.setComplianceStatus).toHaveBeenCalledTimes(i + 1),
      );
      await screen.findByRole("button", { name: "Review status change" });
    }
    const [first, second] = vi
      .mocked(api.setComplianceStatus)
      .mock.calls.map(([, key]) => key);
    expect(first).not.toBe(second);
  });

  it("omits validUntil when the field is empty", async () => {
    vi.mocked(api.setComplianceStatus).mockResolvedValue({});
    render(<Compliance />);
    fill(ADDR, "Blocked");
    fireEvent.click(
      await screen.findByRole("button", { name: "Review status change" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Confirm set status" }));

    expect(await screen.findByText("submitted")).toBeInTheDocument();
    expect(api.setComplianceStatus).toHaveBeenCalledWith(
      { address: ADDR, status: "Blocked", validUntil: undefined },
      expect.stringMatching(
        new RegExp(`^compliance-${ADDR}-Blocked-[0-9a-f-]{36}$`),
      ),
    );
  });

  it("shows the server's message when the update is rejected and does not reload wallets", async () => {
    vi.mocked(api.setComplianceStatus).mockRejectedValue(
      new ApiError(422, {
        code: "invalid",
        message: "address is a system contract",
      }),
    );
    render(<Compliance />);
    await waitFor(() => expect(api.listWallets).toHaveBeenCalledTimes(1));
    fill(ADDR);
    fireEvent.click(
      screen.getByRole("button", { name: "Review status change" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Confirm set status" }));

    expect(
      await screen.findByText("address is a system contract"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Submitted\. Reference/)).toBeNull();
    expect(api.listWallets).toHaveBeenCalledTimes(1);
  });

  it("falls back to a generic message for a non-API failure", async () => {
    vi.mocked(api.setComplianceStatus).mockRejectedValue(
      new TypeError("network"),
    );
    render(<Compliance />);
    fill(ADDR);
    fireEvent.click(
      await screen.findByRole("button", { name: "Review status change" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Confirm set status" }));

    expect(
      await screen.findByText("Failed to update status."),
    ).toBeInTheDocument();
  });
});

describe("Compliance ownership challenge", () => {
  it("disables generate until an address is entered", async () => {
    render(<Compliance />);
    const btn = await screen.findByRole("button", {
      name: "Generate challenge",
    });
    expect(btn).toBeDisabled();
  });

  it("requests a challenge for the address and shows nonce, message and expiry", async () => {
    vi.mocked(api.createChallenge).mockResolvedValue({
      address: ADDR,
      nonce: "nonce-1",
      message: "Sign to prove ownership",
      expiresAt: "2026-10-16T12:00:00Z",
    });
    render(<Compliance />);
    fireEvent.change(
      await screen.findByLabelText("Address", {
        selector: "#challenge-address",
      }),
      { target: { value: ADDR } },
    );
    fireEvent.click(screen.getByRole("button", { name: "Generate challenge" }));

    expect(await screen.findByText("nonce-1")).toBeInTheDocument();
    expect(screen.getByText("Sign to prove ownership")).toBeInTheDocument();
    expect(screen.getByText("2026-10-16T12:00:00Z")).toBeInTheDocument();
    expect(api.createChallenge).toHaveBeenCalledWith({ address: ADDR });
  });

  it("shows the server's message when the challenge fails and no challenge details", async () => {
    vi.mocked(api.createChallenge).mockRejectedValue(
      new ApiError(400, { code: "invalid", message: "invalid address" }),
    );
    render(<Compliance />);
    fireEvent.change(
      await screen.findByLabelText("Address", {
        selector: "#challenge-address",
      }),
      { target: { value: "nope" } },
    );
    fireEvent.click(screen.getByRole("button", { name: "Generate challenge" }));

    expect(await screen.findByText("invalid address")).toBeInTheDocument();
    expect(screen.queryByText("Message to sign")).toBeNull();
  });
});
