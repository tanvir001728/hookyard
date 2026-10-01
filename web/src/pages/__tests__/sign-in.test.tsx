import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { SignInPage } from "../sign-in";

afterEach(() => vi.restoreAllMocks());

it("shows an actionable error for an invalid token", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify({ error: { code: "unauthorized", message: "invalid API token" } }), { status: 401 }),
  );
  render(
    <QueryClientProvider client={new QueryClient()}>
      <SignInPage />
    </QueryClientProvider>,
  );

  const submit = screen.getByRole("button", { name: "Sign in" });
  expect(submit).toBeDisabled();
  await userEvent.type(screen.getByLabelText("API token"), "wrong-token");
  await userEvent.click(submit);

  expect(await screen.findByRole("alert")).toHaveTextContent("Check HOOKYARD_API_TOKENS");
  expect(screen.getByLabelText("API token")).toHaveAttribute("aria-invalid", "true");
});
