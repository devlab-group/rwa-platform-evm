import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Assets } from "./Assets";
import { buildSignerPolicy } from "../../lib/signerPolicy";
import { api, ApiError } from "../../lib/client";
import { sendMint, waitForTxReceipt } from "../../lib/wallet";
import { installFakeWallet, renderWithWallet } from "../../test/walletHarness";

// Mint uses the admin wallet, not server relay. Upload supplies the separate auditor
// signature/address; the page checks the address and builds attestation fields from record/project.
vi.mock("../../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/client")>();
  return {
    ...actual,
    api: {
      ...actual.api,
      listRecords: vi.fn(),
      getProject: vi.fn(),
      getProfile: vi.fn(),
      createRecord: vi.fn(),
    },
  };
});

// Spy on mint/receipt to check chain, controller, attestation and signature without a chain
vi.mock("../../lib/wallet", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/wallet")>();
  return {
    ...actual,
    sendMint: vi.fn().mockResolvedValue("0xminthash"),
    waitForTxReceipt: vi.fn().mockResolvedValue(undefined),
  };
});

const ADMIN = "0x1111111111111111111111111111111111111111"; // fake wallet account
const AUDITOR = "0x9999999999999999999999999999999999999999";
const SUPPLY_CONTROLLER = "0x0000000000000000000000000000000000000044";
const VAULT = "0x0000000000000000000000000000000000000055";

const RECORD = {
  recordId: "record-001",
  status: "Signed" as const,
  metadataDigest: `0x${"aa".repeat(32)}`,
  cid: "bafyrecord",
  amount: "1000000000000000000",
  createdAt: "2026-01-01T00:00:00Z",
  recordKey: `0x${"bb".repeat(32)}`,
  nonce: "7",
  validUntil: 4102444800,
};

const PROJECT = {
  chainId: 31337,
  decimals: 18,
  auditor: AUDITOR,
  profileDigest: `0x${"cc".repeat(32)}`,
  addresses: { supplyController: SUPPLY_CONTROLLER, vault: VAULT },
};

const SIGNATURE = `0x${"ab".repeat(65)}`;

// Use a File-like text() stub for jsdom upload handling
function signedResultFile(auditor: string = AUDITOR): File {
  const content = JSON.stringify({
    formatVersion: "1.0",
    auditor,
    primaryType: "MintAttestation",
    typedDataDigest: `0x${"dd".repeat(32)}`,
    signature: SIGNATURE,
    signedAt: "2026-01-01T00:00:00Z",
  });
  return {
    name: "signed-result.json",
    text: () => Promise.resolve(content),
  } as unknown as File;
}

