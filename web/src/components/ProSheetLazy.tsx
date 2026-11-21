import { lazy, Suspense, useEffect, type ComponentProps } from "react";

// The Pro sheet is opened rarely, so it is its own chunk. Whatever shows a button that opens it mounts <ProSheetPreload/> beside it: the chunk
// is fetched then (and kept by the browser), so the click opens the sheet at once, with no flash, and still works offline after load.
const load = () => import("./ProSheet");
const Sheet = lazy(() => load().then((m) => ({ default: m.ProSheet })));

export const ProSheetPreload = () => {
  useEffect(() => void load().catch(() => {}), []);
  return null;
};

export const ProSheet = (props: ComponentProps<typeof Sheet>) => (
  <Suspense fallback={null}>
    <Sheet {...props} />
  </Suspense>
);
