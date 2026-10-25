import { useRef } from 'react';
import {
  ArrowRight, Bot, Box, CircleDashed, Clock3, FileCheck2, FileText, GitBranch,
  Library, PackageOpen, Route, ShieldCheck, Workflow,
} from 'lucide-react';
import type { Snapshot, TaskState } from '../bridge';
import { agentOptions } from '../data';
import type { AgentKind, Page, SessionConfig } from '../types';
import { BrandIcon } from './BrandIcon';
import { Button, Choice } from './ui';

export const stateLabels: Record<TaskState, string> = {
  created: '已创建', preparing: '准备中', running: '运行中', waiting_approval: '等待审批',
  stopping: '停止中', succeeded: '已完成', failed: '失败', stopped: '已停止', expired: '已到期',
};

function PageHeading({ eyebrow, title, description, badge }: { eyebrow: string; title: string; description: string; badge?: string }) {
  return <header className="page-heading"><div><span className="eyebrow">{eyebrow}</span><h1>{title}</h1><p>{description}</p></div>{badge && <span className="preview-badge">{badge}</span>}</header>;
}

export function WorkflowGraph({ compact = false, template = '需求 → 实现 → 审查 → 交付' }: { compact?: boolean; template?: string }) {
  const allNodes = [
    { icon: FileText, title: '需求输入', detail: '目标、项目与权限摘要' },
    { icon: Bot, title: 'Agent 实现', detail: '独立工作区 · 未执行' },
    { icon: ShieldCheck, title: '独立审查', detail: '固定快照 · 未创建协作者' },
    { icon: PackageOpen, title: '成果交付', detail: '差异与证据 · 未生成' },
  ];
  const nodes = template === '轻量实现 → 独立审查' ? allNodes.slice(0, 3) : allNodes;
  return (
    <div className={`workflow-graph ${compact ? 'is-compact' : ''}`} aria-label="静态工作流预览">
      {nodes.map(({ icon: NodeIcon, title, detail }, index) => (
        <div className="workflow-graph-item" key={title}>
          <div className="workflow-node"><span className="workflow-node-icon"><NodeIcon size={compact ? 15 : 18} /></span><div><strong>{title}</strong><small>{detail}</small></div><span className="node-state">未执行</span></div>
          {index < nodes.length - 1 && <div className="workflow-connector" aria-hidden="true"><span /><ArrowRight size={14} /></div>}
        </div>
      ))}
    </div>
  );
}

function AgentsPage({ onUseAgent }: { onUseAgent: (agent: AgentKind) => void }) {
  return (
    <div className="management-page"><PageHeading eyebrow="AGENT REGISTRY" title="Agent" description="为会话选择合适的 Agent。真实版本与能力只由可信主机报告。" badge="能力未检测" />
      <div className="agent-grid">{agentOptions.map(agent => <article className="agent-card" key={agent.name}><div className="agent-card-top"><span className="agent-card-icon"><BrandIcon name={agent.name} size={22} /></span><span className="status-pill"><CircleDashed size={12} />未检测</span></div><h2>{agent.name}</h2><p>{agent.description}</p><div className="agent-card-footer"><span>不会在此安装或启动</span><Button variant="outline" onClick={() => onUseAgent(agent.name)}>用于本地预览 <ArrowRight size={13} /></Button></div></article>)}</div>
      <section className="info-section"><Bot size={18} /><div><h2>适配器不是运行保证</h2><p>界面选择只改变本地预览配置。安装状态、模型协议、MCP、会话恢复和工具事件需要在对应版本实测后才能显示。</p></div></section>
    </div>
  );
}

