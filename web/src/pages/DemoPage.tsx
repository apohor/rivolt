import type { ReactNode } from "react";
import { Navigate, useSearchParams } from "react-router-dom";

// /demo is the demo's Now tab. Old links used /demo?tab=money|activity;
// send them to the real routes.
export default function DemoIndex({ home }: { home: ReactNode }) {
  const [params] = useSearchParams();
  const tab = params.get("tab");
  if (tab === "money") return <Navigate to="/demo/money" replace />;
  if (tab === "activity") return <Navigate to="/demo/activity" replace />;
  return <>{home}</>;
}
