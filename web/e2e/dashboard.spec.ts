import { expect, test, type Page } from "@playwright/test";

const token = process.env.HOOKYARD_E2E_TOKEN ?? "hookyard-dev-token";
// flakyvendor also plays the app that receives completion callbacks.
const vendor = process.env.HOOKYARD_E2E_VENDOR ?? "http://127.0.0.1:9090";

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

test.describe("dead letters", () => {
  // Each project gets its own failure code, so its DLQ group isn't changed by
  // the other project's replays running at the same time.
  const statusFor = (project: string) => (project === "mobile" ? 410 : 422);

  test.beforeEach(async ({ request }, testInfo) => {
    // 4xx is a permanent failure, so these go straight to the DLQ.
    const headers = { Authorization: `Bearer ${token}` };
    const ids: string[] = [];
    for (let i = 0; i < 2; i++) {
      const res = await request.post("/v1/requests", {
        headers,
        data: { upstream: "payments-y", method: "POST", path: `/e2e-dead?status=${statusFor(testInfo.project.name)}` },
      });
      expect(res.status()).toBe(202);
      ids.push((await res.json()).id);
    }
    // Wait until they are actually dead, so the tests don't depend on timing.
    for (const id of ids) {
      await expect
        .poll(async () => (await (await request.get(`/v1/requests/${id}`, { headers })).json()).status, { timeout: 10_000 })
        .toBe("dead");
    }
  });

  test("replaying a group confirms the exact count first", async ({ page, request }, testInfo) => {
    const status = statusFor(testInfo.project.name);
    await signIn(page);
    await page.goto("/dlq");
    const row = page.getByRole("row", { name: new RegExp(`payments-y.*HTTP ${status}`) });
    await expect(row).toBeVisible();
    const count = Number(await row.getByRole("cell").nth(2).innerText());
    expect(count).toBeGreaterThanOrEqual(2);

    await row.getByRole("button", { name: /Replay/ }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText(`${count} dead requests will be delivered again`);

    // Escape cancels without replaying.
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);

    await row.getByRole("button", { name: /Replay/ }).click();
    await dialog.getByRole("button", { name: `Replay ${count}` }).click();
    await expect(dialog.getByRole("status")).toContainText(`Queued ${count}`);

    const summary = await (await request.get("/v1/dlq?upstream=payments-y", { headers: { Authorization: `Bearer ${token}` } })).json();
    const group = summary.groups.find((g: { status_code: number }) => g.status_code === status);
    // They are replayed and, since 422 is permanent, they fail again; the
    // group must not have grown beyond the replayed requests.
    expect(group?.count ?? 0).toBeLessThanOrEqual(count);
  });

  test("the navigation shows the dead-letter count", async ({ page, isMobile }) => {
    await signIn(page);
    if (isMobile) await page.getByRole("button", { name: "Open menu" }).click();
    await expect(page.getByLabel(/\d+ dead letters/).last()).toBeVisible();
  });
});

test("no page scrolls sideways on a phone", async ({ page, isMobile }) => {
  test.skip(!isMobile, "phone layout only");
  await signIn(page);
  for (const path of ["/", "/requests", "/dlq", "/upstreams/courier-x"]) {
    await page.goto(path);
    await page.waitForLoadState("networkidle");
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow, `${path} overflows horizontally`).toBeLessThanOrEqual(0);
  }
});

