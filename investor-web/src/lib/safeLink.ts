/** Returns uri if it parses as an https: URL, or null for callers to render as text */
export function httpsHref(uri: string): string | null {
  try {
    return new URL(uri).protocol === "https:" ? uri : null;
  } catch {
    return null;
  }
}
