import { useEffect, useState, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useLocation } from "react-router-dom";
import { isDemoPath } from "./demo";
import { retryUnlessLimit } from "./proLimit";

// The sample truck (/demo) and the real app are two sides with two caches. The demo answers some calls itself (api.ts: inDemo()), for
// example "Rivian sign-in is off"; read from the real app inside the cache's 30 s, that answer showed the visitor "Live Rivian client is
// disabled". Each side gets its own QueryClient, picked by the route, so nothing cached on one side is ever read on the other, whichever way the
// visitor crosses (link, redirect, Back). The provider is keyed by side so every query re-subscribes to the right client before it renders.
// The demo's cache is dropped when the visitor leaves it.
function newDemoClient() {
  return new QueryClient({ defaultOptions: { queries: { staleTime: 30_000, refetchOnWindowFocus: false, retry: retryUnlessLimit(3) } } });
}

export function SideQueryProvider({ appClient, children }: { appClient: QueryClient; children: ReactNode }) {
  const demo = isDemoPath(useLocation().pathname);
  const [demoClient] = useState(newDemoClient);
  useEffect(() => {
    if (!demo) demoClient.clear();
  }, [demo, demoClient]);
  return (
    <QueryClientProvider key={demo ? "demo" : "app"} client={demo ? demoClient : appClient}>
      {children}
    </QueryClientProvider>
  );
}
