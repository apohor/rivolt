import { Card, PageHeader } from "../components/ui";

export default function NotFoundPage() {
  return (
    <div className="space-y-6">
      <PageHeader title="Not found" subtitle="This page does not exist." />
      <Card>
        <a href="/" className="text-emerald-300 hover:underline">
          Go home →
        </a>
      </Card>
    </div>
  );
}
