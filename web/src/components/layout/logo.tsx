import { cn } from "@/lib/utils";

export function Logo({ className }: { className?: string }) {
  return (
    <span className={cn("flex items-center gap-2 font-semibold tracking-tight", className)}>
      <svg viewBox="0 0 32 32" className="size-6" aria-hidden>
        <rect width="32" height="32" rx="8" className="fill-primary" />
        <path d="M13 7v11a5 5 0 1 0 10 0v-2" fill="none" stroke="#fff" strokeWidth="3" strokeLinecap="round" />
        <path d="m19.5 14.5 3.5-3 3.5 3" fill="none" stroke="#fff" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
      Hookyard
    </span>
  );
}
