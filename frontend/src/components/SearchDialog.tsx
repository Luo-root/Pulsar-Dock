import { useRef } from 'react';
import { GitBranch, MessageSquare, Search, X } from 'lucide-react';
import type { Snapshot } from '../bridge';
import type { Conversation } from '../types';
import { stateLabels } from './Pages';
import { Button, Dialog, DialogClose, DialogContent, DialogDescription, DialogTitle } from './ui';

export function SearchDialog({ open, onOpenChange, query, onQuery, conversations, snapshot, onConversation, onHostTask, returnFocus }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  query: string;
  onQuery: (query: string) => void;
  conversations: Conversation[];
  snapshot: Snapshot;
  onConversation: (id: string) => void;
  onHostTask: (id: string) => void;
  returnFocus: HTMLElement | null;
}) {
  const inputRef = useRef<HTMLInputElement>(null);
  const normalized = query.trim().toLocaleLowerCase();
  const localResults = conversations.filter(item => item.title.toLocaleLowerCase().includes(normalized)).slice(0, 20);
  const hostResults = snapshot.tasks.filter(item => item.title.toLocaleLowerCase().includes(normalized)).slice(0, 20);
  const empty = localResults.length + hostResults.length === 0;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="search-dialog" onOpenAutoFocus={event => { event.preventDefault(); inputRef.current?.focus(); }} onCloseAutoFocus={event => {
        if (!returnFocus) return;
        event.preventDefault();
        returnFocus.focus();
      }}>
        <div className="settings-heading"><DialogTitle>搜索会话</DialogTitle><DialogClose asChild><Button size="icon" aria-label="关闭搜索"><X size={16} /></Button></DialogClose></div>
        <DialogDescription>搜索本地预览与主机任务。选择结果只切换页面，不执行任何操作。</DialogDescription>
        <label className="global-search-input"><Search size={17} /><input ref={inputRef} aria-label="搜索会话内容" placeholder="输入会话或任务名称…" value={query} onChange={event => onQuery(event.target.value)} /></label>
        <div className="search-results" aria-live="polite">
          {empty && <div className="search-empty-state"><Search size={22} /><p>没有匹配的会话</p><small>试试其他名称，或清空搜索查看全部。</small></div>}
          {localResults.length > 0 && <section><h2>本地预览</h2><ul>{localResults.map(item => <li key={item.id}><Button onClick={() => { onConversation(item.id); onOpenChange(false); }}><MessageSquare size={15} /><span>{item.title}</span><small>{item.demo ? '示例' : '本地预览'}</small></Button></li>)}</ul></section>}
          {hostResults.length > 0 && <section><h2>主机任务 · 只读</h2><ul>{hostResults.map(item => <li key={item.id}><Button onClick={() => { onHostTask(item.id); onOpenChange(false); }}><GitBranch size={15} /><span>{item.title}</span><small>{stateLabels[item.state]}</small></Button></li>)}</ul></section>}
        </div>
      </DialogContent>
    </Dialog>
  );
}
