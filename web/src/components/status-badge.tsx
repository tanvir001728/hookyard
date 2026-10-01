import { Ban, CalendarClock, CheckCircle2, CircleDashed, HelpCircle, Loader, RotateCw, Skull, type LucideIcon } from "lucide-react";
import { Badge, type BadgeTone } from "@/components/ui/badge";
import type { RequestStatus } from "@/lib/api";

const styles: Record<RequestStatus, { tone: BadgeTone; icon: LucideIcon; label: string }> = {
  scheduled: { tone: "neutral", icon: CalendarClock, label: "Scheduled" },
  pending: { tone: "info", icon: CircleDashed, label: "Pending" },
  in_flight: { tone: "primary", icon: Loader, label: "In flight" },
  failed: { tone: "warning", icon: RotateCw, label: "Retrying" },
  succeeded: { tone: "success", icon: CheckCircle2, label: "Succeeded" },
  dead: { tone: "danger", icon: Skull, label: "Dead" },
  unknown: { tone: "warning", icon: HelpCircle, label: "Unknown" },
  canceled: { tone: "neutral", icon: Ban, label: "Canceled" },
};

export const statusLabel = (s: RequestStatus) => styles[s].label;

/** A request status with an icon and label, so it never relies on color alone. */
export function StatusBadge({ status }: { status: RequestStatus }) {
  const { tone, icon: Icon, label } = styles[status];
  return (
    <Badge tone={tone}>
      <Icon className="size-3.5" aria-hidden />
      {label}
    </Badge>
  );
}

export const allStatuses = Object.keys(styles) as RequestStatus[];
export const finalStatuses: RequestStatus[] = ["succeeded", "dead", "canceled", "unknown"];
