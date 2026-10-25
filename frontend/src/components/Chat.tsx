import { useEffect, useRef, useState } from 'react';
import { AssistantRuntimeProvider, ComposerPrimitive, MessagePrimitive, ThreadPrimitive, useExternalStoreRuntime, type ThreadMessageLike } from '@assistant-ui/react';
import {
  ArrowUpRight, Bot, ChevronDown, FileText, Folder, GitBranch, Paperclip,
  Route, Send, ShieldCheck, UserRound, Users, Wrench,
} from 'lucide-react';
import { agentOptions } from '../data';
import type { Conversation, RunMode, SessionConfig } from '../types';
import { BrandIcon } from './BrandIcon';
import { Button, Choice, Hint } from './ui';

function UserMessage() {
  return (
    <MessagePrimitive.Root className="message user-message">
      <div className="user-message-body"><div className="user-bubble"><MessagePrimitive.Parts /></div><span className="message-time">本地预览</span></div>
      <span className="message-avatar"><UserRound size={16} /></span>
    </MessagePrimitive.Root>
  );
}

function AssistantMessage() {
  return (
    <MessagePrimitive.Root className="message assistant-message">
      <span className="message-avatar assistant-avatar"><Bot size={16} /></span>
      <div className="assistant-body"><div className="message-text"><MessagePrimitive.Parts /></div><span className="message-time">Pulsar · 示例内容</span></div>
    </MessagePrimitive.Root>
  );
}

const greetingText = {
  dawn: '早上好，今天想一起做点什么？',
  morning: '上午好，今天想一起做点什么？',
  noon: '中午好，今天想一起做点什么？',
  afternoon: '下午好，今天想一起做点什么？',
  evening: '晚上好，今天想一起做点什么？',
  late: '夜深了，想一起做点什么？',
};

type GreetingPeriod = keyof typeof greetingText;
const boundaries: [number, GreetingPeriod][] = [[300, 'dawn'], [540, 'morning'], [690, 'noon'], [840, 'afternoon'], [1110, 'evening'], [1410, 'late']];

function periodAt(date: Date): GreetingPeriod {
  const minute = date.getHours() * 60 + date.getMinutes();
  let period: GreetingPeriod = 'late';
  for (const [start, candidate] of boundaries) {
    if (minute < start) break;
    period = candidate;
  }
  return period;
}

function untilNextPeriod(date: Date): number {
  const minute = date.getHours() * 60 + date.getMinutes() + date.getSeconds() / 60;
  const next = boundaries.find(([start]) => start > minute)?.[0] ?? 1740;
  return Math.max(1000, Math.round((next - minute) * 60_000) + 50);
}

function ConversationGreeting() {
  const [period, setPeriod] = useState<GreetingPeriod>(() => periodAt(new Date()));
  useEffect(() => {
    let timer = 0;
    const refresh = () => {
      const now = new Date();
      setPeriod(periodAt(now));
      window.clearTimeout(timer);
      timer = window.setTimeout(refresh, untilNextPeriod(now));
    };
    const visible = () => { if (document.visibilityState === 'visible') refresh(); };
    refresh();
    window.addEventListener('focus', refresh);
    document.addEventListener('visibilitychange', visible);
    return () => {
      window.clearTimeout(timer);
      window.removeEventListener('focus', refresh);
      document.removeEventListener('visibilitychange', visible);
    };
  }, []);
  return <h1 className="home-greeting">{greetingText[period]}</h1>;
}

const modeLabels: { value: RunMode; label: string }[] = [
  { value: 'direct', label: '对话' },
  { value: 'workflow', label: '工作流' },
  { value: 'auto', label: 'AUTO' },
];

