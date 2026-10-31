import { inDemo } from "../lib/demo";
import type { DemoLink } from "../lib/demoLinks";

// Plain links to the demo pages that show a Pro feature (new tab, nothing about the person in the address). Not inside the demo itself.
export function DemoLinks({ links, className = "" }: { links: DemoLink[]; className?: string }) {
  if (links.length === 0 || inDemo()) return null;
  return (
    <div data-demo-links className={`flex flex-wrap gap-x-4 gap-y-1 text-sm ${className}`}>
      {links.map((l) => (
        <a key={l.href} href={l.href} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2">{l.label}</a>
      ))}
    </div>
  );
}
