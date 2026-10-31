import { Link } from "react-router-dom";
import { ProLimitNotice } from "./ProLimitNotice";

// What a locked vehicle's page shows (a free account's non-primary vehicle): why, that it keeps recording and alerting and stays in the
// export, and where to pick the vehicle the free plan shows. Pro shows them all. Nothing is deleted.
export function LockedVehicleNotice({ name }: { name?: string }) {
  return (
    <div data-locked-vehicle className="space-y-3">
      <ProLimitNotice limit={{ code: "pro_required" }} feature="vehicle" privacy />
      <p className="text-xs text-neutral-500">
        {name ? `${name} is ` : "This vehicle is "}recording as usual, and its alerts still reach you.{" "}
        <Link to="/settings#primary-vehicle" className="underline underline-offset-2">Choose the vehicle the free plan shows</Link>.
      </p>
    </div>
  );
}
