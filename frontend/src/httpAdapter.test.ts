import { describe, expect, it, vi } from 'vitest';
import { canOperate, DesktopHttpAdapter } from './httpAdapter';

const task = { id: 'task-1', rootId: 'root-1', goal: 'goal', state: 'created', createdAt: '2026-10-25T00:00:00Z', updatedAt: '2026-10-25T00:00:00Z' };
const agent = { id: 'opencode', installed: true, cliVerified: true, modelVerified: true, operations: ['create', 'execute'] };

describe('DesktopHttpAdapter', () => {
  it('uses real DTO fields, bearer auth, exact routes, and independent server stop', async () => {
    const calls: { url: string; init?: RequestInit }[] = [];
    const fetcher = vi.fn(async (input: URL | RequestInfo, init?: RequestInit) => { const url = String(input); calls.push({ url, init }); if (url.endsWith('/agents')) return new Response(JSON.stringify([agent]), { status: 200 }); if (url.endsWith('/execute')) return new Response(JSON.stringify({ task, commandCompleted: false }), { status: 200 }); if (url.endsWith('/tasks')) return new Response(JSON.stringify(task), { status: 200 }); return new Response(null, { status: 204 }); });
    const adapter = new DesktopHttpAdapter({ baseURL: 'http://127.0.0.1:8787/', token: 'synthetic-session-token' }, fetcher);
    expect(await adapter.listAgents()).toEqual([agent]);
    expect(await adapter.createTask('opencode', 'goal', 'idem')).toEqual(task);
    expect((await adapter.execute('opencode', 'root-1', 'task-1')).task.rootId).toBe('root-1');
    await adapter.stop('opencode', 'root-1');
    expect(calls.map(call => call.url)).toEqual(['http://127.0.0.1:8787/api/agents', 'http://127.0.0.1:8787/api/agents/opencode/tasks', 'http://127.0.0.1:8787/api/agents/opencode/tasks/root-1/task-1/execute', 'http://127.0.0.1:8787/api/agents/opencode/tasks/root-1/stop']);
    expect((calls[1].init?.headers as Record<string, string>).Authorization).toBe('Bearer synthetic-session-token');
    expect(JSON.parse(String(calls[1].init?.body))).toEqual({ goal: 'goal', idempotencyKey: 'idem', agentId: 'opencode' });
    expect(calls[3].url).not.toContain('task-1');
  });
  it('rejects unsafe bases, redirects, invalid JSON, and malformed bounded responses', async () => {
    for (const baseURL of ['https://example.test/', 'http://localhost/user:pass@/', 'http://127.0.0.1/?token=x', 'http://127.0.0.1/api']) expect(() => new DesktopHttpAdapter({ baseURL, token: 'x' })).toThrow();
    const redirect = vi.fn(async (_url: URL | RequestInfo, init?: RequestInit) => { expect(init?.redirect).toBe('error'); throw new Error('redirect'); });
    await expect(new DesktopHttpAdapter({ baseURL: 'http://[::1]/', token: 'x' }, redirect).listAgents()).rejects.toThrow('桌面服务请求失败');
    const invalid = vi.fn(async () => new Response(JSON.stringify([{ ...agent, id: '' }]), { status: 200 }));
    await expect(new DesktopHttpAdapter({ baseURL: 'http://localhost/', token: 'x' }, invalid).listAgents()).rejects.toThrow('格式无效');
    const badTask = vi.fn(async () => new Response(JSON.stringify({ ...task, rootId: '' }), { status: 200 }));
    await expect(new DesktopHttpAdapter({ baseURL: 'http://localhost/', token: 'x' }, badTask).createTask('opencode', 'goal', 'idem')).rejects.toThrow('格式无效');
  });
  it('requires create and execute plus all three verification gates, but not stop operation', () => {
    expect(canOperate(agent, 'OpenCode')).toBe(true);
    expect(canOperate({ ...agent, operations: ['create'] }, 'OpenCode')).toBe(false);
    expect(canOperate({ ...agent, modelVerified: false }, 'OpenCode')).toBe(false);
    expect(canOperate({ ...agent, installed: false }, 'OpenCode')).toBe(false);
  });
});
