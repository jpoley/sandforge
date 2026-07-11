import { useEffect, useState } from "react";
import { Task, listTasks, createTask, setDone } from "./api";

export default function App() {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [title, setTitle] = useState("");
  const [error, setError] = useState<string | null>(null);

  async function refresh() {
    try {
      setTasks(await listTasks());
    } catch (e) {
      setError(String(e));
    }
  }

  useEffect(() => {
    void refresh();
  }, []);

  async function onAdd(e: React.FormEvent) {
    e.preventDefault();
    const t = title.trim();
    if (!t) return;
    try {
      const created = await createTask(t);
      setTasks((prev) => [...prev, created]);
      setTitle("");
    } catch (e) {
      setError(String(e));
    }
  }

  async function onToggle(task: Task) {
    try {
      const updated = await setDone(task.id, !task.done);
      setTasks((prev) => prev.map((x) => (x.id === updated.id ? updated : x)));
    } catch (e) {
      setError(String(e));
    }
  }

  return (
    <main style={{ fontFamily: "sans-serif", maxWidth: 480, margin: "2rem auto" }}>
      <h1>Tasks</h1>

      <form onSubmit={onAdd} style={{ display: "flex", gap: 8 }}>
        <input
          data-testid="new-task-input"
          aria-label="new task title"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="What needs doing?"
          style={{ flex: 1 }}
        />
        <button data-testid="add-task" type="submit">
          Add
        </button>
      </form>

      {error && (
        <p data-testid="error" style={{ color: "crimson" }}>
          {error}
        </p>
      )}

      <ul data-testid="task-list" style={{ listStyle: "none", padding: 0 }}>
        {tasks.map((task) => (
          <li
            key={task.id}
            data-testid="task-row"
            style={{ display: "flex", gap: 8, alignItems: "center", padding: "4px 0" }}
          >
            <input
              type="checkbox"
              data-testid="task-toggle"
              checked={task.done}
              onChange={() => onToggle(task)}
            />
            <span style={{ textDecoration: task.done ? "line-through" : "none" }}>
              {task.title}
            </span>
          </li>
        ))}
      </ul>
    </main>
  );
}
