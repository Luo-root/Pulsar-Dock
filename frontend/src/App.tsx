import { useEffect, useRef, useState } from 'react';
import { Menu, PanelRightOpen } from 'lucide-react';
import { disconnectedBridge, disconnectedSnapshot, parseSnapshot, type HostBridge, type Snapshot } from './bridge';
import { Chat } from './components/Chat';
import { HostConversation, ManagementPage } from './components/Pages';
import { SettingsDialog, type SettingsTab } from './components/SettingsDialog';
import { Sidebar } from './components/Sidebar';
import { Button, Hint } from './components/ui';
import { WorkspacePanel } from './components/WorkspacePanel';
import { createConversation, defaultConfig, demoConversation } from './data';
import type { Conversation, Page, Preferences, SessionConfig, WorkspaceTab } from './types';
import './styles.css';

const unavailable = '当前仅提供只读状态接口，未接入可信操作桥；不能创建、启动、停止、撤权或应用成果。';
const defaultPreferences: Preferences = { theme: 'light', inspector: false, compact: false };

function loadPreferences(): Preferences {
  try {
    const value: unknown = JSON.parse(localStorage.getItem('pulsar-dock.preferences') ?? 'null');
    if (value && typeof value === 'object' && 'theme' in value && 'inspector' in value && (value.theme === 'light' || value.theme === 'dark') && typeof value.inspector === 'boolean') {
      return { theme: value.theme, inspector: value.inspector, compact: 'compact' in value && value.compact === true };
    }
  } catch { /* Invalid or unavailable storage does not affect the host connection. */ }
  return defaultPreferences;
}

