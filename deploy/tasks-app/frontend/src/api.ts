export interface Task {
  id: string;
  title: string;
  done: boolean;
  createdAt: string;
}

const BASE = "/api";

export async function listTasks(): Promise<Task[]> {
  const res = await fetch(`${BASE}/tasks`);
  if (!res.ok) throw new Error(`list failed: ${res.status}`);
  return res.json();
}

export async function createTask(title: string): Promise<Task> {
  const res = await fetch(`${BASE}/tasks`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ title }),
  });
  if (!res.ok) throw new Error(`create failed: ${res.status}`);
  return res.json();
}

export async function setDone(id: string, done: boolean): Promise<Task> {
  const res = await fetch(`${BASE}/tasks/${id}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ done }),
  });
  if (!res.ok) throw new Error(`patch failed: ${res.status}`);
  return res.json();
}