function WorkflowPage({ config, onConfig }: { config: SessionConfig; onConfig: (patch: Partial<SessionConfig>) => void }) {
  const modes: { value: SessionConfig['mode']; label: string }[] = [
    { value: 'direct', label: '对话' },
    { value: 'workflow', label: '工作流' },
    { value: 'auto', label: 'AUTO' },
  ];
  const modeRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const onModeKey = (event: React.KeyboardEvent, index: number) => {
    let next = index;
    if (event.key === 'ArrowRight') next = (index + 1) % modes.length;
    else if (event.key === 'ArrowLeft') next = (index + modes.length - 1) % modes.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = modes.length - 1;
    else return;
    event.preventDefault();
    onConfig({ mode: modes[next].value });
    modeRefs.current[next]?.focus();
  };
  const panelProps = {
    role: 'tabpanel',
    id: 'management-mode-panel',
    'aria-labelledby': `management-mode-${config.mode}`,
  } as const;
  return (
    <div className="management-page">
      <PageHeading eyebrow="RUN MODES" title="运行模式" description="保留对话、工作流与 AUTO 的统一入口。当前只展示界面与配置，不执行流程。" badge="静态 UI · 未接执行引擎" />
      <div className="management-tabs" role="tablist" aria-label="运行模式设置">
        {modes.map((mode, index) => (
          <button
            key={mode.value}
            ref={element => { modeRefs.current[index] = element; }}
            id={`management-mode-${mode.value}`}
            type="button"
            role="tab"
            aria-selected={config.mode === mode.value}
            aria-controls="management-mode-panel"
            tabIndex={config.mode === mode.value ? 0 : -1}
            onClick={() => onConfig({ mode: mode.value })}
            onKeyDown={event => onModeKey(event, index)}
          >{mode.label}</button>
        ))}
      </div>
      {config.mode === 'direct' && <section {...panelProps} className="mode-settings"><div className="mode-settings-heading"><Bot size={19} /><div><h2>对话</h2><p>由一个选定 Agent 处理会话。协作者仍需遵循任务授权与独立环境边界。</p></div></div><Choice label="默认 Agent" value={config.agent} options={agentOptions.map(item => item.name)} onChange={agent => { const selected = agentOptions.find(item => item.name === agent); if (selected) onConfig({ agent: selected.name }); }} /><p className="muted">此处只保存当前本地预览的选择，不探测或启动 Agent。</p></section>}
      {config.mode === 'auto' && <section {...panelProps} className="mode-settings"><div className="mode-settings-heading"><Route size={19} /><div><h2>AUTO</h2><p>为未来的自动角色与流程选择预留目标输入。当前没有自动路由或模型调用。</p></div></div><label className="field-label" htmlFor="management-goal">全局 Goal</label><input id="management-goal" className="text-field" value={config.goal} onChange={event => onConfig({ goal: event.target.value })} placeholder="输入一个仅用于界面预览的目标" /><p className="muted">目标只保留在当前窗口内存，刷新后清空。</p></section>}
      {config.mode === 'workflow' && <section {...panelProps} className="workflow-editor"><div className="workflow-editor-top"><div><h2>{config.workflow}</h2><p>{config.workflow === '轻量实现 → 独立审查' ? '保留输入、实现和独立审查的轻量静态模板。' : '一个适合 Pulsar Dock 安全协作边界的静态模板。'}</p></div><Choice label="工作流模板" value={config.workflow} options={['需求 → 实现 → 审查 → 交付', '轻量实现 → 独立审查']} onChange={workflow => onConfig({ workflow })} /></div><div className="workflow-canvas"><div className="canvas-grid" /><WorkflowGraph template={config.workflow} /></div><div className="workflow-notes"><span><Workflow size={14} />UI 模板已保留</span><span><Clock3 size={14} />执行与恢复未接入</span></div></section>}
      <section className="info-section"><Box size={18} /><div><h2>后续执行层</h2><p>Pulse 可承载数据依赖、节点状态与单进程执行；Pulse Durable 可承接检查点存储和恢复。本轮没有引入、配置或验证这两个执行库，也没有把静态节点图当成真实运行。</p></div></section>
    </div>
  );
}

