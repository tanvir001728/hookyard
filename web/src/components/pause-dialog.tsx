import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Pause, Play } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { ErrorState, Spinner } from "@/components/ui/feedback";
import { Input, Label, Select } from "@/components/ui/input";
import { api, type Upstream } from "@/lib/api";

export type PauseAction = "pause" | "resume";

const durations = [
  { value: "", label: "Until resumed" },
  { value: "15m", label: "15 minutes" },
  { value: "1h", label: "1 hour" },
  { value: "4h", label: "4 hours" },
  { value: "24h", label: "24 hours" },
] as const;

/** Pauses or resumes deliveries to an upstream, with a reason for the audit log. */
export function PauseDialog({ upstream, action, onClose }: { upstream: Upstream; action: PauseAction | null; onClose: () => void }) {
  const title = action === "pause" ? `Pause ${upstream.name}?` : `Resume ${upstream.name}?`;
  return (
    <Dialog open={action !== null} onClose={onClose} title={action ? title : ""}>
      {action && <PauseForm key={action} upstream={upstream} action={action} onClose={onClose} />}
    </Dialog>
  );
}

function PauseForm({ upstream, action, onClose }: { upstream: Upstream; action: PauseAction; onClose: () => void }) {
  const qc = useQueryClient();
  const [reason, setReason] = useState("");
  const [duration, setDuration] = useState("");
  const mutation = useMutation({
    mutationFn: () => {
      const body: Record<string, string> = { reason: reason.trim() };
      if (action === "pause" && duration) body.duration = duration;
      return api<Upstream>(`/v1/upstreams/${encodeURIComponent(upstream.name)}/${action}`, { method: "POST", body });
    },
    onSuccess: (data) => {
      qc.setQueryData(["upstream", upstream.name], data);
      void qc.invalidateQueries({ queryKey: ["upstreams"] });
      void qc.invalidateQueries({ queryKey: ["upstream", upstream.name, "events"] });
      onClose();
    },
  });

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        mutation.mutate();
      }}
    >
      <p className="text-sm text-muted-foreground">
        {action === "pause"
          ? "Requests to this upstream wait in the queue: they aren't sent, don't use up attempts, and the paused time doesn't count against their max age. The pause applies to every Hookyard instance and survives restarts."
          : "Waiting requests are delivered again, within the upstream's rate limit and concurrency cap."}
      </p>
      <div className="mt-4 flex flex-col gap-1.5">
        <Label htmlFor="pause-reason">Reason (for the audit log)</Label>
        <Input
          id="pause-reason"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder={action === "pause" ? "e.g. Vendor maintenance window" : "e.g. Maintenance finished"}
          required
        />
      </div>
      {action === "pause" && (
        <div className="mt-3 flex flex-col gap-1.5">
          <Label htmlFor="pause-duration">Resume automatically</Label>
          <Select id="pause-duration" value={duration} onChange={(e) => setDuration(e.target.value)}>
            {durations.map((d) => (
              <option key={d.value} value={d.value}>
                {d.label}
              </option>
            ))}
          </Select>
        </div>
      )}
      {mutation.isError && <ErrorState error={mutation.error} className="mt-3" />}
      <div className="mt-5 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
        <Button variant="outline" onClick={onClose} data-autofocus>
          Cancel
        </Button>
        <Button type="submit" variant={action === "pause" ? "destructive" : "default"} disabled={!reason.trim() || mutation.isPending}>
          {mutation.isPending ? <Spinner /> : action === "pause" ? <Pause /> : <Play />}
          {action === "pause" ? "Pause deliveries" : "Resume deliveries"}
        </Button>
      </div>
    </form>
  );
}
