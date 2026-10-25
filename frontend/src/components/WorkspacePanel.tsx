import { useRef, useState, type CSSProperties, type KeyboardEvent, type PointerEvent } from 'react';
import {
  ArrowRight, FileDiff, FileText, FolderOpen, LockKeyhole, PackageOpen,
  PanelRightClose, ShieldCheck, Workflow,
} from 'lucide-react';
import type { Snapshot } from '../bridge';
import type { Conversation, WorkspaceTab } from '../types';
import { WorkflowGraph } from './Pages';
import { Button, Hint } from './ui';

const workspaceTabs = [['resources', '资源'], ['artifacts', '成果'], ['permissions', '权限'], ['workflow', '工作流']] as const;

export function WorkspacePanel({ current, task, snapshot, tab, onTab, onClose }: {
  current: Conversation;
  task?: Snapshot['tasks'][number];
  snapshot: Snapshot;
  tab: WorkspaceTab;
  onTab: (tab: WorkspaceTab) => void;
  onClose: () => void;
}) {
  const [width, setWidth] = useState(360);
  const resizeStart = useRef<{ x: number; width: number } | null>(null);
  const beginResize = (event: PointerEvent<HTMLDivElement>) => {
    event.currentTarget.setPointerCapture(event.pointerId);
    resizeStart.current = { x: event.clientX, width };
  };
  const moveResize = (event: PointerEvent<HTMLDivElement>) => {
    if (!resizeStart.current) return;
    setWidth(Math.max(300, Math.min(480, resizeStart.current.width - (event.clientX - resizeStart.current.x))));
  };
  const keyResize = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
    event.preventDefault();
    const delta = event.key === 'ArrowLeft' ? -20 : 20;
    setWidth(value => Math.max(300, Math.min(480, value + delta)));
  };
  const artifacts = task ? snapshot.artifacts.filter(artifact => artifact.taskId === task.id) : [];
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const onTabKey = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    let next = index;
    if (event.key === 'ArrowRight') next = (index + 1) % workspaceTabs.length;
    else if (event.key === 'ArrowLeft') next = (index + workspaceTabs.length - 1) % workspaceTabs.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = workspaceTabs.length - 1;
    else return;
    event.preventDefault();
    onTab(workspaceTabs[next][0]);
    tabRefs.current[next]?.focus();
  };

  return (
    <aside className="workspace-panel" aria-label="右侧工作区" style={{ '--workspace-width': `${width}px` } as CSSProperties}>
      <div className="workspace-resize" role="separator" aria-label="调整右侧工作区宽度" aria-orientation="vertical" aria-valuemin={300} aria-valuemax={480} aria-valuenow={width} tabIndex={0} onPointerDown={beginResize} onPointerMove={moveResize} onPointerUp={() => { resizeStart.current = null; }} onPointerCancel={() => { resizeStart.current = null; }} onKeyDown={keyResize} />
      <div className="workspace-topbar"><strong>工作区</strong><Hint text="收起右侧工作区"><Button size="icon" aria-label="收起右侧工作区" onClick={onClose}><PanelRightClose size={15} /></Button></Hint></div>
      <div className="workspace-tabs" role="tablist" aria-label="工作区资源">
        {workspaceTabs.map(([value, label], index) => (
          <button
            key={value}
            ref={element => { tabRefs.current[index] = element; }}
            id={`workspace-tab-${value}`}
            type="button"
            role="tab"
            aria-selected={tab === value}
            aria-controls="workspace-tabpanel"
            tabIndex={tab === value ? 0 : -1}
            onClick={() => onTab(value)}
            onKeyDown={event => onTabKey(event, index)}
          >{label}</button>
        ))}
      </div>
      <div className="workspace-content" id="workspace-tabpanel" role="tabpanel" aria-labelledby={`workspace-tab-${tab}`}>
        {tab === 'resources' && <div className="resource-menu"><p className="workspace-intro">查看当前会话的辅助资源。示例与主机记录始终分开。</p><button onClick={() => onTab('artifacts')}><FileDiff size={18} /><span><strong>成果与差异</strong><small>固定快照、文件清单和示例差异</small></span><ArrowRight size={14} /></button><button onClick={() => onTab('permissions')}><ShieldCheck size={18} /><span><strong>权限边界</strong><small>主机报告权限与本地预览区别</small></span><ArrowRight size={14} /></button><button onClick={() => onTab('workflow')}><Workflow size={18} /><span><strong>工作流预览</strong><small>静态节点图 · 未接执行引擎</small></span><ArrowRight size={14} /></button><div className="resource-note"><FolderOpen size={16} /><span>项目文件浏览尚未接入<small>不会把本地预览工作区当作真实目录</small></span></div></div>}
        {tab === 'artifacts' && <div className="artifact-workspace"><div className="workspace-section-heading"><h2>成果与差异</h2><span className="preview-badge">{task ? '主机快照' : current.demo ? '示例' : '未生成'}</span></div>{task ? artifacts.length ? artifacts.map(artifact => <div className="artifact-reference" key={artifact.id}><FileText size={17} /><strong>{artifact.name}</strong><code>SHA-256 {artifact.digest}</code><p>不可变引用 · 未应用</p></div>) : <PanelEmpty text="该任务没有主机成果引用" /> : current.demo ? <><div className="file-header"><FileText size={14} /><code>docs/error-response.md</code><span>示例</span></div><div className="diff" aria-label="示例差异"><span className="diff-meta">@@ 统一错误响应结构 @@</span><span className="diff-add">+ error.code: 稳定业务语义</span><span className="diff-add">+ message: 可读说明</span><span className="diff-add">+ request_id: 关联日志</span><span className="diff-context">  internal_stack: 不对外返回</span></div><p className="artifact-footnote">这是静态示例差异，不对应磁盘文件，不会写回、提交或发布。</p><Button variant="outline" disabled>应用成果</Button></> : <PanelEmpty text="本地预览不会自动生成成果" />}</div>}
        {tab === 'permissions' && <div className="permission-workspace"><div className="workspace-section-heading"><h2>权限边界</h2><LockKeyhole size={15} /></div>{task ? <><p className="workspace-intro">以下来自主机快照；完整授权范围仍由可信控制面核对。</p>{task.permissions.length ? <div className="permission-list">{task.permissions.map(permission => <span key={permission}><ShieldCheck size={13} />{permission}</span>)}</div> : <PanelEmpty text="没有权限记录，未报告不等于已授权" />}</> : <><div className="permission-preview"><span><i />项目文件</span><strong>未连接</strong></div><div className="permission-preview"><span><i />模型服务</span><strong>未连接</strong></div><div className="permission-preview"><span><i />外部动作</span><strong>未授权</strong></div><p className="artifact-footnote">本地预览不持有主机权限。界面中的模式选择不会修改真实审批策略。</p></>}<Button variant="outline" disabled>撤销权限</Button></div>}
        {tab === 'workflow' && <div className="workflow-workspace"><div className="workspace-section-heading"><h2>工作流预览</h2><span className="preview-badge">未执行</span></div><p className="workspace-intro">保留未来 Pulse / Pulse Durable 的展示接缝，本轮只实现静态 UI。</p><WorkflowGraph compact /></div>}
      </div>
    </aside>
  );
}

function PanelEmpty({ text }: { text: string }) {
  return <div className="panel-empty"><PackageOpen size={23} /><p>{text}</p></div>;
}
