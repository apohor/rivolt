import { useQuery } from "@tanstack/react-query";
import { backend } from "../lib/api";
import { stopImpersonation } from "../lib/impersonation";

// SignOutButton hides itself when auth is disabled on the server
// (whoami returns null). When present, clicking it clears the
// session cookie server-side and sends the browser to /login.
// Using window.location instead of react-router so the full SPA
// state is dropped — we don't want cached queries leaking between
// users on a shared-browser install.
export function SignOutButton({ label = "Sign out", solid = false }: { label?: string; solid?: boolean } = {}) {
  const me = useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => backend.whoami(),
    staleTime: 5 * 60_000,
  });
  if (!me.data) return null;
  return (
    <button
      type="button"
      onClick={async () => {
        // Impersonation is read-only server-side (see
        // impersonationMW): a POST /api/auth/logout sent with the
        // X-Rivolt-Impersonate header still attached would 405
        // instead of actually signing out. Clear it first so this
        // always signs the real admin (or user) out cleanly.
        stopImpersonation();
        try {
          await backend.logout();
        } finally {
          window.location.assign("/login");
        }
      }}
      title={`Signed in as ${me.data.username}`}
      className={
        solid
          ? "rounded-md border border-neutral-600 bg-neutral-800 px-3 py-1.5 text-sm font-semibold text-neutral-100 hover:border-neutral-500 hover:bg-neutral-700"
          : "rounded-full border border-neutral-800 bg-neutral-900 px-3 py-1 text-xs text-neutral-300 hover:border-neutral-700 hover:text-neutral-100"
      }
    >
      {label}
    </button>
  );
}
