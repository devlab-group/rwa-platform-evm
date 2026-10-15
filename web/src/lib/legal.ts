import type { components } from "./api-types";

export type LegalDescriptor = components["schemas"]["LegalDescriptor"];

/**
 * Returns uri if it parses as an https: URL, or null for callers to render as text
 */
export function safeHttpsHref(uri: string): string | null {
  try {
    return new URL(uri).protocol === "https:" ? uri : null;
  } catch {
    return null;
  }
}

export interface LegalForm {
  wrapper: string;
  issuerLei: string;
  holderRights: string;
  redemptionUri: string;
  redemptionSha256: string;
  maturityDate: string;
  ratingProvider: string;
  ratingGrade: string;
  ratingUri: string;
  ratingSha256: string;
}

export const EMPTY_LEGAL_FORM: LegalForm = {
  wrapper: "",
  issuerLei: "",
  holderRights: "",
  redemptionUri: "",
  redemptionSha256: "",
  maturityDate: "",
  ratingProvider: "",
  ratingGrade: "",
  ratingUri: "",
  ratingSha256: "",
};

const SHA256_RE = /^[0-9a-f]{64}$/;

// Checks complete document references and hash format; the server validates the descriptor
function checkRef(label: string, uri: string, sha256: string): string | null {
  if (!uri && !sha256) return null;
  if (!uri || !sha256) return `${label}: give both the URI and the SHA-256.`;
  if (!SHA256_RE.test(sha256))
    return `${label}: SHA-256 must be 64 lowercase hex characters.`;
  return null;
}

/** Blank wrapper omits legal, preserving digests for profiles without it */
export function buildLegal(f: LegalForm): {
  legal?: LegalDescriptor;
  error?: string;
} {
  if (!f.wrapper) return {};
  const legal: Record<string, unknown> = { wrapper: f.wrapper };
  if (f.issuerLei) legal.issuerLei = f.issuerLei;
  if (f.holderRights) legal.holderRights = f.holderRights;
  if (f.maturityDate) legal.maturityDate = f.maturityDate;

  const redemptionError = checkRef(
    "Redemption terms",
    f.redemptionUri,
    f.redemptionSha256,
  );
  if (redemptionError) return { error: redemptionError };
  if (f.redemptionUri)
    legal.redemptionTerms = {
      uri: f.redemptionUri,
      sha256: f.redemptionSha256,
    };

  const anyRating =
    f.ratingProvider || f.ratingGrade || f.ratingUri || f.ratingSha256;
  if (anyRating) {
    if (!f.ratingProvider || !f.ratingUri || !f.ratingSha256)
      return { error: "Rating: provider, URI and SHA-256 are required." };
    const ratingError = checkRef("Rating", f.ratingUri, f.ratingSha256);
    if (ratingError) return { error: ratingError };
    legal.rating = {
      provider: f.ratingProvider,
      ...(f.ratingGrade ? { grade: f.ratingGrade } : {}),
      uri: f.ratingUri,
      sha256: f.ratingSha256,
    };
  }
  return { legal: legal as unknown as LegalDescriptor };
}

/** Reads legal from stored profile JSON; absent/unparseable input returns null */
export function legalFromProfileJson(
  raw: string | undefined,
): LegalDescriptor | null {
  if (!raw) return null;
  try {
    const legal = (JSON.parse(raw) as { legal?: unknown }).legal;
    return legal && typeof legal === "object"
      ? (legal as LegalDescriptor)
      : null;
  } catch {
    return null;
  }
}
