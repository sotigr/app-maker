// Compile-time check of the generated tracker client (spec 13.1 example).
import { createClient, type Task, TaskStatus } from "../gen/index";

export async function demo() {
  const client = createClient({ baseUrl: "/" });
  const p = await client.project.create({ title: "Launch" });
  const task: Task = await client.task.create({ title: "Write copy", estimate: "3.5" }, p.id);
  await client.task.update(task, { title: "New title" }); // sends If-Match: task.version
  const r = await client.task.actions.complete(task, { note: "Shipped" });
  const late: boolean | undefined = r.output?.late;
  const s: (typeof TaskStatus)[number] = "todo";
  // @ts-expect-error private fields do not exist on the client (spec 13)
  task.data.internalScore;
  // @ts-expect-error title must be a string
  await client.task.create({ title: 42 });
  return [late, s];
}