describe("Assets signature upload → wallet mint", () => {
  let wallet: ReturnType<typeof installFakeWallet>;

  beforeEach(() => {
    wallet = installFakeWallet({ account: ADMIN, chainId: 31337 });
    vi.mocked(api.listRecords)
      .mockReset()
      .mockResolvedValue({ items: [RECORD] });
    vi.mocked(api.getProject).mockReset().mockResolvedValue(PROJECT);
    // No stored profile; metadata skeleton is irrelevant to this mint test
    vi.mocked(api.getProfile)
      .mockReset()
      .mockRejectedValue(
        new ApiError(404, { code: "not_found", message: "no profile" }),
      );
    vi.mocked(sendMint).mockClear().mockResolvedValue("0xminthash");
    vi.mocked(waitForTxReceipt).mockClear().mockResolvedValue(undefined);
  });

  afterEach(() => {
    wallet.uninstall();
  });

  it("assembles the MintAttestation from the record + project and broadcasts SupplyController.mint", async () => {
    renderWithWallet(<Assets />, { connected: true });

    // Wait for the record to load before selecting its ID
    await screen.findByText("record-001");

    fireEvent.change(
      await screen.findByLabelText("Record ID", { selector: "#sigRecordId" }),
      {
        target: { value: "record-001" },
      },
    );
    fireEvent.change(screen.getByLabelText("Signed result"), {
      target: { files: [signedResultFile()] },
    });
    // Wait for the parsed-file summary before minting
    await screen.findByText(AUDITOR);

    fireEvent.click(
      screen.getByRole("button", { name: "Upload signature & mint" }),
    );

    await waitFor(() => expect(sendMint).toHaveBeenCalled());
    const [chainId, from, supplyController, attestation, signature] =
      vi.mocked(sendMint).mock.calls[0];
    expect(chainId).toBe(31337);
    expect(from).toBe(ADMIN);
    expect(supplyController).toBe(SUPPLY_CONTROLLER);
    expect(signature).toBe(SIGNATURE);
    expect(attestation).toEqual({
      auditor: AUDITOR,
      profileDigest: PROJECT.profileDigest,
      recordKey: RECORD.recordKey,
      metadataDigest: RECORD.metadataDigest,
      amount: 1000000000000000000n,
      nonce: 7n,
      validUntil: 4102444800n,
      vault: VAULT,
    });
  });

  it("blocks the mint when the signed-result auditor is not the project's auditor", async () => {
    renderWithWallet(<Assets />, { connected: true });
    await screen.findByText("record-001");

    fireEvent.change(
      await screen.findByLabelText("Record ID", { selector: "#sigRecordId" }),
      {
        target: { value: "record-001" },
      },
    );
    fireEvent.change(screen.getByLabelText("Signed result"), {
      target: {
        files: [signedResultFile("0x1234567890123456789012345678901234567890")],
      },
    });
    await screen.findByText("0x1234567890123456789012345678901234567890");

    fireEvent.click(
      screen.getByRole("button", { name: "Upload signature & mint" }),
    );

    await screen.findByText(/this project's auditor is/i);
    expect(sendMint).not.toHaveBeenCalled();
  });

  it("blocks the mint when no loaded record matches the entered ID", async () => {
    renderWithWallet(<Assets />, { connected: true });
    await screen.findByText("record-001");

    fireEvent.change(
      await screen.findByLabelText("Record ID", { selector: "#sigRecordId" }),
      {
        target: { value: "does-not-exist" },
      },
    );
    fireEvent.change(screen.getByLabelText("Signed result"), {
      target: { files: [signedResultFile()] },
    });
    await screen.findByText(AUDITOR);

    fireEvent.click(
      screen.getByRole("button", { name: "Upload signature & mint" }),
    );

    await screen.findByText(/no loaded record matches this id/i);
    expect(sendMint).not.toHaveBeenCalled();
  });
});

const POLICY_UUID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee";

describe("Assets signer policy (offline signing trust root)", () => {
  it("buildSignerPolicy emits exactly the six keys, chainId as a decimal string, projectId as the UUID", () => {
    const policy = buildSignerPolicy(PROJECT, POLICY_UUID);
    expect(policy).not.toBeNull();
    // The six required policy keys, and nothing else
    expect(Object.keys(policy!).sort()).toEqual([
      "auditor",
      "chainId",
      "controller",
      "profileDigest",
      "projectId",
      "vault",
    ]);
    // chainId is a decimal string, not the API's number
    expect(policy!.chainId).toBe("31337");
    expect(typeof policy!.chainId).toBe("string");
    // projectId is the UUID string, not the bytes32 profile digest
    expect(policy!.projectId).toBe(POLICY_UUID);
    expect(policy!.projectId).not.toBe(PROJECT.profileDigest);
    // Every other value maps straight off the project.
    expect(policy!.controller).toBe(SUPPLY_CONTROLLER);
    expect(policy!.vault).toBe(VAULT);
    expect(policy!.auditor).toBe(AUDITOR);
    expect(policy!.profileDigest).toBe(PROJECT.profileDigest);
  });

  it("returns null when the project isn't deployed or the UUID is missing", () => {
    expect(buildSignerPolicy(undefined, POLICY_UUID)).toBeNull();
    expect(buildSignerPolicy(PROJECT, undefined)).toBeNull();
    // No policy before deployment addresses exist
    expect(
      buildSignerPolicy({ ...PROJECT, addresses: {} }, POLICY_UUID),
    ).toBeNull();
  });

  it("renders the policy block before Records with a download button when deployed", async () => {
    vi.mocked(api.listRecords).mockReset().mockResolvedValue({ items: [] });
    vi.mocked(api.getProject).mockReset().mockResolvedValue(PROJECT);
    vi.mocked(api.getProfile)
      .mockReset()
      .mockResolvedValue({
        profile: {
          profileVersion: "1.0",
          projectId: POLICY_UUID,
          assetType: "gold",
          tokenUnit: "gram",
          tokenDecimals: 18,
        } as unknown as Record<string, never>,
        projectId: POLICY_UUID,
        profileDigest: PROJECT.profileDigest,
        cid: "cid",
        decimals: 18,
        tokenUnit: "gram",
      });

    renderWithWallet(<Assets />);

    const policyHeading = await screen.findByRole("heading", {
      name: /Signer policy/,
    });
    await screen.findByRole("button", { name: "Download policy.json" });
    // The UUID (not the digest) is shown in the block.
    expect(screen.getByText(POLICY_UUID)).toBeInTheDocument();

    // The policy block precedes the Records block in the DOM.
    const recordsHeading = screen.getByRole("heading", { name: "Records" });
    expect(
      policyHeading.compareDocumentPosition(recordsHeading) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("shows a muted note (no download) when the project isn't deployed / no profile", async () => {
    vi.mocked(api.listRecords).mockReset().mockResolvedValue({ items: [] });
    vi.mocked(api.getProject).mockReset().mockRejectedValue(new Error("404"));
    vi.mocked(api.getProfile)
      .mockReset()
      .mockRejectedValue(
        new ApiError(404, { code: "not_found", message: "no profile" }),
      );

    renderWithWallet(<Assets />);

    await screen.findByText(/Create the Asset Profile and deploy first/i);
    expect(
      screen.queryByRole("button", { name: "Download policy.json" }),
    ).toBeNull();
  });
});
