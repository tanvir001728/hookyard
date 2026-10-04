import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, XCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog } from "@/components/ui/dialog";
import { ErrorState, Spinner } from "@/components/ui/feedback";
import { Input, Label } from "@/components/ui/input";
import { api, type HookyardRequest } from "@/lib/api";

export type Resolution = "succeeded" | "dead";

const copy: Record<Resolution, { title: string; body: string; action: string }> = {
  succeeded: {
    title: "Mark as delivered?",
    body: "Confirm with the upstream that it processed this request (for example in its dashboard). The request will be marked succeeded and won't be sent again.",
    action: "Mark as delivered",
  },
  dead: {
    title: "Mark as failed?",
    body: "Confirm with the upstream that it did not process this request. It moves to the dead-letter queue, where you can replay it. Replaying something that did go through would duplicate it.",
    action: "Mark as failed",
  },
};

/** Settles a request whose outcome is unknown, with a reason for the audit log. */
export function ResolveDialog({ request, resolution, onClose }: { request: HookyardRequest; resolution: Resolution | null; onClose: () => void }) {
  return (
    <Dialog open={resolution !== null} onClose={onClose} title={resolution ? copy[resolution].title : ""}>
      {resolution && <ResolveForm key={resolution} request={request} resolution={resolution} onClose={onClose} />}
    </Dialog>
  );
}

function ResolveForm({ request, resolution, onClose }: { request: HookyardRequest; resolution: Resolution; onClose: () => void }) {
  const qc = useQueryClient();
  const [reason, setReason] = useState("");
  const resolve = useMutation({
    mutationFn: () => api<HookyardRequest>(`/v1/requests/${request.id}/resolve`, { method: "POST", body: { outcome: resolution, reason: reason.trim() } }),
    onSuccess: (data) => {
      qc.setQueryData(["request", request.id], data);
      void qc.invalidateQueries({ queryKey: ["requests"] });
      void qc.invalidateQueries({ queryKey: ["dlq"] });
      onClose();
    },
  });
  const { body, action } = copy[resolution];

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        resolve.mutate();
      }}
    >
      <p className="text-sm text-muted-foreground">{body}</p>
      <div className="mt-4 flex flex-col gap-1.5">
        <Label htmlFor="resolve-reason">Reason (for the audit log)</Label>
        <Input id="resolve-reason" value={reason} onChange={(e) => setReason(e.target.value)} placeholder="e.g. Vendor dashboard shows the order" required />
      </div>
      {resolve.isError && <ErrorState error={resolve.error} className="mt-3" />}
      <div className="mt-5 flex justify-end gap-2">
        <Button variant="outline" onClick={onClose} data-autofocus>
          Cancel
        </Button>
        <Button type="submit" variant={resolution === "dead" ? "destructive" : "default"} disabled={!reason.trim() || resolve.isPending}>
          {resolve.isPending ? <Spinner /> : resolution === "succeeded" ? <CheckCircle2 /> : <XCircle />}
          {action}
        </Button>
      </div>
    </form>
  );
}