export default function App({ bridge = disconnectedBridge }: { bridge?: HostBridge }) {
  const [snapshot, setSnapshot] = useState<Snapshot>(disconnectedSnapshot);
  const [error, setError] = useState('');
  const [page, setPage] = useState<Page>('home');
  const [conversations, setConversations] = useState<Conversation[]>(() => [createConversation('draft-1'), demoConversation]);
  const [selectedId, setSelectedId] = useState('draft-1');
  const [hostTaskId, setHostTaskId] = useState<string | null>(null);
  const [query, setQuery] = useState('');
  const [moreOpen, setMoreOpen] = useState(false);
  const [preferences, setPreferences] = useState<Preferences>(loadPreferences);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);
  const [workspaceTab, setWorkspaceTab] = useState<WorkspaceTab>('resources');
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [settingsTab, setSettingsTab] = useState<SettingsTab>('通用');
  const counter = useRef(1);
  const settingsReturnFocus = useRef<HTMLElement | null>(null);

  useEffect(() => {
    let active = true;
    let receivedUpdate = false;
    setSnapshot(disconnectedSnapshot);
    setError('');
    setHostTaskId(null);
    setPage('home');
    const accept = (value: unknown) => {
      if (!active) return;
      try { setSnapshot(parseSnapshot(value)); setError(''); }
      catch {
        setSnapshot(disconnectedSnapshot);
        setHostTaskId(null);
        setPage('home');
        setError('主机状态格式无效，已拒绝展示。请检查桌面连接版本。');
      }
    };
    let unsubscribe = () => {};
    try {
      unsubscribe = bridge.subscribe(value => { receivedUpdate = true; accept(value); });
      void bridge.getSnapshot().then(value => { if (!receivedUpdate) accept(value); }).catch(() => {
        if (active && !receivedUpdate) {
          setSnapshot(disconnectedSnapshot);
          setHostTaskId(null);
          setPage('home');
          setError('无法读取主机状态。请在可信桌面应用中检查连接，无需在此输入凭据。');
        }
      });
    } catch {
      active = false;
      setSnapshot(disconnectedSnapshot);
      setHostTaskId(null);
      setPage('home');
      setError('无法订阅主机状态。请检查桌面连接。');
    }
    return () => { active = false; unsubscribe(); };
  }, [bridge]);

  useEffect(() => {
    document.documentElement.dataset.theme = preferences.theme;
    document.documentElement.style.colorScheme = preferences.theme;
    try { localStorage.setItem('pulsar-dock.preferences', JSON.stringify(preferences)); } catch { /* Preferences remain available in memory. */ }
  }, [preferences]);

  useEffect(() => {
    if (hostTaskId && !snapshot.tasks.some(task => task.id === hostTaskId)) {
      setHostTaskId(null);
      setPage('home');
    }
  }, [hostTaskId, snapshot.tasks]);

  const current = conversations.find(item => item.id === selectedId) ?? conversations[0];
  const hostTask = snapshot.tasks.find(item => item.id === hostTaskId);
  const chatVisible = page === 'home' || page === 'conversation';

  const updateCurrent = (patch: Partial<Conversation>) => {
    setConversations(items => items.map(item => item.id === selectedId ? { ...item, ...patch } : item));
  };
  const updateConfig = (patch: Partial<SessionConfig>) => {
    setConversations(items => items.map(item => item.id === selectedId ? { ...item, config: { ...item.config, ...patch } } : item));
  };
  const updatePreferences = (patch: Partial<Preferences>) => setPreferences(value => ({ ...value, ...patch }));

  const quickChat = (agent?: string) => {
    setHostTaskId(null);
    setMobileOpen(false);
    if (!agent && page === 'home' && !current.demo && !current.messages.length) {
      setPage('home');
      return;
    }
    const id = `draft-${++counter.current}`;
    const next = createConversation(id);
    if (agent) next.config = { ...defaultConfig, agent };
    setConversations(items => [...items, next]);
    setSelectedId(id);
    setPage('home');
  };

  const selectConversation = (id: string) => {
    const next = conversations.find(item => item.id === id);
    if (!next) return;
    setSelectedId(id);
    setHostTaskId(null);
    setPage(next.messages.length ? 'conversation' : 'home');
    setMobileOpen(false);
  };

  const selectPage = (next: Page) => {
    setPage(next);
    setHostTaskId(null);
    setMobileOpen(false);
  };

  const sendMessage = (text: string) => {
    const clean = text.trim();
    if (!clean) return;
    const firstLine = clean.split('\n')[0];
    const title = Array.from(firstLine).slice(0, 24).join('');
    setConversations(items => items.map(item => {
      if (item.id !== selectedId) return item;
      return {
        ...item,
        title: item.messages.length ? item.title : title,
        draft: '',
        messages: [...item.messages, { id: `local-${crypto.randomUUID()}`, role: 'user', content: [{ type: 'text', text: clean }] }],
        notice: '已保留在本地预览，未调用模型、未创建任务，也未启动 Agent。',
      };
    }));
    setPage('conversation');
  };

  const openSettings = (tab: SettingsTab = '通用') => {
    settingsReturnFocus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setSettingsTab(tab);
    setSettingsOpen(true);
  };
  const openWorkspace = (tab: WorkspaceTab) => {
    setWorkspaceTab(tab);
    updatePreferences({ inspector: true });
  };

  return (
    <div className={`app ${sidebarCollapsed ? 'sidebar-collapsed' : ''} ${preferences.compact ? 'compact' : ''}`} data-theme={preferences.theme}>
      <a className="skip" href="#main">跳到主内容</a>
      {mobileOpen && <button className="mobile-overlay" aria-label="关闭导航" onClick={() => setMobileOpen(false)} />}
      <div className="desktop">
        <Sidebar
          collapsed={sidebarCollapsed}
          mobileOpen={mobileOpen}
          page={page}
          conversations={conversations}
          selectedId={selectedId}
          hostTaskId={hostTaskId}
          snapshot={snapshot}
          query={query}
          moreOpen={moreOpen}
          onQuery={setQuery}
          onMore={() => setMoreOpen(value => !value)}
          onCollapse={() => setSidebarCollapsed(value => !value)}
          onQuickChat={() => quickChat()}
          onPage={selectPage}
          onConversation={selectConversation}
          onHostTask={id => { setHostTaskId(id); setPage('conversation'); setMobileOpen(false); }}
          onSettings={() => openSettings()}
        />
        <section className="main-shell">
          <header className="topbar">
            <div className="topbar-left"><Button size="icon" className="mobile-menu" aria-label="打开导航" onClick={() => setMobileOpen(true)}><Menu size={17} /></Button><span className="topbar-context">{hostTask ? '主机任务 · 只读' : page === 'home' ? '安全协作工作台' : page === 'conversation' ? '本地会话预览' : page === 'agents' ? 'Agent' : page === 'connections' ? '连接中心' : page === 'workflow' ? '运行模式' : '审批与成果'}</span></div>
            <div className="topbar-actions">
              <Button className="host-connection" onClick={() => openSettings('诊断')}><span className={`connection-dot ${snapshot.connection === 'connected' ? 'connected' : ''}`} />{snapshot.connection === 'connected' ? '主机已连接 · 只读' : '主机未连接'}</Button>
              {chatVisible && <Hint text={preferences.inspector ? '收起右侧工作区' : '展开右侧工作区'}><Button size="icon" className={preferences.inspector ? 'panel-active' : ''} aria-label={preferences.inspector ? '收起右侧工作区' : '展开右侧工作区'} aria-expanded={preferences.inspector} onClick={() => updatePreferences({ inspector: !preferences.inspector })}><PanelRightOpen size={16} /></Button></Hint>}
            </div>
          </header>
          {error && <div className="bridge-error" role="status">{error}</div>}
          <div className="main-body">
            <main id="main" className="main" tabIndex={-1}>
              {hostTask ? <HostConversation task={hostTask} snapshot={snapshot} /> : chatVisible ? (
                <Chat
                  key={current.id}
                  conversation={current}
                  home={page === 'home'}
                  onUpdate={updateCurrent}
                  onConfig={updateConfig}
                  onSend={sendMessage}
                  onOpenWorkflow={() => selectPage('workflow')}
                  onOpenArtifact={() => openWorkspace('artifacts')}
                />
              ) : <ManagementPage page={page} snapshot={snapshot} config={current.config} onConfig={updateConfig} onUseAgent={quickChat} actionReason={unavailable} />}
            </main>
            {chatVisible && preferences.inspector && <WorkspacePanel current={current} task={hostTask} snapshot={snapshot} tab={workspaceTab} onTab={setWorkspaceTab} onClose={() => updatePreferences({ inspector: false })} />}
          </div>
        </section>
      </div>
      <SettingsDialog open={settingsOpen} onOpenChange={setSettingsOpen} tab={settingsTab} onTab={setSettingsTab} snapshot={snapshot} preferences={preferences} onPreferences={updatePreferences} returnFocus={settingsReturnFocus.current} />
    </div>
  );
}
