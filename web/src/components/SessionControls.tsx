import { useAuthSession } from "../hooks/useAuthSession";
import { clearSession } from "../lib/authSession";
import { shortenAddress } from "../lib/format";

/**
 * Shows admin JWT status and Log out; Login handles authentication. Logout clears the
 * local token without revocation or wallet disconnect; Header Disconnect clears both.
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
