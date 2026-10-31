import { retryUnlessLimit } from "./lib/proLimit";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter } from "react-router-dom";
import App from "./App";
import { VersionBar } from "./components/VersionBar";
import { isTermsRequiredError } from "./lib/terms";
import { registerServiceWorker } from "./pwa";
// Catches the browser's install prompt early (it fires once, at load); the first-charge card offers it.
import "./lib/installPrompt";
import "./index.css";

// A request the server refused with terms_required means the Terms gate should be showing: refetch who we are.
const termsRequired = (e: unknown) => {
  if (isTermsRequiredError(e)) void queryClient.invalidateQueries({ queryKey: ["auth", "me"] });
};
const queryClient: QueryClient = new QueryClient({
  queryCache: new QueryCache({ onError: termsRequired }),
  mutationCache: new MutationCache({ onError: termsRequired }),
  defaultOptions: {
    // A free account's 402 is an answer, never retried; everything else keeps react-query's three tries.
    queries: { staleTime: 30_000, refetchOnWindowFocus: false, retry: retryUnlessLimit(3) },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
        <VersionBar />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
);

// Register the PWA service worker after the app mounts. No-op on
// insecure origins, non-service-worker browsers, or during SSR.
registerServiceWorker();
