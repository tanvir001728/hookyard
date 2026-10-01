import { useState, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { LayoutDashboard, List, LogOut, Menu, Moon, Sun, X, type LucideIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useSession, useSignOut } from "@/lib/session";
import { useTheme } from "@/lib/theme";
import { cn } from "@/lib/utils";
import { Logo } from "./logo";

interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  exact?: boolean;
}

export const navItems: NavItem[] = [
  { to: "/", label: "Overview", icon: LayoutDashboard, exact: true },
  { to: "/requests", label: "Requests", icon: List },
];


function Nav({ onNavigate }: { onNavigate?: () => void }) {
  return (
    <nav className="flex flex-col gap-0.5" aria-label="Main">
      {navItems.map(({ to, label, icon: Icon, exact }) => (
        <Link
          key={to}
          to={to}
          onClick={onNavigate}
          activeOptions={{ exact: exact ?? false, includeSearch: false }}
          className="flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-sm text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          activeProps={{ className: "bg-muted font-medium !text-foreground" }}
        >
          <Icon className="size-4" aria-hidden />
          {label}
        </Link>
      ))}
    </nav>
  );
}

function Footer() {
  const [theme, setTheme, resolved] = useTheme();
  const session = useSession();
  const signOut = useSignOut();
  // One click always flips what you see; "system" stays the default until then.
  const next = resolved === "dark" ? "light" : "dark";
  const ThemeIcon = resolved === "dark" ? Sun : Moon;

  return (
    <div className="flex items-center justify-between gap-2 border-t pt-3">
      <div className="min-w-0 text-xs">
        <p className="text-muted-foreground">Signed in as</p>
        <p className="truncate font-medium">{session.data?.actor}</p>
      </div>
      <div className="flex gap-1">
        <Button
          variant="ghost"
          size="icon"
          onClick={() => setTheme(next)}
          title={`Switch to ${next} mode${theme === "system" ? " (currently following the system)" : ""}`}
          aria-label={`Switch to ${next} mode`}
        >
          <ThemeIcon />
        </Button>
        <Button variant="ghost" size="icon" onClick={() => signOut.mutate()} title="Sign out" aria-label="Sign out">
          <LogOut />
        </Button>
      </div>
    </div>
  );
}

export function AppShell({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false);

  return (
    <div className="min-h-dvh lg:grid lg:grid-cols-[15rem_1fr]">
      <aside className="hidden border-r bg-card lg:sticky lg:top-0 lg:flex lg:h-dvh lg:flex-col lg:gap-6 lg:p-4">
        <Logo className="px-2.5 pt-1" />
        <div className="flex-1">
          <Nav />
        </div>
        <Footer />
      </aside>

      <header className="sticky top-0 z-20 flex h-14 items-center justify-between border-b bg-card/80 px-4 backdrop-blur lg:hidden">
        <Logo />
        <Button variant="ghost" size="icon" onClick={() => setOpen(true)} aria-label="Open menu">
          <Menu />
        </Button>
      </header>

      {open && (
        <div className="fixed inset-0 z-30 lg:hidden" role="dialog" aria-modal="true" aria-label="Menu">
          <div className="absolute inset-0 bg-black/40" onClick={() => setOpen(false)} />
          <div className="absolute inset-y-0 left-0 flex w-64 flex-col gap-6 border-r bg-card p-4">
            <div className="flex items-center justify-between">
              <Logo className="px-2.5" />
              <Button variant="ghost" size="icon" onClick={() => setOpen(false)} aria-label="Close menu">
                <X />
              </Button>
            </div>
            <div className="flex-1">
              <Nav onNavigate={() => setOpen(false)} />
            </div>
            <Footer />
          </div>
        </div>
      )}

      <main className="min-w-0">
        <div className="mx-auto w-full max-w-7xl px-4 py-6 sm:px-6 lg:px-8 lg:py-8">{children}</div>
      </main>
    </div>
  );
}

export function PageHeader({ title, description, actions, className }: { title: string; description?: ReactNode; actions?: ReactNode; className?: string }) {
  return (
    <div className={cn("mb-6 flex flex-wrap items-end justify-between gap-4", className)}>
      <div>
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}
