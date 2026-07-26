import { Suspense, lazy } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import { InvestorHeader } from "./components/InvestorHeader";

// One screen, still code-split: the shell (header, skip link) paints while the
// investor bundle loads. The route file keeps its named-export convention, so
// it's re-wrapped as a default for lazy()'s sake.
const Investor = lazy(() =>
  import("./routes/investor/Investor").then((m) => ({ default: m.Investor })),
);

export default function App() {
  return (
    <div className="app-shell">
      <a href="#main-content" className="skip-link">
        Skip to main content
      </a>

      <InvestorHeader />

      <main id="main-content" className="app-main" tabIndex={-1}>
        <Suspense
          fallback={
            <div className="async-state async-state--loading" role="status">
              Loading…
            </div>
          }
        >
          <Routes>
            <Route path="/" element={<Investor />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </Suspense>
      </main>
    </div>
  );
}
