import { useAuthSession } from "../hooks/useAuthSession";
import { clearSession } from "../lib/authSession";
import { shortenAddress } from "../lib/format";

/**
 * Admin-session indicator + log out, shown in the admin nav. The session is a
 * wallet-signature-minted JWT (see components/Login.tsx); there is no
 * server-side revocation, so logging out simply drops the token client-side.
 * This leaves the wallet itself connected (use the header Disconnect button to
 * drop the wallet too) so an operator can step down from admin without losing
 * their wallet connection.
 */
export function SessionControls() {
  const session = useAuthSession();

  if (!session) {
    return (
      <div className="app-nav__session app-nav__session--signed-out">
        Not signed in
      </div>
    );
  }

  return (
    <div className="app-nav__session">
      <span className="app-nav__session-role" title={session.address}>
        Signed in as {session.role} ({shortenAddress(session.address)})
      </span>
      <button
        type="button"
        className="button button--secondary"
        onClick={() => clearSession()}
      >
        Log out
      </button>
    </div>
  );
}
