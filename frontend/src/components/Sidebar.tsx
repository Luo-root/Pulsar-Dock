import type { KeyboardEvent, RefObject } from 'react';
import {
  Bot, ChevronDown, FolderKanban, GitBranch, MessageSquare, PanelLeft, Route,
  Search, Settings, ShieldCheck, Sparkles,
} from 'lucide-react';
import type { Snapshot } from '../bridge';
import type { Conversation, Page } from '../types';
import { Button, Hint, PulsarMark } from './ui';

interface SidebarProps {
  collapsed: boolean;
  mobile: boolean;
  mobileOpen: boolean;
  sidebarRef: RefObject<HTMLElement | null>;
  page: Page;
  conversations: Conversation[];
  selectedId: string;
  hostTaskId: string | null;
  snapshot: Snapshot;
  onCollapse: () => void;
  onQuickChat: () => void;
  onSearch: () => void;
  onPage: (page: Page) => void;
  onConversation: (id: string) => void;
  onHostTask: (id: string) => void;
  onSettings: () => void;
}

const mainLinks = [
  { page: 'agents' as const, label: 'Agent', icon: Bot },
  { page: 'workflow' as const, label: '运行模式', icon: Route },
];

export function Sidebar(props: SidebarProps) {
  const {
    collapsed, mobile, mobileOpen, sidebarRef, page, conversations, selectedId,
    hostTaskId, snapshot, onCollapse, onQuickChat, onSearch, onPage,
    onConversation, onHostTask, onSettings,
  } = props;

  const trapFocus = (event: KeyboardEvent<HTMLElement>) => {
    if (!mobile || !mobileOpen || event.key !== 'Tab') return;
    const focusable = Array.from(sidebarRef.current?.querySelectorAll<HTMLElement>('button:not(:disabled), input, [href], [tabindex="0"]') ?? []);
    const first = focusable[0];
    const last = focusable.at(-1);
    if (!first || !last) return;
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  };

  return (
    <aside
      ref={sidebarRef}
      className={`sidebar ${collapsed ? 'is-collapsed' : ''} ${mobileOpen ? 'is-mobile-open' : ''}`}
      aria-label="项目与会话"
      aria-hidden={mobile && !mobileOpen ? true : undefined}
      aria-modal={mobile ? true : undefined}
      role={mobile ? 'dialog' : undefined}
      inert={mobile && !mobileOpen}
      onKeyDown={trapFocus}
    >
      <div className="brand-row">
        <Button className="brand-home" aria-label="Pulsar Dock 首页" onClick={onQuickChat}>
          <span className="brand-mark"><PulsarMark /></span>
          <strong>Pulsar Dock</strong>
        </Button>
        <Hint text={mobile ? '关闭导航' : collapsed ? '展开侧边栏' : '收起侧边栏'}>
          <Button size="icon" className="sidebar-toggle" aria-label={mobile ? '关闭导航' : collapsed ? '展开侧边栏' : '收起侧边栏'} aria-expanded={mobile ? mobileOpen : !collapsed} onClick={onCollapse}>
            <PanelLeft size={16} />
          </Button>
        </Hint>
      </div>

      <nav className="primary-nav" aria-label="主导航">
        <Button className={`nav-item ${page === 'home' && !hostTaskId ? 'active' : ''}`} aria-label="快速对话" aria-current={page === 'home' && !hostTaskId ? 'page' : undefined} onClick={onQuickChat}>
          <MessageSquare size={16} /><span>快速对话</span>
        </Button>
        <Button className="nav-item" aria-label="搜索会话" onClick={onSearch}>
          <Search size={16} /><span>搜索会话</span>
        </Button>
        {mainLinks.map(({ page: target, label, icon: NavIcon }) => (
          <Button key={target} className={`nav-item ${page === target && !hostTaskId ? 'active' : ''}`} aria-label={label} aria-current={page === target && !hostTaskId ? 'page' : undefined} onClick={() => onPage(target)}>
            <NavIcon size={16} /><span>{label}</span>
          </Button>
        ))}
        <Button className={`nav-item ${page === 'artifacts' && !hostTaskId ? 'active' : ''}`} aria-label="审批与成果" aria-current={page === 'artifacts' && !hostTaskId ? 'page' : undefined} onClick={() => onPage('artifacts')}>
          <ShieldCheck size={16} /><span>审批与成果</span><span className="nav-count">{snapshot.artifacts.length}</span>
        </Button>
      </nav>

      <div className="sidebar-scroll">
        <div className="workspace-heading">
          <ChevronDown size={13} /><FolderKanban size={14} /><span>本地预览</span><span className="preview-badge">UI</span>
        </div>
        <nav className="session-list" aria-label="本地预览会话">
          {conversations.map(item => (
            <Button key={item.id} className={`session-item ${selectedId === item.id && !hostTaskId && (page === 'home' || page === 'conversation') ? 'active' : ''}`} onClick={() => onConversation(item.id)}>
              {item.demo ? <Sparkles size={14} /> : <MessageSquare size={14} />}
              <span className="session-title">{item.title}</span>
              {item.demo && <span className="session-demo">示例</span>}
            </Button>
          ))}
        </nav>
        {snapshot.tasks.length > 0 && (
          <>
            <div className="workspace-heading host-heading"><ChevronDown size={13} /><GitBranch size={14} /><span>主机任务</span><span className="readonly-label">只读</span></div>
            <nav className="session-list" aria-label="主机任务">
              {snapshot.tasks.map(item => (
                <Button key={item.id} className={`session-item ${hostTaskId === item.id ? 'active' : ''}`} onClick={() => onHostTask(item.id)}>
                  <span className={`task-dot state-${item.state}`} /><span className="session-title">{item.title}</span>
                </Button>
              ))}
            </nav>
          </>
        )}
      </div>

      <div className="sidebar-footer">
        <Button className="nav-item settings-entry" aria-label="设置" onClick={onSettings}><Settings size={16} /><span>设置</span></Button>
      </div>
    </aside>
  );
}