test("an unknown request explains itself and can be resolved", async ({ page, request }) => {
  // A POST to an endpoint that hangs past its timeout: sent, but no response.
  const res = await request.post("/v1/requests", {
    headers: { Authorization: `Bearer ${token}` },
    data: { upstream: "hanging", method: "POST", path: `/charge?hang=1&key=${Date.now()}`, timeout: "300ms", body: { amount: 5 } },
  });
  const { id } = await res.json();

  await signIn(page);
  await page.goto(`/requests/${id}`);
  await expect(page.getByText("Did this go through? Hookyard can't tell.")).toBeVisible({ timeout: 10_000 });
  await expect(page.getByText("No response", { exact: true })).toBeVisible(); // the attempt badge

  await page.getByRole("button", { name: "Mark as delivered" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("button", { name: "Mark as delivered" })).toBeDisabled(); // a reason is required
  await dialog.getByLabel("Reason (for the audit log)").fill("Vendor dashboard shows the charge");
  await dialog.getByRole("button", { name: "Mark as delivered" }).click();

  await expect(dialog).toHaveCount(0);
  await expect(page.getByText("Succeeded").first()).toBeVisible();
  await expect(page.getByText("Did this go through?")).toHaveCount(0);
});

test("the request page shows its completion callback", async ({ page, request }) => {
  const res = await request.post("/v1/requests", {
    headers: { Authorization: `Bearer ${token}` },
    data: {
      upstream: "courier-x",
      method: "POST",
      path: `/shipments?key=${Date.now()}`,
      body: { order: 1 },
      callback_url: `${vendor}/hooks/hookyard`,
      on_result: "order.shipment",
    },
  });
  expect(res.status()).toBe(202);
  const { id } = await res.json();

  await signIn(page);
  await page.goto(`/requests/${id}`);
  const card = page.getByRole("region", { name: "Callbacks" });
  await expect(card.getByText("Delivered", { exact: true })).toBeVisible({ timeout: 10_000 });
  await expect(card.getByText("request.succeeded")).toBeVisible();
  await expect(card.getByText(`${vendor}/hooks/hookyard`)).toBeVisible();
  await expect(page.getByText("order.shipment", { exact: true })).toBeVisible(); // On result
});

test.describe("upstream page", () => {
  test("opens from the overview and shows state, charts and configuration", async ({ page }) => {
    await signIn(page);
    const card = page.getByRole("heading", { name: "courier-x", exact: true }).locator("xpath=ancestor::div[contains(@class,'rounded-xl')][1]");
    await card.getByRole("link", { name: "Details" }).click();
    await expect(page).toHaveURL(/\/upstreams\/courier-x$/);
    await expect(page.getByRole("heading", { name: "courier-x", level: 1 })).toBeVisible();
    await expect(page.getByText("Delivering").first()).toBeVisible();

    // Both charts have a legend and a table view.
    await expect(page.getByRole("heading", { name: "Latency" })).toBeVisible();
    const legends = page.getByRole("list", { name: "Legend" });
    await expect(legends).toHaveCount(2);
    await expect(legends.nth(1)).toContainText("p99");

    // Configuration, including the classification rule, without header values.
    await expect(page.getByText("50/s, burst 50")).toBeVisible();
    await expect(page.getByText("fake success")).toBeVisible();
    await page.getByRole("button", { name: "View as YAML" }).click();
    await expect(page.getByText("rate_limit: 50/s")).toBeVisible();
    await expect(page.getByText("max_concurrency: 8")).toBeVisible();

    await page.getByRole("radio", { name: "6h" }).click();
    await expect(page).toHaveURL(/range=6h/);
    await expect(page.getByRole("heading", { name: "Last 6 hours" })).toBeVisible();
  });

  test("pauses and resumes deliveries with a reason", async ({ page }, testInfo) => {
    const name = `pausable-${testInfo.project.name}`;
    await signIn(page);
    await page.goto(`/upstreams/${name}`);
    await expect(page.getByRole("heading", { name, level: 1 })).toBeVisible();
    // Leftover state from an interrupted run.
    if (await page.getByRole("button", { name: "Resume" }).isVisible()) {
      await page.getByRole("button", { name: "Resume" }).click();
      await page.getByRole("dialog").getByLabel("Reason (for the audit log)").fill("cleanup");
      await page.getByRole("dialog").getByRole("button", { name: "Resume deliveries" }).click();
    }

    await page.getByRole("button", { name: "Pause", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("button", { name: "Pause deliveries" })).toBeDisabled(); // a reason is required
    await dialog.getByLabel("Reason (for the audit log)").fill("Vendor maintenance window");
    await dialog.getByLabel("Resume automatically").selectOption("1h");
    await dialog.getByRole("button", { name: "Pause deliveries" }).click();
    await expect(dialog).toHaveCount(0);

    await expect(page.getByText(/Paused by dev/)).toBeVisible();
    await expect(page.getByText(/when deliveries resume on their own/)).toBeVisible();
    const history = page.getByRole("list", { name: "Upstream history" });
    await expect(history.getByText("Vendor maintenance window").first()).toBeVisible();

    await page.getByRole("button", { name: "Resume" }).click();
    await dialog.getByLabel("Reason (for the audit log)").fill("Maintenance finished");
    await dialog.getByRole("button", { name: "Resume deliveries" }).click();
    await expect(page.getByText("Delivering").first()).toBeVisible();
    await expect(history.getByText("Resumed").first()).toBeVisible();
    await expect(page.getByRole("button", { name: "Pause", exact: true })).toBeVisible();
  });

  test("an unknown upstream shows a friendly message", async ({ page }) => {
    await signIn(page);
    await page.goto("/upstreams/nope");
    await expect(page.getByText("Upstream not found")).toBeVisible();
  });
});
