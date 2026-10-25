import { describe, expect, it, vi } from 'vitest';
import { canOperate, DesktopHttpAdapter } from './httpAdapter';

describe('DesktopHttpAdapter', () => {
  it('uses bearer auth and exact task routes, including server stop', async () => {
    const calls: { url: string; init?: RequestInit }[] = [];
    const fetcher = vi.fn(async (url: string, init?: RequestInit) => { calls.push({ url, init }); return new Response(JSON.stringify(url.endsWith('/agents') ? [] : url.endsWith('/execute') ? { task: { id: 't' }, commandCompleted: false, output: 'out' } : {}), { status: 200 }); });
    const adapter = new DesktopHttpAdapter({ baseURL: 'http://127.0.0.1:8787/', token: 'synthetic-session-token' }, fetcher);
    await adapter.listAgents();
    await adapter.createTask('opencode', 'goal', 'idem');
    await adapter.execute('opencode', 'root', 'task');
    await adapter.stop('opencode', 'root', 'task');
    expect(calls.map(call => call.url)).toEqual(['http://127.0.0.1:8787/api/agents', 'http://127.0.0.1:8787/api/agents/opencode/tasks', 'http://127.0.0.1:8787/api/agents/opencode/tasks/root/task/execute', 'http://127.0.0.1:8787/api/agents/opencode/tasks/root/task/stop']);
    expect((calls[1].init?.headers as Record<string, string>).Authorization).toBe('Bearer synthetic-session-token');
    expect(JSON.parse(String(calls[1].init?.body))).toEqual({ goal: 'goal', idempotencyKey: 'idem', agentId: 'opencode' });
  });
  it('requires loopback and all server capability gates', () => {
    expect(() => new DesktopHttpAdapter({ baseURL: 'https://example.test', token: 'x' })).toThrow();
    const base = { agentId: 'opencode', installed: true, cliVerified: true, modelVerified: true, operations: ['execute', 'stop'] };
    expect(canOperate(base, 'OpenCode')).toBe(true);
    expect(canOperate({ ...base, operations: ['execute'] }, 'OpenCode')).toBe(false);
    expect(canOperate({ ...base, modelVerified: false }, 'OpenCode')).toBe(false);
  });
});
