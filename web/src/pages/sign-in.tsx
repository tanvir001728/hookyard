import { useState, type FormEvent } from "react";
import { KeyRound } from "lucide-react";
import { Logo } from "@/components/layout/logo";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Spinner } from "@/components/ui/feedback";
import { Input, Label } from "@/components/ui/input";
import { ApiError } from "@/lib/api";
import { useSignIn } from "@/lib/session";

export function SignInPage() {
  const [token, setToken] = useState("");
  const signIn = useSignIn();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (token.trim()) signIn.mutate(token.trim());
  };

  const error =
    signIn.error instanceof ApiError && signIn.error.status === 401
      ? "That token isn't valid. Check HOOKYARD_API_TOKENS on the server."
      : signIn.error?.message;

  return (
    <div className="flex min-h-dvh flex-col items-center justify-center gap-8 p-4">
      <Logo className="text-lg" />
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle className="text-base">Sign in</CardTitle>
          <CardDescription>
            Use an API token from <code className="font-mono text-xs">HOOKYARD_API_TOKENS</code> on the server. For an entry
            like <code className="font-mono text-xs">dev:abc123…</code>, the token is the part after the colon.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={onSubmit} className="flex flex-col gap-4">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="token">API token</Label>
              <Input
                id="token"
                type="password"
                autoComplete="current-password"
                autoFocus
                value={token}
                onChange={(e) => setToken(e.target.value)}
                aria-invalid={Boolean(error)}
                aria-describedby={error ? "token-error" : undefined}
              />
              {error && (
                <p id="token-error" role="alert" className="text-sm text-red-600 dark:text-red-400">
                  {error}
                </p>
              )}
            </div>
            <Button type="submit" disabled={signIn.isPending || !token.trim()}>
              {signIn.isPending ? <Spinner /> : <KeyRound />}
              Sign in
            </Button>
          </form>
        </CardContent>
      </Card>
      <p className="max-w-sm text-center text-xs text-muted-foreground">
        The token is exchanged for a session cookie and is not stored in the browser.
      </p>
    </div>
  );
}
