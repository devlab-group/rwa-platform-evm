import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import "./styles/index.scss";
import App from "./App";
import { WalletProvider } from "./context/WalletContext";

const container = document.getElementById("root");
if (!container) throw new Error("Missing #root element");

// Nothing to restore at boot: the only credential this app keeps is the
// X-Wallet-Session token, stored in localStorage by address and read once a
// wallet connects (see lib/walletSession.ts)
createRoot(container).render(
  <StrictMode>
    <WalletProvider>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </WalletProvider>
  </StrictMode>,
);
