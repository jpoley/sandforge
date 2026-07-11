import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import App from "./App";
import type { Task } from "./api";

// Hermetic test: mock fetch so no backend/DB is needed (SC-8).
function mockFetchWith(initial: Task[]) {
  const store = [...initial];
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";

    if (url.endsWith("/api/tasks") && method === "GET") {
      return new Response(JSON.stringify(store), { status: 200 });
    }
    if (url.endsWith("/api/tasks") && method === "POST") {
      const body = JSON.parse(String(init?.body));
      const task: Task = {
        id: `id-${store.length + 1}`,
        title: body.title,
        done: false,
        createdAt: new Date().toISOString(),
      };
      store.push(task);
      return new Response(JSON.stringify(task), { status: 201 });
    }
    throw new Error(`unexpected fetch ${method} ${url}`);
  });
}

describe("App", () => {
  beforeEach(() => {
    vi.stubGlobal("fetch", mockFetchWith([
      { id: "id-1", title: "seeded task", done: false, createdAt: "2026-01-01T00:00:00Z" },
    ]));
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders tasks fetched from the API", async () => {
    render(<App />);
    expect(await screen.findByText("seeded task")).toBeInTheDocument();
  });

  it("adds a task via the form and shows it in the list", async () => {
    render(<App />);
    await screen.findByText("seeded task");

    fireEvent.change(screen.getByTestId("new-task-input"), {
      target: { value: "write tests" },
    });
    fireEvent.click(screen.getByTestId("add-task"));

    await waitFor(() => {
      expect(screen.getByText("write tests")).toBeInTheDocument();
    });
    expect(screen.getAllByTestId("task-row")).toHaveLength(2);
  });
});
