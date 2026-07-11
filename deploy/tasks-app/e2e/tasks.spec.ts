import { test, expect } from "@playwright/test";

// SC-6: Through the LB, / returns 200 and the task list region renders,
// populated from /api/tasks. We seed a task via the API first so the list
// is non-empty and assert it shows up in the rendered list.
test("SC-6: page loads and renders the task list from the API", async ({
  page,
  request,
  baseURL,
}) => {
  const title = `sc6-seed-${Date.now()}`;
  const create = await request.post(`${baseURL}/api/tasks`, {
    data: { title },
  });
  expect(create.status()).toBe(201);

  const resp = await page.goto("/");
  expect(resp?.status()).toBe(200);

  // The list region is present...
  await expect(page.getByTestId("task-list")).toBeVisible();
  // ...and is populated from /api/tasks (our seeded task is visible).
  await expect(
    page.getByTestId("task-row").filter({ hasText: title }),
  ).toBeVisible();
});

// SC-7: Creating a task in the UI makes it appear in the list without a
// reload error.
test("SC-7: creating a task in the UI makes it appear", async ({ page }) => {
  await page.goto("/");

  const title = `sc7-ui-${Date.now()}`;
  await page.getByTestId("new-task-input").fill(title);
  await page.getByTestId("add-task").click();

  await expect(
    page.getByTestId("task-row").filter({ hasText: title }),
  ).toBeVisible();

  // No error banner appeared.
  await expect(page.getByTestId("error")).toHaveCount(0);
});
