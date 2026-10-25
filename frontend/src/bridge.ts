export const taskStates = ['created', 'preparing', 'running', 'waiting_approval', 'stopping', 'succeeded', 'failed', 'stopped', 'expired'] as const;
export type TaskState = typeof taskStates[number];
export interface Task {
  readonly id: string;
  readonly parentId: string | null;
  readonly title: string;
  readonly state: TaskState;
  readonly permissions: readonly string[];
}
export interface Snapshot {
  readonly version: 1;
  readonly connection: 'connected' | 'disconnected';
  readonly tasks: readonly Task[];
  readonly events: readonly { readonly id: string; readonly taskId: string; readonly summary: string }[];
  readonly artifacts: readonly { readonly id: string; readonly taskId: string; readonly name: string; readonly digest: string }[];
}
// Deliberately read-only. No native administration, navigation or execution surface.
export interface HostBridge {
  getSnapshot(): Promise<unknown>;
  subscribe(listener: (snapshot: unknown) => void): () => void;
}
export const disconnectedSnapshot: Snapshot = Object.freeze({ version: 1, connection: 'disconnected', tasks: Object.freeze([]), events: Object.freeze([]), artifacts: Object.freeze([]) });
export const disconnectedBridge: HostBridge = {
  getSnapshot: async () => disconnectedSnapshot,
  subscribe: () => () => {},
};
const record = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);
const text = (v: unknown): v is string => typeof v === 'string' && v.length <= 8192;
const list = (v: unknown): v is unknown[] => Array.isArray(v) && v.length <= 5000;
const invalid = (): never => { throw new Error('主机状态格式无效，已拒绝展示。请检查桌面连接版本。'); };
export function parseSnapshot(value: unknown): Snapshot {
  if (!record(value) || value.version !== 1 || (value.connection !== 'connected' && value.connection !== 'disconnected') || !list(value.tasks) || !list(value.events) || !list(value.artifacts)) return invalid();
  const ids = new Set<string>();
  const tasks: Task[] = value.tasks.map(t => {
    if (!record(t) || !text(t.id) || !t.id || ids.has(t.id) || !text(t.title) || !(t.parentId === null || text(t.parentId)) || !taskStates.includes(t.state as TaskState) || !list(t.permissions) || !t.permissions.every(text)) return invalid();
    ids.add(t.id);
    return Object.freeze({ id: t.id, parentId: t.parentId, title: t.title, state: t.state as TaskState, permissions: Object.freeze([...t.permissions]) });
  });
  const parents = new Map(tasks.map(t => [t.id, t.parentId]));
  for (const task of tasks) {
    const seen = new Set([task.id]);
    let parent = task.parentId;
    while (parent !== null) {
      if (!ids.has(parent) || seen.has(parent)) return invalid();
      seen.add(parent); parent = parents.get(parent) ?? null;
    }
  }
  const eventIds = new Set<string>();
  const events = value.events.map(e => {
    if (!record(e) || !text(e.id) || !e.id || eventIds.has(e.id) || !text(e.taskId) || !ids.has(e.taskId) || !text(e.summary)) return invalid();
    eventIds.add(e.id);
    return Object.freeze({ id: e.id, taskId: e.taskId, summary: e.summary });
  });
  const artifactIds = new Set<string>();
  const artifacts = value.artifacts.map(a => {
    if (!record(a) || !text(a.id) || !a.id || artifactIds.has(a.id) || !text(a.taskId) || !ids.has(a.taskId) || !text(a.name) || !text(a.digest) || !/^[a-f0-9]{64}$/i.test(a.digest)) return invalid();
    artifactIds.add(a.id);
    return Object.freeze({ id: a.id, taskId: a.taskId, name: a.name, digest: a.digest });
  });
  if (value.connection === 'disconnected' && (tasks.length || events.length || artifacts.length)) return invalid();
  return Object.freeze({ version: 1, connection: value.connection as Snapshot['connection'], tasks: Object.freeze(tasks), events: Object.freeze(events), artifacts: Object.freeze(artifacts) });
}
