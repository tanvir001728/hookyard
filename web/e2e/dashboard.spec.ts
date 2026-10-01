import { expect, test, type Page } from "@playwright/test";

const token = process.env.HOOKYARD_E2E_TOKEN ?? "hookyard-dev-token";

async function signIn(page: Page, value = token) {
  await page.goto("/");
  await page.getByLabel("API token").fill(value);
  await page.getByRole("button", { name: "Sign in" }).click();
}

test("rejects an invalid token with a helpful message", async ({ page }) => {
  await signIn(page, "not-a-real-token-000");
  await expect(page.getByRole("alert")).toContainText("HOOKYARD_API_TOKENS");
});

test("signs in, shows the overview and signs out", async ({ page, isMobile }) => {
  await signIn(page);
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();

  if (isMobile) await page.getByRole("button", { name: "Open menu" }).click();
  await expect(page.getByText("Signed in as").last()).toBeVisible();
  await page.getByRole("button", { name: "Sign out" }).last().click();
  await expect(page.getByLabel("API token")).toBeVisible();
});

test("the theme toggle flips light and dark", async ({ page, isMobile }) => {
  test.skip(isMobile, "covered on desktop");
  await signIn(page);
  const html = page.locator("html");
  const wasDark = await html.evaluate((el) => el.classList.contains("dark"));

  await page.getByRole("button", { name: /Switch to (dark|light) mode/ }).click();
  await expect(html).toHaveClass(wasDark ? /^(?!.*\bdark\b)/ : /\bdark\b/);
  // The choice survives a reload.
  await page.reload();
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  expect(await html.evaluate((el) => el.classList.contains("dark"))).toBe(!wasDark);
});

test("unknown pages show a not-found message", async ({ page }) => {
  await signIn(page);
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  await page.goto("/definitely/not/here");
  await expect(page.getByText("Page not found")).toBeVisible();
});

test("the overview shows health per upstream and a deliveries chart", async ({ page }) => {
  await signIn(page);
  await expect(page.getByRole("heading", { name: "Deliveries" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Upstreams" })).toBeVisible();
  // Every health badge pairs an icon with a text label.
  await expect(page.getByText(/^(Healthy|Degraded|Failing|No traffic)$/).first()).toBeVisible();
});

test("the time range is kept in the URL and the chart has a table view", async ({ page, isMobile }) => {
  await signIn(page);
  await page.getByRole("radio", { name: "24h" }).click();
  await expect(page).toHaveURL(/range=24h/);
  await expect(page.getByText("last 24 hours")).toBeVisible();

  await page.getByRole("button", { name: "View as table" }).click();
  await expect(page.getByRole("columnheader", { name: "Failed attempts" })).toBeVisible();

  await page.reload();
  await expect(page.getByRole("radio", { name: "24h" })).toHaveAttribute("aria-checked", "true");
  if (!isMobile) await expect(page.getByRole("link", { name: "Overview" })).toHaveClass(/font-medium/);
});

test.describe("requests", () => {
  test.beforeEach(async ({ request }) => {
    // A request with a secret header, so redaction can be checked.
    const res = await request.post("/v1/requests", {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        upstream: "courier-x",
        method: "POST",
        path: "/e2e",
        headers: { Authorization: "Bearer e2e-secret-value", "X-Trace": "t-1" },
        body: { e2e: true },
        tags: { suite: "e2e" },
      },
    });
    expect(res.status()).toBe(202);
  });

  test("filters are kept in the URL and a row opens the request", async ({ page }) => {
    await signIn(page);
    await page.goto("/requests?tag=suite:e2e");
    await expect(page.getByRole("heading", { name: "Requests" })).toBeVisible();
    await page.getByRole("button", { name: "Succeeded", exact: true }).click();
    await expect(page).toHaveURL(/status=succeeded/);
    await page.getByRole("button", { name: "Succeeded", exact: true }).click();
    await expect(page).not.toHaveURL(/status=/);

    await page.getByRole("link", { name: /POST \/e2e/ }).first().click();
    await expect(page.getByRole("heading", { name: "Attempts" })).toBeVisible();
    await expect(page.getByText("suite: e2e")).toBeVisible();
  });

  test("the detail page never shows secret header values", async ({ page }) => {
    await signIn(page);
    await page.goto("/requests?tag=suite:e2e");
    await page.getByRole("link", { name: /POST \/e2e/ }).first().click();
    await expect(page.getByRole("heading", { name: "Request", exact: true })).toBeVisible();
    await expect(page.getByText("t-1", { exact: true })).toBeVisible();
    // The request's own headers and the copy-as-code snippet are redacted.
    await expect(page.locator("dl", { hasText: "X-Trace" }).getByText("e2e-secret-value")).toHaveCount(0);
    await expect(page.locator("pre", { hasText: "curl" })).not.toContainText("e2e-secret-value");
  });

  test("searching for a request ID opens it", async ({ page, request }) => {
    const list = await (await request.get("/v1/requests?limit=1&tag=suite:e2e", { headers: { Authorization: `Bearer ${token}` } })).json();
    const id = list.data[0].id as string;
    await signIn(page);
    await page.goto("/requests");
    await page.getByLabel("Request ID or dedupe key").fill(id);
    await page.getByRole("button", { name: "Apply" }).click();
    await expect(page).toHaveURL(new RegExp(`/requests/${id}$`));
  });

  test("unknown request IDs show a friendly message", async ({ page }) => {
    await signIn(page);
    await page.goto("/requests/req_01ZZZZZZZZZZZZZZZZZZZZZZZZ");
    await expect(page.getByText("Request not found")).toBeVisible();
  });
});
