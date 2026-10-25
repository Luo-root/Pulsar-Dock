import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import App from './App';
import type { AgentInfo, DesktopClient, ExecuteResult, TaskResult } from './httpAdapter';

const agent: AgentInfo = { id: 'claude', installed: true, cliVerified: true, modelVerified: true, operations: ['create', 'execute'] };
const task = (id: string, rootId: string, goal: string): TaskResult => ({ id, rootId, goal, state: 'created', createdAt: '2026-10-25T00:00:00Z', updatedAt: '2026-10-25T00:00:00Z' });
const result = (id: string, rootId: string, output: string): ExecuteResult => ({ task: task(id, rootId, output), commandCompleted: true, output });
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (reason?: unknown) => void; const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; }); return { promise, resolve, reject }; }
class FakeClient implements DesktopClient {
  readonly agents = deferred<AgentInfo[]>(); readonly creates: string[] = []; readonly executes: { rootId: string; taskId: string }[] = []; readonly stops: { rootId: string }[] = [];
  readonly taskResults = new Map<string, ReturnType<typeof deferred<TaskResult>>>(); readonly executeResults = new Map<string, ReturnType<typeof deferred<ExecuteResult>>>();
  listAgents() { return this.agents.promise; }
  createTask(_agent: string, goal: string) { this.creates.push(goal); const d = deferred<TaskResult>(); this.taskResults.set(goal, d); return d.promise; }
  execute(_agent: string, rootId: string, taskId: string) { this.executes.push({ rootId, taskId }); const d = deferred<ExecuteResult>(); this.executeResults.set(taskId, d); return d.promise; }
  stop(_agent: string, rootId: string) { this.stops.push({ rootId }); return Promise.resolve(); }
}
function openConnection(user: ReturnType<typeof userEvent.setup>) { return user.click(screen.getByRole('button', { name: '设置' })).then(() => user.click(screen.getByRole('button', { name: '连接中心' }))); }

describe('desktop operation concurrency', () => {
  it('publishes only the newest connection after reverse-order directory responses', async () => {
    const user = userEvent.setup(); const clients: FakeClient[] = [];
    render(<App adapterFactory={() => { const client = new FakeClient(); clients.push(client); return client; }} />);
    await openConnection(user); await user.click(screen.getByRole('button', { name: '连接并读取 Agent' })); await user.click(screen.getByRole('button', { name: '连接并读取 Agent' }));
    clients[1].agents.resolve([agent]); clients[0].agents.resolve([{ ...agent, id: 'claude' }]); await user.keyboard('{Escape}'); await waitFor(() => expect(screen.getByRole('button', { name: '发送到 Agent' })).toBeInTheDocument());
  });
  it('allows only one create, keeps result in original conversation, and stops its bound root', async () => {
    const user = userEvent.setup(); const client = new FakeClient(); render(<App adapterFactory={() => client} />);
    await openConnection(user); client.agents.resolve([agent]); await user.click(screen.getByRole('button', { name: '连接并读取 Agent' })); await user.keyboard('{Escape}'); await waitFor(() => expect(screen.getByRole('button', { name: '发送到 Agent' })).toBeInTheDocument());
    const input = screen.getByRole('textbox', { name: '消息草稿' }); await user.type(input, 'first'); await user.keyboard('{Enter}'); await user.keyboard('{Enter}'); expect(client.creates).toEqual(['first']);
    client.taskResults.get('first')!.resolve(task('task-1', 'root-1', 'first')); await waitFor(() => expect(client.executes).toHaveLength(1)); await user.click(screen.getByRole('button', { name: '快速对话' }));
    await user.click(screen.getByRole('button', { name: '停止任务' })); expect(client.stops).toEqual([{ rootId: 'root-1' }]);
    client.executeResults.get('task-1')!.resolve(result('task-1', 'root-1', 'original-result')); await waitFor(() => expect(screen.getByRole('textbox', { name: '消息草稿' })).toBeInTheDocument()); expect(screen.queryByText('original-result')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /first/ })); await waitFor(() => expect(screen.getByText('original-result')).toBeInTheDocument());
  });
});
