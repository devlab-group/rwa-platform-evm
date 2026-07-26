import { useState } from "react";
import { useWalletContext } from "../context/walletContextValue";
import { api, ApiError } from "../lib/client";
import { setSession } from "../lib/authSession";
import { shortenAddress } from "../lib/format";

/**
 * Admin sign-in via wallet signature. Replaces the old operator API-key
 * exchange. The flow: the connected wallet address is sent to
 * `POST /auth/challenge`, the wallet personal_signs the returned message, and
 * the signature goes to `POST /auth/session`. If the server recovers the
 * project admin as the signer it returns a JWT, which is stored (IndexedDB via
 * lib/authSession.ts) and attached as a bearer token on every request.
 *
 * Rendered by AppLayout in place of the admin `<Outlet/>` whenever there is no
 * live admin session. Connecting a wallet is handled by the global header
 * Connect button; this screen only drives the challenge → sign → verify step
 * once a wallet is present.
 */
export function Login() {
  const wallet = useWalletContext();
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | undefined>();

  async function handleSignIn() {
    if (!wallet.address) return;
    setSubmitting(true);
    setError(undefined);
    try {
      const { message } = await api.createAuthChallenge({
        address: wallet.address,
      });
      const signature = await wallet.signMessage(message);
      const result = await api.createSession({
        address: wallet.address,
        signature,
      });
      setSession({
        token: result.token,
        expiresAt: result.expiresAt,
        role: result.role,
        address: result.address,
      });
    } catch (err) {
      setError(loginErrorMessage(err));
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="card login-card">
      <h1>Admin sign-in</h1>
      <p>
        Admin access is authenticated by wallet signature. Connect the project
        admin wallet, then sign a one-time challenge to start a session. Nothing
        is sent on-chain and no gas is spent — the signature only proves you
        control the admin address.
      </p>

      {wallet.address ? (
        <>
          <dl className="tx-preview__grid">
            <div className="tx-preview__row">
              <dt>Connected wallet</dt>
              <dd title={wallet.address}>{shortenAddress(wallet.address)}</dd>
            </div>
          </dl>
          <button
            type="button"
            className="button button--primary"
            onClick={handleSignIn}
            disabled={submitting}
          >
            {submitting ? "Waiting for signature…" : "Sign in with wallet"}
          </button>
        </>
      ) : (
        <button
          type="button"
          className="button button--primary"
          onClick={wallet.connect}
          disabled={wallet.connecting}
        >
          {wallet.connecting ? "Connecting…" : "Connect wallet to sign in"}
        </button>
      )}

      {error && (
        <p className="async-state--error" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}

/**
 * Turns a login failure into an operator-facing message. A 401 specifically
 * means the recovered signer is not the configured project admin; a rejected
 * signature is the common non-error case (the user dismissed the wallet
 * prompt) and shouldn't read like a fault.
 */
function loginErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401)
      return "This wallet is not the project admin. Switch to the admin account and try again.";
    return err.message;
  }
  // viem/EIP-1193 user-rejection surfaces as code 4001 (or a message
  // mentioning rejection); treat it as a benign cancellation.
  if (isUserRejection(err))
    return "Signature request was rejected. Sign the challenge to sign in.";
  return "Sign-in failed. Try again.";
}

function isUserRejection(err: unknown): boolean {
  if (typeof err !== "object" || err === null) return false;
  const code = (err as { code?: unknown }).code;
  if (code === 4001) return true;
  const message = (err as { message?: unknown }).message;
  return typeof message === "string" && /reject/i.test(message);
}
