import { act, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AppLayout } from "./AppLayout";
import { api } from "../lib/client";

vi.mock("../hooks/useAuthSession", () => ({
  useAuthSession: () => ({ token: "t" }),
}));
vi.mock("./WalletConnectButton", () => ({ WalletConnectButton: () => null }));
vi.mock("./NetworkSwitcher", () => ({ NetworkSwitcher: () => null }));
vi.mock("./SessionControls", () => ({ SessionControls: () => null }));
vi.mock("../lib/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/client")>();
  return { ...actual, api: { ...actual.api, getProject: vi.fn() } };
});

let go: (to: string) => void;
function Nav() {
  go = useNavigate();
  return null;
}

describe("AppLayout nav", () => {
  beforeEach(() => vi.mocked(api.getProject).mockReset());

  it("shows the Income link after an in-session deploy without a reload", async () => {
    vi.mocked(api.getProject)
      .mockRejectedValueOnce(new Error("404"))
      .mockResolvedValue({ version: "rwa-v3" } as never);
    render(
      <MemoryRouter initialEntries={["/admin/setup"]}>
        <Nav />
        <Routes>
          <Route path="/admin" element={<AppLayout />}>
            <Route path="setup" element={<p>setup</p>} />
            <Route path="assets" element={<p>assets</p>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await screen.findByText("setup");
    expect(screen.queryByRole("link", { name: "Income" })).toBeNull();

    act(() => go("/admin/assets"));
    expect(
      await screen.findByRole("link", { name: "Income" }),
    ).toBeInTheDocument();

    // a known version is kept: navigating again sends no request and keeps the link
    const calls = vi.mocked(api.getProject).mock.calls.length;
    act(() => go("/admin/setup"));
    await screen.findByText("setup");
    act(() => go("/admin/assets"));
    await screen.findByText("assets");
    expect(vi.mocked(api.getProject).mock.calls.length).toBe(calls);
    expect(screen.getByRole("link", { name: "Income" })).toBeInTheDocument();
  });
});
