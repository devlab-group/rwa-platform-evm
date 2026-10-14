// Formats minimal-unit amounts and converts whole-unit input to integer units
// with toMinimalUnits. API amounts stay decimal strings or BigInt rather than
// JavaScript numbers or JSON floats.
import { formatUnits, parseUnits } from "viem";

/**
 * Renders a minimal-units amount string for display. With `decimals` (e.g.
 * Project.decimals for RWA amounts) the value is scaled to whole units;
 * without, the raw integer is digit-grouped and the caller must label the unit
 * (see TransactionPreview).
 */
export function formatTokenAmount(raw: string, decimals?: number): string {
  if (raw === "" || raw === undefined) return "-";
  try {
    if (decimals === undefined) {
      return groupDigits(raw);
    }
    const value = formatUnits(BigInt(raw), decimals);
    return groupDecimal(value);
  } catch {
    return raw;
  }
}

/**
 * Thrown by `toMinimalUnits` when the entered value isn't a valid non-negative
 * amount for a token with `decimals` fractional places. `.message` echoes the
 * typed value and is meant to be shown to the user as-is.
 */
export class AmountFormatError extends Error {
  constructor(
    public readonly value: string,
    public readonly decimals: number,
  ) {
    const precision =
      decimals > 0
        ? ` Enter a whole number or a decimal with up to ${decimals} fractional digit${decimals === 1 ? "" : "s"}.`
        : " Enter a whole number (this token has no fractional units).";
    super(`"${value}" is not a valid amount.${precision}`);
    this.name = "AmountFormatError";
  }
}

/**
 * Converts whole-unit input, such as "1.5", to minimal-unit decimal strings
 * using viem parseUnits. Rejects empty, negative and over-precise input before
 * coercion or rounding: "1.2345678" at 6 decimals must not become "1234568".
 */
export function toMinimalUnits(human: string, decimals: number): string {
  const value = human.trim();
  // Non-negative decimal only: "5", "5.", "5.5", ".5", "00.50". Rejects "",
  // ".", "-1", "1e3", "1,000", "abc", "1.5.5".
  if (!/^(\d+\.?\d*|\.\d+)$/.test(value)) {
    throw new AmountFormatError(human, decimals);
  }
  const frac = value.includes(".") ? value.slice(value.indexOf(".") + 1) : "";
  if (frac.length > decimals) {
    // More fractional digits than the token supports: parseUnits would round
    // and change the amount, so reject
    throw new AmountFormatError(human, decimals);
  }
  return parseUnits(value, decimals).toString();
}

function groupDigits(raw: string): string {
  const negative = raw.startsWith("-");
  const digits = negative ? raw.slice(1) : raw;
  if (!/^\d+$/.test(digits)) return raw;
  const grouped = digits.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return negative ? `-${grouped}` : grouped;
}

function groupDecimal(value: string): string {
  const [whole, frac] = value.split(".");
  const groupedWhole = groupDigits(whole);
  return frac ? `${groupedWhole}.${frac}` : groupedWhole;
}

export function shortenAddress(
  address: string | undefined | null,
  chars = 4,
): string {
  if (!address) return "-";
  if (address.length <= chars * 2 + 2) return address;
  return `${address.slice(0, chars + 2)}…${address.slice(-chars)}`;
}

export function formatUnixSeconds(seconds: number | undefined | null): string {
  if (!seconds) return "-";
  return new Date(seconds * 1000).toLocaleString();
}

export function formatIsoTimestamp(iso: string | undefined | null): string {
  if (!iso) return "-";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}
