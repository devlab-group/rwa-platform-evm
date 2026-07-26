import type { ReactNode } from "react";
import { formatTokenAmount, shortenAddress } from "../lib/format";

export interface AddressField {
  label: string;
  address: string;
}

export interface AmountField {
  label: string;
  raw: string;
  decimals?: number;
  symbol: string;
}

/** Client-side wallet-submission lifecycle; distinct from indexer TransactionChainStatus. */
export type ConfirmationState =
  | "draft"
  | "awaiting-signature"
  | "submitted"
  | "confirmed"
  | "reverted"
  | "reorged";

const CONFIRMATION_LABEL: Record<ConfirmationState, string> = {
  draft: "Not yet submitted",
  "awaiting-signature": "Awaiting wallet signature",
  submitted: "Submitted, unconfirmed",
  confirmed: "Confirmed",
  reverted: "Reverted",
  reorged: "Reorged — resubmission required",
};

export interface TransactionPreviewProps {
  title: string;
  chainId: number;
  /**
   * The contracts involved, one of which is the transaction's target. The
   * calldata is encoded in the browser from these very addresses (lib/abis.ts
   * + lib/wallet.ts), so what the preview shows is what the wallet is handed.
   */
  addresses: AddressField[];
  amounts: AmountField[];
  priceSide?: "purchase" | "redemption";
  quoteTokenSymbol?: string;
  slippageBps?: number;
  confirmationState: ConfirmationState;
  /** Extra call-specific caveat, appended after the standard non-guarantee notice. */
  notice?: string;
  children?: ReactNode;
}

/**
 * Mandatory pre-approval summary: every wallet or Safe transaction must be
 * previewed with chainId, addresses, amounts + units, price side, quote token,
 * slippage bounds, and confirmation state before the user can approve it.
 */
export function TransactionPreview({
  title,
  chainId,
  addresses,
  amounts,
  priceSide,
  quoteTokenSymbol,
  slippageBps,
  confirmationState,
  notice,
  children,
}: TransactionPreviewProps) {
  return (
    <section className="tx-preview" aria-label={title}>
      <h3 className="tx-preview__title">{title}</h3>

      <dl className="tx-preview__grid">
        <div className="tx-preview__row">
          <dt>Chain ID</dt>
          <dd>{chainId}</dd>
        </div>

        {addresses.map((f) => (
          <div className="tx-preview__row" key={f.label}>
            <dt>{f.label}</dt>
            <dd title={f.address}>{shortenAddress(f.address)}</dd>
          </div>
        ))}

        {amounts.map((f) => (
          <div className="tx-preview__row" key={f.label}>
            <dt>{f.label}</dt>
            <dd>
              {formatTokenAmount(f.raw, f.decimals)} {f.symbol}
            </dd>
          </div>
        ))}

        {priceSide && (
          <div className="tx-preview__row">
            <dt>Price side</dt>
            <dd className="tx-preview__price-side" data-side={priceSide}>
              {priceSide === "purchase" ? "Purchase price" : "Redemption price"}
            </dd>
          </div>
        )}

        {quoteTokenSymbol && (
          <div className="tx-preview__row">
            <dt>Quote token</dt>
            <dd>{quoteTokenSymbol}</dd>
          </div>
        )}

        {slippageBps !== undefined && (
          <div className="tx-preview__row">
            <dt>Slippage bound</dt>
            <dd>±{(slippageBps / 100).toFixed(2)}%</dd>
          </div>
        )}

        <div className="tx-preview__row">
          <dt>Confirmation state</dt>
          <dd>
            <span
              className="tx-preview__confirmation"
              data-state={confirmationState}
            >
              {CONFIRMATION_LABEL[confirmationState]}
            </span>
          </dd>
        </div>
      </dl>

      <p className="tx-preview__notice">
        This is a preview of an on-chain transaction. Values are not final until
        the transaction is mined and has passed the platform&apos;s finality
        threshold; a pending or funded redemption is not a guarantee of payment.
        {notice ? ` ${notice}` : ""}
      </p>

      {children && <div className="tx-preview__actions">{children}</div>}
    </section>
  );
}
