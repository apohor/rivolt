// Rivolt brand mark — lightning bolt in a square.
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
      <rect
        x="4"
        y="4"
        width="56"
        height="56"
        rx="12"
        fill="none"
        stroke="currentColor"
        strokeWidth="3.5"
        opacity="0.9"
      />
      <path
        d="M36 10 L18 36 H30 L26 54 L46 26 H34 Z"
        fill="currentColor"
      />
    </svg>
  );
}