function ArtifactsPage({ snapshot, actionReason }: { snapshot: Snapshot; actionReason: string }) {
  const waiting = snapshot.tasks.filter(task => task.state === 'waiting_approval');
  return (
    <div className="management-page"><PageHeading eyebrow="REVIEW & ARTIFACTS" title="审批与成果" description="审批具体权限，检查不可变成果，再由用户明确决定是否应用。" badge="只读" />
      <section className="approval-section"><div className="section-heading"><h2>待处理审批</h2><span className="count-badge">{waiting.length}</span></div>{waiting.length ? <ul className="artifact-list">{waiting.map(task => <li key={task.id}><ShieldCheck size={18} /><div><strong>{task.title}</strong><span>{task.id} · 等待审批</span><code>主机只报告等待状态，审批参数与对象未提供。</code></div><Button variant="outline" disabled>审批详情未提供</Button></li>)}</ul> : <div className="empty-block"><ShieldCheck size={25} /><h3>没有待处理审批</h3><p>当前主机快照没有等待审批的任务。没有记录不等于已经授权。</p></div>}</section>
      <section className="artifact-section"><div className="section-heading"><h2>主机成果</h2><span className="count-badge">{snapshot.artifacts.length}</span></div>{snapshot.artifacts.length ? <ul className="artifact-list">{snapshot.artifacts.map(artifact => <li key={artifact.id}><FileCheck2 size={18} /><div><strong>{artifact.name}</strong><span>{artifact.taskId}</span><code>SHA-256 {artifact.digest}</code></div><Button variant="outline" disabled aria-describedby="artifact-action-reason">应用成果</Button></li>)}</ul> : <div className="empty-block"><PackageOpen size={25} /><h3>尚无主机成果</h3><p>成果由主机提供不可变引用后显示。任务完成不代表改动已应用。</p></div>}</section>
      <p className="action-reason" id="artifact-action-reason">{actionReason}</p>
    </div>
  );
}

export function ManagementPage({ page, snapshot, config, onConfig, onUseAgent, actionReason }: {
  page: Exclude<Page, 'home' | 'conversation'>;
  snapshot: Snapshot;
  config: SessionConfig;
  onConfig: (patch: Partial<SessionConfig>) => void;
  onUseAgent: (agent: AgentKind) => void;
  actionReason: string;
}) {
  if (page === 'agents') return <AgentsPage onUseAgent={onUseAgent} />;
  if (page === 'workflow') return <WorkflowPage config={config} onConfig={onConfig} />;
  return <ArtifactsPage snapshot={snapshot} actionReason={actionReason} />;
}

export function HostConversation({ task, snapshot }: { task: Snapshot['tasks'][number]; snapshot: Snapshot }) {
  const events = snapshot.events.filter(event => event.taskId === task.id);
  const artifacts = snapshot.artifacts.filter(artifact => artifact.taskId === task.id);
  return (
    <div className="host-conversation"><header className="conversation-header"><div className="conversation-heading"><h1>{task.title}</h1><span className="agent-identity"><Library size={14} />主机记录</span><span className="session-id" style={{ textOverflow: 'ellipsis' }}>{task.id}</span></div><span className="preview-badge">只读 · {stateLabels[task.state]}</span></header>
      <div className="host-scroll"><div className="host-content"><div className="preview-line"><span className="preview-badge">主机报告</span><span>这里只展示接口已有的事实，不推测环境、进度或授权。</span></div><dl className="host-facts"><div><dt>任务状态</dt><dd>{stateLabels[task.state]}</dd></div><div><dt>父子关系</dt><dd>{task.parentId ? `子任务 · ${task.parentId}` : '根任务'}</dd></div><div><dt>权限记录</dt><dd>{task.permissions.length} 项</dd></div><div><dt>成果引用</dt><dd>{artifacts.length} 项</dd></div></dl>
        <section className="host-events"><h2>事件</h2>{events.length ? events.map(event => <div className="host-event" key={event.id}><span className="event-icon"><GitBranch size={15} /></span><p style={{ overflowWrap: 'anywhere' }}>{event.summary}</p></div>) : <div className="empty-inline">暂无任务事件。不会生成模拟运行日志。</div>}</section>
        <section className="host-permissions"><h2>权限记录</h2>{task.permissions.length ? <div className="permission-tags">{task.permissions.map(permission => <span key={permission} style={{ overflowWrap: 'anywhere' }}>{permission}</span>)}</div> : <p className="muted">没有可展示的权限记录。未报告不等于已授权。</p>}</section>
      </div></div>
      <div className="composer-dock"><div className="readonly-composer"><span>当前主机接口只提供状态记录，不能发送消息或控制任务。</span><Button disabled>只读</Button></div></div>
    </div>
  );
}
