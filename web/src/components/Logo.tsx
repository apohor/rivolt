// Caffeine brand mark — espresso cup with rising steam.
// Keep this in sync with web/public/favicon.svg.

type Props = {
  size?: number;
  className?: string;
};

export default function Logo({ size = 24, className = "" }: Props) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 64 64"
      width={size}
      height={size}
      className={className}
      aria-hidden="true"
    >
      <path
        d="M22 10c0 4-4 6-4 10s4 6 4 10"
        stroke="currentColor"
        strokeWidth="3"
        strokeLinecap="round"
        fill="none"
        opacity="0.9"
      />
      <path
        d="M32 10c0 4-4 6-4 10s4 6 4 10"
        stroke="currentColor"
        strokeWidth="3"
        strokeLinecap="round"
        fill="none"
        opacity="0.55"
      />
      <path
        d="M42 10c0 4-4 6-4 10s4 6 4 10"
        stroke="currentColor"
        strokeWidth="3"
        strokeLinecap="round"
        fill="none"
        opacity="0.9"
      />
      <path
        d="M14 34h30v8a10 10 0 0 1-10 10H24a10 10 0 0 1-10-10v-8z"
        fill="currentColor"
        opacity="0.9"
      />
      <path
        d="M44 36h4a6 6 0 0 1 0 12h-4"
        stroke="currentColor"
        strokeWidth="3.5"
        fill="none"
        strokeLinecap="round"
        opacity="0.9"
      />
    </svg>
  );
}
