import { useState } from "react";
import { useWalletContext } from "../context/walletContextValue";
import { api, ApiError } from "../lib/client";
import { setSession } from "../lib/authSession";
import { shortenAddress } from "../lib/format";

/**
 * Admin wallet login: request POST /auth/challenge, personal_sign the message,
 * then exchange the signature at POST /auth/session. A configured-admin match
 * returns the JWT persisted by lib/authSession.ts and sent by lib/client.ts.
 * AppLayout shows this connect/sign/verify screen while no live session exists.
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
        Connect the project admin wallet and sign a one-time challenge to start
        a session. The signature proves wallet ownership; it sends no on-chain
        transaction and costs no gas.
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

// Turns a login failure into an operator-facing message. The server returns the
// same 401 for a non-admin signer and for a bad, expired or replayed challenge.
// A dismissed wallet prompt gets a milder message than a fault.
function loginErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401)
      return "This wallet is not the project admin, or the sign-in challenge expired. Switch to the admin account and try again.";
    return err.message;
  }
  // EIP-1193 reports a user rejection as code 4001; some wallets only put
  // "reject" in the message
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
