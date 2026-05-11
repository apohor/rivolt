import { Link } from "react-router-dom";
import { Card } from "../components/ui";

export default function NotFoundPage() {
  return (
    <div className="flex min-h-[60vh] items-center justify-center">
      <Card>
        <div className="max-w-md space-y-4 text-center">
          <div className="text-6xl font-bold text-neutral-700">404</div>
          <div>
            <h1 className="text-xl font-semibold text-neutral-100">Page not found</h1>
            <p className="mt-1 text-sm text-neutral-400">
              The page you're looking for doesn't exist or has moved.
            </p>
          </div>
          <div className="flex items-center justify-center gap-3 pt-2">
            <Link
              to="/"
              className="rounded-md bg-emerald-600 px-4 py-2 text-sm font-semibold text-white hover:bg-emerald-500"
            >
              Back to Overview
            </Link>
            <Link
              to="/drives"
              className="rounded-md border border-neutral-700 px-4 py-2 text-sm text-neutral-300 hover:border-neutral-500 hover:text-neutral-100"
            >
              Drives
            </Link>
          </div>
        </div>
      </Card>
    </div>
  );
}
