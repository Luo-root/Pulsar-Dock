import { useEffect, useRef, useState } from 'react';
import { disconnectedBridge, disconnectedSnapshot, parseSnapshot, type HostBridge, type Snapshot, type TaskState } from './bridge';
import './styles.css';

const pages = ['工作台', '连接', '成果', '设置'] as const;
const tabs = ['状态', '事件', '成果', '权限'] as const;
const stateLabels: Record<TaskState, string> = { created: '已创建', preparing: '准备中', running: '运行中', waiting_approval: '等待审批', stopping: '停止中', succeeded: '已完成', failed: '失败', stopped: '已停止', expired: '已到期' };
type Preferences = { theme: 'light' | 'dark'; inspector: boolean };
function loadPreferences(): Preferences {
  try {
    const v: unknown = JSON.parse(localStorage.getItem('pulsar-dock.preferences') ?? 'null');
    if (v && typeof v === 'object' && 'theme' in v && 'inspector' in v && (v.theme === 'light' || v.theme === 'dark') && typeof v.inspector === 'boolean') return { theme: v.theme, inspector: v.inspector };
  } catch { /* Unavailable or invalid storage is not a connection failure. */ }
  return { theme: 'light', inspector: true };
}
export default function App({ bridge = disconnectedBridge }: { bridge?: HostBridge }) {
  const [snapshot, setSnapshot] = useState<Snapshot>(disconnectedSnapshot);
  const [error, setError] = useState('');
  const [page, setPage] = useState<typeof pages[number]>('工作台');
  const [tab, setTab] = useState<typeof tabs[number]>('状态');
  const [selectedId, select] = useState<string | null>(null);
  const [treeOpen, setTreeOpen] = useState(true);
  const [preferences, setPreferences] = useState(loadPreferences);
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([]);
  useEffect(() => {
    let active = true;
    let receivedUpdate = false;
    setSnapshot(disconnectedSnapshot); setError(''); select(null);
    const accept = (value: unknown) => {
      if (!active) return;
      try { setSnapshot(parseSnapshot(value)); setError(''); }
      catch { setSnapshot(disconnectedSnapshot); setError('主机状态格式无效，已拒绝展示。请检查桌面连接版本。'); }
    };
    let unsubscribe = () => {};
    try {
      unsubscribe = bridge.subscribe(value => { receivedUpdate = true; accept(value); });
      void bridge.getSnapshot().then(value => { if (!receivedUpdate) accept(value); }).catch(() => {
        if (active && !receivedUpdate) { setSnapshot(disconnectedSnapshot); setError('无法读取主机状态。请在可信桌面应用中检查连接，无需在此输入凭据。'); }
      });
    } catch { active = false; setSnapshot(disconnectedSnapshot); setError('无法订阅主机状态。请检查桌面连接。'); }
    return () => { active = false; unsubscribe(); };
  }, [bridge]);
  useEffect(() => {
    try { localStorage.setItem('pulsar-dock.preferences', JSON.stringify(preferences)); } catch { /* Preferences remain usable in memory. */ }
  }, [preferences]);
  const task = snapshot.tasks.find(t => t.id === selectedId);
  const events = snapshot.events.filter(e => e.taskId === task?.id);
  const artifacts = snapshot.artifacts.filter(a => a.taskId === task?.id);
  const unavailable = '当前仅提供只读状态接口，未接入可信操作桥；不能创建、启动、停止、撤权或应用成果。';
  const toggleInspector = () => setPreferences(p => ({ ...p, inspector: !p.inspector }));
  const artifactList = (all = false) => {
    const items = all ? snapshot.artifacts : artifacts;
    return items.length ? <ul className="records">{items.map(a => <li key={a.id}><strong>{a.name}</strong><span className="mono digest">SHA-256 {a.digest}</span></li>)}</ul> : <Empty title="尚无成果快照" description="成果由主机提供不可变引用后显示。任务完成不代表改动已应用。" />;
  };
  return <div className="app" data-theme={preferences.theme}>
    <a className="skip" href="#main">跳到主内容</a>
    <header className="topbar"><div className="brand"><span className="brand-mark" aria-hidden="true">⠿</span><strong>Pulsar Dock</strong><span className="brand-caption">安全协作工作台</span></div><span className="connection"><i aria-hidden="true" />{snapshot.connection === 'connected' ? '主机已连接 · 只读' : '主机未连接'}</span></header>
    <nav className="navigation" aria-label="主导航">{pages.map((p, i) => <button key={p} aria-current={page === p ? 'page' : undefined} onClick={() => setPage(p)}><span className="nav-symbol" aria-hidden="true">{['▦', '⌁', '▱', '⚙'][i]}</span>{p}</button>)}<span className="nav-note mono">LOCAL / READ ONLY</span></nav>
    <div className="notice" role="status"><span className="notice-label">{error ? '状态不可用' : snapshot.connection === 'connected' ? '只读连接' : '等待桌面连接'}</span><span>{error || (snapshot.connection === 'connected' ? '仅展示主机报告的数据，操作接口尚未接线。' : '尚未接入主机。任务、权限与成果将在可信连接建立后显示。')}</span></div>
    <div className={`workspace ${treeOpen ? '' : 'tree-closed'} ${preferences.inspector ? '' : 'inspector-closed'}`}>
      <aside className="task-panel" aria-label="任务树"><div className="panel-heading"><h2>{treeOpen ? '任务' : '树'}</h2><button className="icon-button" aria-label={treeOpen ? '折叠任务树' : '展开任务树'} aria-expanded={treeOpen} onClick={() => setTreeOpen(!treeOpen)}>{treeOpen ? '‹' : '›'}</button></div>{treeOpen && <><div className="tree-summary"><span>全部任务</span><span className="mono">{snapshot.tasks.length}</span></div>{snapshot.tasks.length ? <ul className="task-list">{snapshot.tasks.map(t => <li key={t.id}><button className={`task-node ${task?.id === t.id ? 'selected' : ''}`} onClick={() => select(t.id)} aria-pressed={task?.id === t.id}><span>{t.parentId ? '↳ ' : ''}{t.title}</span><small>{stateLabels[t.state]}</small><span className="permission-track" aria-label={`权限轨道：${t.permissions.length}项主机报告权限`}>{t.permissions.length ? t.permissions.map((p, i) => <span key={i} title={p}>{p}</span>) : <span className="no-grant">无权限记录</span>}</span></button></li>)}</ul> : <div className="tree-empty"><span className="tree-glyph" aria-hidden="true">└ ─</span><p>还没有任务</p><small>连接后查看真实任务树与协作者。</small></div>}<div className="tree-footer"><span className="legend-line" />权限轨道随任务展示<br /><small>不推测环境或授权状态</small></div></>}</aside>
      <main id="main" tabIndex={-1}>
        <div className="main-heading"><div><span className="eyebrow">{page === '工作台' ? '任务执行' : '本地控制台'}</span><h1>{page === '工作台' ? task?.title ?? '工作台' : page}</h1></div><button className="quiet" onClick={toggleInspector} aria-expanded={preferences.inspector}>{preferences.inspector ? '收起检查器' : '展开检查器'}</button></div>
        {page === '工作台' ? <><div className="actionbar"><div className="actions"><button disabled aria-describedby="action-reason">＋ 创建任务</button><button disabled aria-describedby="action-reason">启动</button><button disabled aria-describedby="action-reason">停止</button></div><span className="mono">{task ? stateLabels[task.state] : 'NO TASK SELECTED'}</span></div><p id="action-reason" className="action-reason">{unavailable}</p><div className="tabs" role="tablist" aria-label="任务详情">{tabs.map((t, index) => <button id={`tab-${index}`} key={t} ref={el => { tabRefs.current[index] = el; }} role="tab" aria-selected={tab === t} aria-controls="task-details" tabIndex={tab === t ? 0 : -1} onClick={() => setTab(t)} onKeyDown={e => { let next = index; if (e.key === 'ArrowRight') next = (index + 1) % tabs.length; else if (e.key === 'ArrowLeft') next = (index + tabs.length - 1) % tabs.length; else if (e.key === 'Home') next = 0; else if (e.key === 'End') next = tabs.length - 1; else return; e.preventDefault(); setTab(tabs[next]); tabRefs.current[next]?.focus(); }}>{t}</button>)}</div><section className="details" id="task-details" role="tabpanel" aria-labelledby={`tab-${tabs.indexOf(tab)}`} tabIndex={0}>{tab === '状态' ? task ? <><h2>主机报告状态</h2><dl><dt>任务标识</dt><dd className="mono">{task.id}</dd><dt>状态</dt><dd>{stateLabels[task.state]}</dd><dt>环境与进度</dt><dd>此接口未提供，不推测运行环境或完成比例。</dd></dl></> : <Empty title="从一个真实任务开始" description="当前没有选中的任务。先检查桌面连接；主机接入后，可在左侧选择任务查看状态。"><button className="primary" onClick={() => setPage('连接')}>查看连接状态 <span aria-hidden="true">→</span></button></Empty> : tab === '事件' ? events.length ? <ul className="records">{events.map(e => <li key={e.id}>{e.summary}</li>)}</ul> : <Empty title="暂无任务事件" description="只展示主机上报的事件，不生成模拟运行日志。" /> : tab === '成果' ? artifactList() : <><h2>权限记录</h2>{task?.permissions.length ? <ul className="records">{task.permissions.map((p, i) => <li key={i}>{p}</li>)}</ul> : <p className="muted">没有可展示的权限记录。未报告不等于已授权。</p>}</>}</section><div className="execution-note"><span aria-hidden="true">⌘</span><div><strong>原生交互尚未接线</strong><p>此区域不会执行命令、打开 Agent 页面或自动连接模型。</p></div></div></> : page === '连接' ? <section className="page-content"><h2>可信桌面连接</h2><p>主机{snapshot.connection === 'connected' ? '已连接，当前接口只读。' : '未连接。请在 Pulsar Dock 桌面壳中接入只读 HostBridge。'}</p><dl><dt>状态读取</dt><dd className="mono">getSnapshot / subscribe</dd><dt>模型与 MCP</dt><dd>尚未接线，不进行网络探测</dd><dt>凭据</dt><dd>此页面不收集密钥。请勿将凭据粘贴到任务或事件中。</dd></dl><button disabled aria-describedby="action-reason-connect">检查连接</button><p id="action-reason-connect" className="muted">主动诊断接口尚未接入；当前仅接收主机状态。</p></section> : page === '成果' ? <section className="page-content"><h2>不可变成果</h2>{artifactList(true)}<button disabled aria-describedby="apply-reason">应用成果</button><p id="apply-reason" className="muted">应用接口尚未接线。后续须由用户确认并核对原项目基线。</p></section> : <section className="page-content"><h2>界面偏好</h2><p className="muted">仅保存到此设备的浏览器存储，不更改主机策略。</p><label className="setting">主题<select value={preferences.theme} onChange={e => setPreferences(p => ({ ...p, theme: e.target.value as Preferences['theme'] }))}><option value="light">浅色 · 冷灰蓝</option><option value="dark">深色 · 墨蓝</option></select></label><label className="setting"><span>显示权限与成果检查器</span><input type="checkbox" checked={preferences.inspector} onChange={toggleInspector} /></label><div className="settings-limit"><h2>主机设置尚未接线</h2><p>安全策略、凭据库、环境清理和日志导出不能在此修改。</p></div></section>}
      </main>
      {preferences.inspector && <aside className="inspector" aria-label="权限与成果检查器"><div className="panel-heading"><h2>检查器</h2><span className="mono">READ ONLY</span></div><section><span className="eyebrow">选中任务</span><h3>{task?.title ?? '未选择任务'}</h3><p className="muted">{task ? stateLabels[task.state] : '选择任务后查看权限和成果引用。'}</p></section><section><div className="section-label"><h3>权限边界</h3><span className="tag">待核对</span></div><p className="muted">{task ? `主机报告 ${task.permissions.length} 项权限记录；完整授权范围需由可信控制面核对。` : '尚无任务权限记录，不能确认任何执行能力。'}</p><button disabled aria-describedby="revoke-reason">撤销权限</button><p className="fine" id="revoke-reason">{unavailable}</p></section><section><h3>成果快照</h3><p className="muted">{task ? `${artifacts.length} 项成果引用` : '无选中任务'}</p><p className="fine">成果不会自动写回原项目，也不会自动提交或发布。</p></section><div className="boundary-note"><span className="mono">TRUST BOUNDARY</span><p>任务内容仅作为文本展示。界面不执行其中的链接或指令。</p></div></aside>}
    </div><footer className="statusbar"><span>◯ {snapshot.connection === 'connected' ? '只读状态流' : '未建立主机连接'}</span><span>任务 {snapshot.tasks.length} <span aria-hidden="true"> / </span> 成果 {snapshot.artifacts.length}</span><span>未启用执行接口</span></footer>
  </div>;
}
function Empty({ title, description, children }: { title: string; description: string; children?: React.ReactNode }) {
  return <div className="empty"><div className="empty-diagram" aria-hidden="true"><span /> <i /> <span /></div><h2>{title}</h2><p>{description}</p>{children}</div>;
}
