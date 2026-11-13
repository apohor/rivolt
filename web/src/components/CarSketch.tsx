import { R1S, R1T } from "./carSketchArt";

// Side-view line drawing shown when a vehicle has no Rivian picture, or the picture fails to load.
// R1T for model R1T, R1S for R1S and anything else. The drawing reads --car-* variables (see index.css),
// so one file serves both themes. The drawings are JSX in carSketchArt.tsx.
export function CarSketch({
  model,
  charging = false,
  className = "",
}: {
  model?: string | null;
  charging?: boolean;
  className?: string;
}) {
  const isR1T = model?.trim().toUpperCase() === "R1T";
  return (
    <div
      data-testid="now-car-sketch"
      data-model={isR1T ? "R1T" : "R1S"}
      className={`relative w-full max-w-[220px] [&>svg:first-child]:h-auto [&>svg:first-child]:w-full ${className}`}
    >
      {isR1T ? <R1T /> : <R1S />}
      {charging && (
        <svg
          viewBox="-12 -12 24 24"
          aria-hidden="true"
          className="absolute right-[22%] top-[10%] h-6 w-6"
        >
          <circle r="11" className="fill-emerald-500" />
          <path d="M1.5 -6.5 -4 1h4l-1.5 5.5 5.5-7.5h-4z" fill="#06080C" />
        </svg>
      )}
    </div>
  );
}
