import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import "./styles/index.scss";
import App from "./App";
import { WalletProvider } from "./context/WalletContext";

const container = document.getElementById("root");
if (!container) throw new Error("Missing #root element");

// Nothing to rehydrate at boot: this app holds no long-lived credential. The
// only session it keeps is the wallet-scoped X-Wallet-Session token, which
// lives in localStorage keyed by address and is read lazily once a wallet
// connects (see lib/walletSession.ts).
createRoot(container).render(
  <StrictMode>
    <WalletProvider>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </WalletProvider>
  </StrictMode>,
);
