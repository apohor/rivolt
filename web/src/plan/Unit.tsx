// Unit: "mi", "kW", "%" next to a value: caption size, neutral-400, regular weight
// everywhere in the planner (see the type scale in styles/next-plan.css).
export function Unit({ children }: { children: React.ReactNode }) {
  return <span className="pt-unit">{children}</span>;
}
