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

// Hydrate the admin JWT from IndexedDB without delaying the shell. Subscribers refresh
// the admin area after hydration; a failed or empty load leaves it signed out.
void hydrateSession();
