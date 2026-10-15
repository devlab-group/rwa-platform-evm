import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { LegalDisclosure } from "./LegalDisclosure";

const HASH = "a".repeat(64);

describe("LegalDisclosure", () => {
  it("renders nothing without legal", () => {
    const { container } = render(<LegalDisclosure legal={undefined} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("renders every field, with https references as links", () => {
    render(
      <LegalDisclosure
        legal={{
          wrapper: "share",
          issuerLei: "5493000EXAMPLE0LEI67",
          holderRights: "Pro-rata claim",
          maturityDate: "2030-12-31",
          redemptionTerms: {
            uri: "https://issuer.example/t.pdf",
            sha256: HASH,
          },
          rating: {
            provider: "Example Ratings",
            grade: "A",
            uri: "ipfs://bafy",
            sha256: "b".repeat(64),
          },
        }}
      />,
    );
    expect(
      screen.getByRole("heading", { name: "What this token represents" }),
    ).toBeInTheDocument();
    for (const t of [
      "share",
      "5493000EXAMPLE0LEI67",
      "Pro-rata claim",
      "2030-12-31",
      "Example Ratings: A",
    ])
      expect(screen.getByText(t, { exact: false })).toBeInTheDocument();
    expect(screen.getByText(HASH)).toBeInTheDocument();
    const links = screen.getAllByRole("link");
    expect(links).toHaveLength(1);
    expect(links[0]).toHaveAttribute("href", "https://issuer.example/t.pdf");
    expect(screen.getByText("ipfs://bafy")).toBeInTheDocument();
  });

  it("renders a javascript: uri as text, not a link", () => {
    render(
      <LegalDisclosure
        legal={{
          wrapper: "claim",
          redemptionTerms: { uri: "javascript:alert(1)", sha256: HASH },
        }}
      />,
    );
    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.getByText("javascript:alert(1)")).toBeInTheDocument();
  });
});
