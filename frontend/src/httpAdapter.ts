import type { AgentKind } from './types';

export interface AgentInfo {
  agentId: string;
  displayName?: string;
  installed: boolean;
  cliVerified: boolean;
  modelVerified: boolean;
  operations: string[];
}
export interface TaskResult { id: string; parentId?: string | null; title?: string; state?: string; permissions?: string[] }
export interface ExecuteResult { task: TaskResult; commandCompleted: boolean; output: string }
export interface ConnectionConfig { baseURL: string; token: string }

const candidates: Record<AgentKind, string> = { 'Claude Code': 'claude', Codex: 'codex', OpenCode: 'opencode' };
export const agentCandidateId = (agent: AgentKind) => candidates[agent];

export class DesktopHttpError extends Error { constructor(message: string, readonly status?: number) { super(message); this.name = 'DesktopHttpError'; } }

function endpoint(baseURL: string, path: string) { return `${baseURL.replace(/\/+$/, '')}${path}`; }
function validBaseURL(value: string) { try { const url = new URL(value); return url.protocol === 'http:' && (url.hostname === '127.0.0.1' || url.hostname === 'localhost' || url.hostname === '::1'); } catch { return false; } }

export class DesktopHttpAdapter {
  constructor(private readonly config: ConnectionConfig, private readonly fetcher: typeof fetch = fetch) {
    if (!validBaseURL(config.baseURL)) throw new DesktopHttpError('只允许连接本机 HTTP 服务。');
    if (!config.token.trim()) throw new DesktopHttpError('连接令牌不能为空。');
  }
  private async request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const response = await this.fetcher(endpoint(this.config.baseURL, path), { ...init, headers: { Authorization: `Bearer ${this.config.token}`, 'Content-Type': 'application/json', ...(init.headers ?? {}) } });
    if (!response.ok) throw new DesktopHttpError(`桌面服务请求失败（${response.status}）。`, response.status);
    return response.json() as Promise<T>;
  }
  listAgents() { return this.request<AgentInfo[]>('/api/agents'); }
  createTask(agentID: string, goal: string, idempotencyKey: string) { return this.request<TaskResult>(`/api/agents/${encodeURIComponent(agentID)}/tasks`, { method: 'POST', body: JSON.stringify({ goal, idempotencyKey, agentId: agentID }) }); }
  execute(agentID: string, rootID: string, taskID: string) { return this.request<ExecuteResult>(`/api/agents/${encodeURIComponent(agentID)}/tasks/${encodeURIComponent(rootID)}/${encodeURIComponent(taskID)}/execute`, { method: 'POST' }); }
  stop(agentID: string, rootID: string, taskID: string) { return this.request<void>(`/api/agents/${encodeURIComponent(agentID)}/tasks/${encodeURIComponent(rootID)}/${encodeURIComponent(taskID)}/stop`, { method: 'POST' }); }
}

export function canOperate(agent: AgentInfo | undefined, kind: AgentKind) {
  return !!agent && agent.agentId === agentCandidateId(kind) && agent.installed && agent.cliVerified && agent.modelVerified && agent.operations.includes('execute') && agent.operations.includes('stop');
}
