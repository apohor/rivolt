import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";

// The debounced place search over /api/geocode that both the planner's typeahead and the front door's "Where to?" share.
// `active` is true once there is enough typed to search; `pending` covers the debounce and the request; with no results and nothing pending,
// `failed` says the search errored and `empty` that it found nothing.
export function useGeocode(query: string) {
  const [debounced, setDebounced] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setDebounced(query), 150);
    return () => clearTimeout(t);
  }, [query]);
  const results = useQuery({
    queryKey: ["geocode", debounced],
    queryFn: () => backend.geocode(debounced, 8),
    enabled: debounced.trim().length >= 2,
    // One retry, not the app's three: a failed search says so within a second or two.
    retry: 1,
    staleTime: 5 * 60 * 1000,
  });
  const active = query.trim().length >= 2;
  const pending = query !== debounced || results.isFetching;
  const items = results.data ?? [];
  return {
    items,
    active,
    pending,
    failed: active && !pending && results.isError,
    empty: active && !pending && results.isFetched && !results.isError && items.length === 0,
  };
}
