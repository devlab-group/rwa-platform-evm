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

// Load a persisted admin JWT from IndexedDB in the background. Mounting does
// not wait for it, so the shell renders at once; when hydration resolves,
// subscribers are notified and the admin area replaces the login screen. A
// failed or empty load leaves the app signed out.
void hydrateSession();
