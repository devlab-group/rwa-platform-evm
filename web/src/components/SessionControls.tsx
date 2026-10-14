import { useAuthSession } from "../hooks/useAuthSession";
import { clearSession } from "../lib/authSession";
import { shortenAddress } from "../lib/format";

/**
 * Session indicator and Log out button in the admin nav. The session is the JWT
 * issued after the wallet-signature login (components/Login.tsx). The server
 * cannot revoke it, so logging out drops the token in the browser. The wallet
 * stays connected; the header Disconnect button drops both.
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
