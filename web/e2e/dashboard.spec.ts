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