function Composer({ conversation, home, onUpdate, onConfig, onOpenWorkflow }: {
  conversation: Conversation;
  home: boolean;
  onUpdate: (patch: Partial<Conversation>) => void;
  onConfig: (patch: Partial<SessionConfig>) => void;
  onOpenWorkflow: () => void;
}) {
  const modeRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const config = conversation.config;
  const selectMode = (mode: RunMode) => onConfig({ mode });
  const onModeKey = (event: React.KeyboardEvent, index: number) => {
    let next = index;
    if (event.key === 'ArrowRight') next = (index + 1) % modeLabels.length;
    else if (event.key === 'ArrowLeft') next = (index + modeLabels.length - 1) % modeLabels.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = modeLabels.length - 1;
    else return;
    event.preventDefault();
    selectMode(modeLabels[next].value);
    modeRefs.current[next]?.focus();
  };

  return (
    <div className={`composer-group ${home ? 'is-home' : 'is-followup'}`}>
      {home && (
        <div className="composer-context-bar" aria-label="预览工作区">
          <Folder size={14} /><span>本地预览工作区</span>
          <span className="context-divider" />
          <GitBranch size={14} /><span>主工作区</span><ChevronDown size={12} />
        </div>
      )}
      <ComposerPrimitive.Root className="composer">
        <ComposerPrimitive.Input
          aria-label="消息草稿"
          placeholder="描述一个想法，在本地预览对话界面…"
          className="composer-input"
          value={conversation.draft}
          onChange={event => onUpdate({ draft: event.target.value })}
        />
        <div className="composer-toolbar">
          <Hint text="附件需要可信桌面操作桥">
            <span className="disabled-control"><Button size="icon" aria-label="添加附件（未连接）" disabled><Paperclip size={16} /></Button></span>
          </Hint>
          <div className="composer-options">
            <Choice label="模型" value={config.model} options={['未连接模型', '示例模型（不调用）']} onChange={model => onConfig({ model })} />
            <Choice label="权限" value={config.approval} options={['标准模式', '逐项确认', '仅讨论']} onChange={approval => onConfig({ approval })} />
            <Hint text="只生成本地预览消息，不调用模型或 Agent">
              <ComposerPrimitive.Send asChild>
                <Button variant="primary" className="send-button" aria-label="发送到本地预览"><Send size={14} /><span>发送</span></Button>
              </ComposerPrimitive.Send>
            </Hint>
          </div>
        </div>
      </ComposerPrimitive.Root>
      {home && (
        <>
          <div className="mode-row">
            <span className="control-label">处理模式</span>
            <div className="mode-tabs" role="tablist" aria-label="处理模式">
              {modeLabels.map((mode, index) => (
                <button
                  key={mode.value}
                  ref={element => { modeRefs.current[index] = element; }}
                  id={`mode-${mode.value}`}
                  type="button"
                  role="tab"
                  aria-selected={config.mode === mode.value}
                  aria-controls="mode-panel"
                  tabIndex={config.mode === mode.value ? 0 : -1}
                  onClick={() => selectMode(mode.value)}
                  onKeyDown={event => onModeKey(event, index)}
                >{mode.label}</button>
              ))}
            </div>
          </div>
          <div className="mode-panel" id="mode-panel" role="tabpanel" aria-labelledby={`mode-${config.mode}`}>
            {config.mode === 'direct' && (
              <div className="agent-row-select"><span className="control-label">选择 Agent</span><div className="agent-options">
                {agentOptions.map(({ name: agent }) => (
                  <Button key={agent} className={`agent-pill ${config.agent === agent ? 'selected' : ''}`} aria-pressed={config.agent === agent} onClick={() => onConfig({ agent })}>
                    <BrandIcon name={agent} size={16} /><span>{agent}</span>
                  </Button>
                ))}
              </div></div>
            )}
            {config.mode === 'workflow' && (
              <div className="workflow-picker"><Route size={16} /><Choice label="工作流模板" value={config.workflow} options={['需求 → 实现 → 审查 → 交付', '轻量实现 → 独立审查']} onChange={workflow => onConfig({ workflow })} /><Button onClick={onOpenWorkflow}>查看工作流 <ArrowUpRight size={13} /></Button></div>
            )}
            {config.mode === 'auto' && (
              <div className="auto-config"><span className="control-label">全局 Goal</span><input aria-label="全局 Goal（本地预览）" value={config.goal} onChange={event => onConfig({ goal: event.target.value })} placeholder="可选，仅用于界面预览" /><span className="preview-badge">未启用自动路由</span></div>
            )}
          </div>
        </>
      )}
      <div className="composer-caption"><span>本地 UI 预览 · 不调用模型、不创建任务</span><span>Enter 发送 · Shift + Enter 换行</span></div>
    </div>
  );
}

