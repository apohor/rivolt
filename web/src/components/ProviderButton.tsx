import type { OIDCProvider } from "../lib/api";

// The Apple logo (the mark Apple's Sign in with Apple guidelines ask for beside "Continue with Apple"; simple-icons, CC0).
const APPLE_LOGO =
  "M12.152 6.896c-.948 0-2.415-1.078-3.96-1.04-2.04.027-3.91 1.183-4.961 3.014-2.117 3.675-.546 9.103 1.519 12.09 1.013 1.454 2.208 3.09 3.792 3.039 1.52-.065 2.09-.987 3.935-.987 1.831 0 2.35.987 3.96.948 1.637-.026 2.676-1.48 3.676-2.948 1.156-1.688 1.636-3.325 1.662-3.415-.039-.013-3.182-1.221-3.22-4.857-.026-3.04 2.48-4.494 2.597-4.559-1.429-2.09-3.623-2.324-4.39-2.376-2-.156-3.675 1.09-4.61 1.09zM15.53 3.83c.843-1.012 1.4-2.427 1.245-3.83-1.207.052-2.662.805-3.532 1.818-.78.896-1.454 2.338-1.273 3.714 1.338.104 2.715-.688 3.559-1.701";

export const isApple = (p: Pick<OIDCProvider, "name">): boolean => p.name.toLowerCase() === "apple";

const SHAPE = "flex w-full items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium transition";

// ProviderButton: one "Continue with …" button for an identity provider the server lists. Every provider gets the same width, height and
// type size (Apple's rules ask for equal prominence); Apple's own is white with black text and its logo, outlined so it holds on the
// light theme as well as the dark. The wording is Apple's: "Continue with Apple".
export function ProviderButton({ provider, onClick }: { provider: OIDCProvider; onClick: () => void }) {
  if (isApple(provider)) {
    return (
      <button
        type="button"
        data-provider="apple"
        onClick={onClick}
        className={`${SHAPE} border border-neutral-400 bg-white text-black hover:bg-neutral-100`}
      >
        <svg aria-hidden="true" viewBox="0 0 24 24" width="16" height="16" fill="currentColor" className="shrink-0">
          <path d={APPLE_LOGO} />
        </svg>
        Continue with Apple
      </button>
    );
  }
  return (
    <button
      type="button"
      data-provider={provider.name}
      onClick={onClick}
      className={`${SHAPE} border border-neutral-800 bg-neutral-900 text-neutral-100 hover:border-emerald-700 hover:bg-neutral-850`}
    >
      Continue with {provider.display_name}
    </button>
  );
}
