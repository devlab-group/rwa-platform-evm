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

/** Browser wallet-submission state, separate from server TransactionChainStatus */
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
  reorged: "Reorged - resubmission required",
};

export interface TransactionPreviewProps {
  title: string;
  chainId: number;
  /**
   * Project contract addresses, including the target; lib/abis.ts and lib/wallet.ts encode calldata
   * locally
   */
  addresses: AddressField[];
  amounts: AmountField[];
  priceSide?: "purchase" | "redemption";
  quoteTokenSymbol?: string;
  slippageBps?: number;
  confirmationState: ConfirmationState;
  /** Call-specific caveat appended to the payment disclaimer */
  notice?: string;
  children?: ReactNode;
}

/**
 * Shows chain, addresses, amounts/units, price side, quote token, slippage bounds and
 * confirmation state before wallet approval
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
        Wait for mining and the platform&apos;s finality threshold before
        treating amounts as final. A pending or funded redemption does not
        guarantee payment.
        {notice ? ` ${notice}` : ""}
      </p>

      {children && <div className="tx-preview__actions">{children}</div>}
    </section>
  );
}
