import {
  Bot, Boxes, ChevronDown, ChevronRight, Ellipsis, FolderKanban, GitBranch,
  Library, MessageSquare, PanelLeft, Route, Search, Settings, ShieldCheck, Sparkles,
} from 'lucide-react';
import type { Snapshot } from '../bridge';
import type { Conversation, Page } from '../types';
import { Button, Hint, PulsarMark } from './ui';

interface SidebarProps {
  collapsed: boolean;
  mobileOpen: boolean;
  page: Page;
  conversations: Conversation[];
  selectedId: string;
  hostTaskId: string | null;
  snapshot: Snapshot;
  query: string;
  moreOpen: boolean;
  onQuery: (value: string) => void;
  onMore: () => void;
  onCollapse: () => void;
  onQuickChat: () => void;
  onPage: (page: Page) => void;
  onConversation: (id: string) => void;
  onHostTask: (id: string) => void;
  onSettings: () => void;
}

const mainLinks = [
  { page: 'agents' as const, label: 'Agent', icon: Bot },
  { page: 'connections' as const, label: '连接中心', icon: Library },
  { page: 'workflow' as const, label: '运行模式', icon: Route },
];

export function Sidebar(props: SidebarProps) {
  const {
    collapsed, mobileOpen, page, conversations, selectedId, hostTaskId, snapshot,
    query, moreOpen, onQuery, onMore, onCollapse, onQuickChat, onPage,
    onConversation, onHostTask, onSettings,
  } = props;
  const filtered = conversations.filter(item => item.title.toLocaleLowerCase().includes(query.toLocaleLowerCase()));
  const hostTasks = snapshot.tasks.filter(item => item.title.toLocaleLowerCase().includes(query.toLocaleLowerCase()));

  return (
    <aside className={`sidebar ${collapsed ? 'is-collapsed' : ''} ${mobileOpen ? 'is-mobile-open' : ''}`} aria-label="项目与会话">
      <div className="brand-row">
        <Button className="brand-home" aria-label="Pulsar Dock 首页" onClick={onQuickChat}>
          <span className="brand-mark"><PulsarMark /></span>
          <strong>Pulsar Dock</strong>
        </Button>
        <Hint text={collapsed ? '展开侧边栏' : '收起侧边栏'}>
          <Button size="icon" className="sidebar-toggle" aria-label={collapsed ? '展开侧边栏' : '收起侧边栏'} aria-expanded={!collapsed} onClick={onCollapse}>
            <PanelLeft size={16} />
          </Button>
        </Hint>
      </div>

      <nav className="primary-nav" aria-label="主导航">
        <Button className={`nav-item ${page === 'home' && !hostTaskId ? 'active' : ''}`} aria-current={page === 'home' && !hostTaskId ? 'page' : undefined} onClick={onQuickChat}>
          <MessageSquare size={16} /><span>快速对话</span>
        </Button>
        {mainLinks.map(({ page: target, label, icon: NavIcon }) => (
          <Button key={target} className={`nav-item ${page === target && !hostTaskId ? 'active' : ''}`} aria-current={page === target && !hostTaskId ? 'page' : undefined} onClick={() => onPage(target)}>
            <NavIcon size={16} /><span>{label}</span>
          </Button>
        ))}
        <Button className={`nav-item more-trigger ${moreOpen ? 'open' : ''}`} aria-expanded={moreOpen} onClick={onMore}>
          <Ellipsis size={16} /><span>更多</span><ChevronDown className="more-chevron" size={14} />
        </Button>
        {moreOpen && (
          <div className="more-links">
            <Button className={`nav-item ${page === 'artifacts' && !hostTaskId ? 'active' : ''}`} onClick={() => onPage('artifacts')}>
              <ShieldCheck size={16} /><span>审批与成果</span><span className="nav-count">{snapshot.artifacts.length}</span>
            </Button>
          </div>
        )}
      </nav>

      <div className="sidebar-scroll">
        <label className="session-search">
          <Search size={14} />
          <input aria-label="搜索会话" placeholder="搜索会话" value={query} onChange={event => onQuery(event.target.value)} />
        </label>
        <div className="workspace-heading">
          <ChevronDown size={13} /><FolderKanban size={14} /><span>本地预览</span><span className="preview-badge">UI</span>
        </div>
        <nav className="session-list" aria-label="本地预览会话">
          {filtered.map(item => (
            <Button key={item.id} className={`session-item ${selectedId === item.id && !hostTaskId && (page === 'home' || page === 'conversation') ? 'active' : ''}`} onClick={() => onConversation(item.id)}>
              {item.demo ? <Sparkles size={14} /> : <MessageSquare size={14} />}
              <span className="session-title">{item.title}</span>
              {item.demo && <span className="session-demo">示例</span>}
            </Button>
          ))}
          {!filtered.length && <p className="search-empty">没有匹配的会话</p>}
        </nav>
        {hostTasks.length > 0 && (
          <>
            <div className="workspace-heading host-heading"><ChevronDown size={13} /><GitBranch size={14} /><span>主机任务</span><span className="readonly-label">只读</span></div>
            <nav className="session-list" aria-label="主机任务">
              {hostTasks.map(item => (
                <Button key={item.id} className={`session-item ${hostTaskId === item.id ? 'active' : ''}`} onClick={() => onHostTask(item.id)}>
                  <span className={`task-dot state-${item.state}`} /><span className="session-title">{item.title}</span>
                </Button>
              ))}
            </nav>
          </>
        )}
      </div>

      <div className="sidebar-footer">
        <div className="sidebar-boundary"><Boxes size={14} /><span>本地界面预览<small>不会自动执行 Agent</small></span></div>
        <Button className="nav-item settings-entry" onClick={onSettings}><Settings size={16} /><span>设置</span><ChevronRight className="settings-chevron" size={13} /></Button>
      </div>
    </aside>
  );
}
