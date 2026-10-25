import type { AgentKind } from './types';
import { taskStates, type TaskState } from './bridge';

export interface AgentInfo {
  id: string;
  displayName?: string;
  installed: boolean;
  cliVerified: boolean;
  modelVerified: boolean;
  operations: string[];
}
export interface TaskResult {
  id: string;
  rootId: string;
  goal: string;
  state: TaskState;
  createdAt: string;
  updatedAt: string;
}
export interface ExecuteResult { task: TaskResult; commandCompleted: boolean; output: string }
export interface ConnectionConfig { baseURL: string; token: string }

const candidates: Record<AgentKind, string> = { 'Claude Code': 'claude', Codex: 'codex', OpenCode: 'opencode' };
const knownOperations = new Set(['create', 'execute']);
const maxAgents = 100;
const maxOperations = 16;
const maxText = 8192;
const maxOutput = 64 * 1024;
export const agentCandidateId = (agent: AgentKind) => candidates[agent];

export class DesktopHttpError extends Error { constructor(message: string, readonly status?: number) { super(message); this.name = 'DesktopHttpError'; } }

function endpoint(baseURL: string, path: string) { return `${baseURL.replace(/\/+$/, '')}${path}`; }
function validBaseURL(value: string) {
  try {
    const url = new URL(value);
    return url.protocol === 'http:' && !url.username && !url.password && !url.search && !url.hash && url.pathname === '/' && ['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname);
  } catch { return false; }
}
const record = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const boundedText = (value: unknown, limit = maxText): value is string => typeof value === 'string' && value.length > 0 && value.length <= limit;
function parseAgent(value: unknown): AgentInfo {
  if (!record(value) || !boundedText(value.id) || (value.displayName !== undefined && !boundedText(value.displayName)) || typeof value.installed !== 'boolean' || typeof value.cliVerified !== 'boolean' || typeof value.modelVerified !== 'boolean' || !Array.isArray(value.operations) || value.operations.length > maxOperations || !value.operations.every(operation => boundedText(operation) && knownOperations.has(operation))) throw new DesktopHttpError('桌面服务返回的 Agent 目录格式无效。');
  return { id: value.id, ...(value.displayName === undefined ? {} : { displayName: value.displayName }), installed: value.installed, cliVerified: value.cliVerified, modelVerified: value.modelVerified, operations: [...value.operations] };
}
function parseTask(value: unknown): TaskResult {
  if (!record(value) || !boundedText(value.id) || !boundedText(value.rootId) || !boundedText(value.goal) || !taskStates.includes(value.state as TaskState) || !boundedText(value.createdAt) || !boundedText(value.updatedAt)) throw new DesktopHttpError('桌面服务返回的任务格式无效。');
  return { id: value.id, rootId: value.rootId, goal: value.goal, state: value.state as TaskState, createdAt: value.createdAt, updatedAt: value.updatedAt };
}
function parseAgents(value: unknown): AgentInfo[] { if (!Array.isArray(value) || value.length > maxAgents) throw new DesktopHttpError('桌面服务返回的 Agent 目录格式无效。'); return value.map(parseAgent); }
function parseTaskResponse(value: unknown): TaskResult { return parseTask(value); }
function parseExecute(value: unknown): ExecuteResult { if (!record(value) || typeof value.commandCompleted !== 'boolean' || typeof value.output !== 'string' || value.output.length > maxOutput) throw new DesktopHttpError('桌面服务返回的执行结果格式无效。'); return { task: parseTask(value.task), commandCompleted: value.commandCompleted, output: value.output }; }

export class DesktopHttpAdapter {
  constructor(private readonly config: ConnectionConfig, private readonly fetcher: typeof fetch = fetch) {
    if (!validBaseURL(config.baseURL)) throw new DesktopHttpError('只允许连接本机根路径 HTTP 服务。');
    if (!config.token.trim()) throw new DesktopHttpError('连接令牌不能为空。');
  }
  private async request<T>(path: string, init: RequestInit = {}, parse: (value: unknown) => T): Promise<T> {
    let response: Response;
    try { response = await this.fetcher(endpoint(this.config.baseURL, path), { ...init, redirect: 'error', headers: { Authorization: `Bearer ${this.config.token}`, 'Content-Type': 'application/json', ...(init.headers ?? {}) } }); }
    catch { throw new DesktopHttpError('桌面服务请求失败。'); }
    if (!response.ok) throw new DesktopHttpError(`桌面服务请求失败（${response.status}）。`, response.status);
    if (response.status === 204) return parse(undefined);
    let body: unknown;
    try { body = await response.json(); } catch { throw new DesktopHttpError('桌面服务响应格式无效。'); }
    return parse(body);
  }
  listAgents() { return this.request('/api/agents', {}, parseAgents); }
  createTask(agentID: string, goal: string, idempotencyKey: string) { return this.request(`/api/agents/${encodeURIComponent(agentID)}/tasks`, { method: 'POST', body: JSON.stringify({ goal, idempotencyKey, agentId: agentID }) }, parseTaskResponse); }
  execute(agentID: string, rootID: string, taskID: string) { return this.request(`/api/agents/${encodeURIComponent(agentID)}/tasks/${encodeURIComponent(rootID)}/${encodeURIComponent(taskID)}/execute`, { method: 'POST' }, parseExecute); }
  stop(agentID: string, rootID: string) { return this.request(`/api/agents/${encodeURIComponent(agentID)}/tasks/${encodeURIComponent(rootID)}/stop`, { method: 'POST' }, () => undefined); }
}

export function canOperate(agent: AgentInfo | undefined, kind: AgentKind) {
  return !!agent && agent.id === agentCandidateId(kind) && agent.installed && agent.cliVerified && agent.modelVerified && agent.operations.includes('create') && agent.operations.includes('execute');
}