export function Chat({ conversation, home, onUpdate, onConfig, onSend, onOpenWorkflow, onOpenArtifact }: {
  conversation: Conversation;
  home: boolean;
  onUpdate: (patch: Partial<Conversation>) => void;
  onConfig: (patch: Partial<SessionConfig>) => void;
  onSend: (text: string) => void;
  onOpenWorkflow: () => void;
  onOpenArtifact: () => void;
}) {
  const runtime = useExternalStoreRuntime({
    messages: conversation.messages,
    isRunning: false,
    convertMessage: (message: ThreadMessageLike) => message,
    onNew: async message => {
      const text = message.content.filter(part => part.type === 'text').map(part => part.text).join('\n');
      if (text.trim()) onSend(text);
    },
  });
  useEffect(() => { runtime.thread.composer.setText(conversation.draft); }, [runtime, conversation.draft]);

  const composer = <Composer conversation={conversation} home={home} onUpdate={onUpdate} onConfig={onConfig} onOpenWorkflow={onOpenWorkflow} />;

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <ThreadPrimitive.Root className={`thread ${home ? 'home-thread' : 'conversation-thread'}`}>
        {home ? (
          <div className="home-viewport"><div className="home-content"><ConversationGreeting />{composer}</div></div>
        ) : (
          <>
            <header className="conversation-header">
              <div className="conversation-heading"><h1>{conversation.title}</h1><span className="agent-identity"><BrandIcon name={conversation.config.agent} size={14} />{conversation.config.agent}</span><span className="session-id">{conversation.demo ? 'example-session' : 'local-draft'}</span></div>
              <span className="preview-badge">交互预览 · 未执行</span>
            </header>
            <ThreadPrimitive.Viewport className="thread-viewport">
              <div className="thread-content">
                {conversation.demo && <div className="preview-line"><span className="preview-badge">示例会话</span><span>所有消息、工具与成果均为静态示例，不代表真实运行。</span></div>}
                <ThreadPrimitive.Messages components={{ UserMessage, AssistantMessage }} />
                {conversation.demo && (
                  <div className="demo-extras">
                    <details className="tool-preview"><summary><Wrench size={14} />工具调用预览<span>未执行</span><ChevronDown size={13} /></summary><p>计划阅读 API 响应定义。此示例没有访问文件或调用工具。</p></details>
                    <details className="tool-preview"><summary><Users size={14} />协作者与独立审查<span>静态示意</span><ChevronDown size={13} /></summary><p>主 Agent、reviewer 与 verifier 只展示未来协作界面，没有创建任何子环境。</p></details>
                    <button className="artifact-card" onClick={onOpenArtifact}><span className="artifact-file-icon"><FileText size={21} /></span><span><strong>统一错误响应方案</strong><small>示例成果 · 查看差异预览</small></span><ArrowUpRight size={16} /></button>
                  </div>
                )}
                {conversation.notice && <div className="local-notice" role="status"><ShieldCheck size={16} /><span>{conversation.notice}</span></div>}
              </div>
            </ThreadPrimitive.Viewport>
            <div className="composer-dock">{composer}</div>
          </>
        )}
      </ThreadPrimitive.Root>
    </AssistantRuntimeProvider>
  );
}
