import type { components } from "../../lib/api-types";
import { httpsHref } from "../../lib/safeLink";

type LegalDescriptor = components["schemas"]["LegalDescriptor"];

function Reference({ uri, sha256 }: { uri: string; sha256: string }) {
  const href = httpsHref(uri);
  return (
    <>
      {href ? (
        <a href={href} rel="noopener noreferrer">
          {uri}
        </a>
      ) : (
        <span>{uri}</span>
      )}
      <br />
      SHA-256: <span className="mono">{sha256}</span>
    </>
  );
}

/** Shows the issuer's legal descriptor when present */
export function LegalDisclosure({
  legal,
}: {
  legal: LegalDescriptor | undefined;
}) {
  if (!legal) return null;
  const rows: [string, React.ReactNode][] = [["Legal form", legal.wrapper]];
  if (legal.issuerLei) rows.push(["Issuer LEI", legal.issuerLei]);
  if (legal.holderRights) rows.push(["Holder rights", legal.holderRights]);
  if (legal.maturityDate) rows.push(["Maturity date", legal.maturityDate]);
  if (legal.redemptionTerms)
    rows.push(["Redemption terms", <Reference {...legal.redemptionTerms} />]);
  if (legal.rating)
    rows.push([
      "Rating",
      <>
        {legal.rating.provider}
        {legal.rating.grade ? `: ${legal.rating.grade}` : ""}
        <br />
        <Reference uri={legal.rating.uri} sha256={legal.rating.sha256} />
      </>,
    ]);
  return (
    <section className="card" aria-labelledby="legal-heading">
      <h2 id="legal-heading">What this token represents</h2>
      <p className="field__hint">
        The platform records the issuer's statements without verifying them.
      </p>
      <dl className="tx-preview__grid">
        {rows.map(([label, value]) => (
          <div className="tx-preview__row" key={label}>
            <dt>{label}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
