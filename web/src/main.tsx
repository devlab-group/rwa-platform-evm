import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import "./styles/index.scss";
import App from "./App";
import { WalletProvider } from "./context/WalletContext";
import { hydrateSession } from "./lib/authSession";

const container = document.getElementById("root");
if (!container) throw new Error("Missing #root element");

createRoot(container).render(
  <StrictMode>
    <WalletProvider>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </WalletProvider>
  </StrictMode>,
);

// Rehydrate any persisted admin JWT from IndexedDB in the background. Mounting
// is not blocked on it (so the shell — skip link, nav — is present immediately);
// when hydration resolves, authSession notifies subscribers and the admin area
// re-renders from the login screen to the requested screen. A failed/empty
// hydrate just leaves the app signed-out.
void hydrateSession();
