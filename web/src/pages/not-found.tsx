import { Link } from "@tanstack/react-router";
import { Compass } from "lucide-react";
import { EmptyState } from "@/components/ui/feedback";

export function NotFoundPage() {
  return (
    <EmptyState icon={<Compass />} title="Page not found">
      <Link to="/" className="text-primary hover:underline">
        Go to the overview
      </Link>
    </EmptyState>
  );
}
