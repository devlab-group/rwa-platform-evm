import { lazy } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import { AppLayout } from "./components/AppLayout";

// Code-split per route: React.lazy + a dynamic import() per screen, so the
// initial bundle only ships app shell + whichever screen is first requested
// (each named export is re-wrapped as a default for lazy()'s sake — the
// route files keep their normal named-export convention). Suspense boundary
// lives once in AppLayout, around the shared <Outlet/>.
const Setup = lazy(() =>
  import("./routes/admin/Setup").then((m) => ({ default: m.Setup })),
);
const Assets = lazy(() =>
  import("./routes/admin/Assets").then((m) => ({ default: m.Assets })),
);
const Compliance = lazy(() =>
  import("./routes/admin/Compliance").then((m) => ({ default: m.Compliance })),
);
const InventorySales = lazy(() =>
  import("./routes/admin/InventorySales").then((m) => ({
    default: m.InventorySales,
  })),
);
const Redemptions = lazy(() =>
  import("./routes/admin/Redemptions").then((m) => ({
    default: m.Redemptions,
  })),
);
const Transactions = lazy(() =>
  import("./routes/admin/Transactions").then((m) => ({
    default: m.Transactions,
  })),
);
const Security = lazy(() =>
  import("./routes/admin/Security").then((m) => ({ default: m.Security })),
);

export default function App() {
  return (
    <Routes>
      <Route element={<AppLayout />}>
        <Route index element={<Navigate to="/admin/setup" replace />} />
        <Route path="admin">
          <Route index element={<Navigate to="setup" replace />} />
          <Route path="setup" element={<Setup />} />
          <Route path="assets" element={<Assets />} />
          <Route path="compliance" element={<Compliance />} />
          <Route path="inventory-sales" element={<InventorySales />} />
          <Route path="redemptions" element={<Redemptions />} />
          <Route path="transactions" element={<Transactions />} />
          <Route path="security" element={<Security />} />
        </Route>
        <Route path="*" element={<Navigate to="/admin/setup" replace />} />
      </Route>
    </Routes>
  );
}
