import { useState } from "react";
import { useWalletContext } from "../context/walletContextValue";
import { api, ApiError } from "../lib/client";
import { setSession } from "../lib/authSession";
import { shortenAddress } from "../lib/format";

/**
 * Requests /auth/challenge, personal_signs its message, then exchanges the signature at
 * /auth/session. lib/authSession.ts persists the JWT and lib/client.ts sends it;
 * AppLayout displays login without a live session.
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
        Connect the admin wallet and sign a one-time challenge. The signature
        proves ownership without an on-chain transaction or gas fee.
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

// Explains login failures: the server uses one 401 for wrong admin, bad signature or
// expired/replayed challenge. Wallet rejection gets a milder message.
function loginErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401)
      return "This wallet is not the project admin, or the challenge expired. Select the admin account and retry.";
    return err.message;
  }
  // Recognize user rejection by EIP-1193 code 4001 or a message containing "reject"
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
