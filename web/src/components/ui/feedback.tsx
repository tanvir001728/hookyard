import type { ReactNode } from "react";
import { AlertTriangle, Loader2 } from "lucide-react";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/utils";

export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("animate-pulse rounded-md bg-muted", className)} />;
}

export function Spinner({ className }: { className?: string }) {
  return <Loader2 className={cn("size-4 animate-spin", className)} aria-hidden />;
}

export function EmptyState({ icon, title, children }: { icon?: ReactNode; title: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 px-6 py-14 text-center">
      {icon && <div className="mb-1 text-muted-foreground [&_svg]:size-8">{icon}</div>}
      <p className="font-medium">{title}</p>
      {children && <div className="max-w-md text-sm text-muted-foreground">{children}</div>}
    </div>
  );
}

export function ErrorState({ error, className }: { error: unknown; className?: string }) {
  const message = error instanceof ApiError || error instanceof Error ? error.message : "Something went wrong.";
  return (
    <div role="alert" className={cn("flex items-start gap-3 rounded-lg border border-red-500/30 bg-red-500/5 p-4 text-sm", className)}>
      <AlertTriangle className="mt-0.5 size-4 shrink-0 text-red-600 dark:text-red-400" aria-hidden />
      <div>
        <p className="font-medium text-red-700 dark:text-red-400">Couldn't load this data</p>
        <p className="text-muted-foreground">{message}</p>
      </div>
    </div>
  );
}
