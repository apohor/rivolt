import { drivingHoursLabel, drivingHoursOptions, useDrivingHours } from "./drivingBudget";

// "Driving per day": the one limit the planner splits days by and looks for a stay at (kept on this device). A native select: it zooms, reflows and
// reads aloud like any other, and it needs no stepper to be tapped at a small size.
export function DrivingPerDay() {
  const [hours, setHours] = useDrivingHours();
  return (
    <div data-driving-per-day className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm">
      <label htmlFor="driving-per-day" className="text-neutral-300">Driving per day</label>
      <select
        id="driving-per-day"
        value={hours}
        onChange={(e) => setHours(Number(e.target.value))}
        className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-sm text-neutral-100"
      >
        {drivingHoursOptions().map((h) => (
          <option key={h} value={h}>{drivingHoursLabel(h)}</option>
        ))}
      </select>
      <span className="text-xs text-neutral-500">A longer drive is offered over more than one day, with a night near where this runs out.</span>
    </div>
  );
}
