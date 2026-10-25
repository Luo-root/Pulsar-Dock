import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import App from './App';
import { disconnectedSnapshot, parseSnapshot, type HostBridge } from './bridge';

const connected = {
  version: 1, connection: 'connected',
  tasks: [{ id: 'task-1', parentId: null, title: '合成测试任务', state: 'created', permissions: ['artifact.read'] }],
  events: [], artifacts: [],
};
const bridgeFor = (value: unknown): HostBridge => ({ getSnapshot: async () => value, subscribe: () => () => {} });

describe('Gold-Band style desktop chat', () => {
  it('opens on the greeting composer and sends only a clearly labelled local preview message', async () => {
    const user = userEvent.setup();
    render(<App />);
    expect(await screen.findByRole('button', { name: '主机未连接' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: /今天想一起做点什么|想一起做点什么/ })).toBeInTheDocument();
    expect(screen.queryByRole('complementary', { name: '右侧工作区' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: '发送到本地预览' })).toBeDisabled();
    expect(screen.getByRole('button', { name: '添加附件（未连接）' })).toBeDisabled();

    await user.type(screen.getByRole('textbox', { name: '消息草稿' }), '帮我整理代码');
    await user.click(screen.getByRole('button', { name: '发送到本地预览' }));
    expect(await screen.findByRole('status')).toHaveTextContent('未调用模型、未创建任务，也未启动 Agent');
    expect(screen.getByRole('heading', { name: '帮我整理代码' })).toBeInTheDocument();
    expect(screen.getAllByText('帮我整理代码').length).toBeGreaterThan(1);
    expect(screen.getByRole('textbox', { name: '消息草稿' })).toHaveValue('');
  });

  it('preserves an unsent draft when switching between conversations', async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.type(screen.getByRole('textbox', { name: '消息草稿' }), '保留的想法');
    await user.click(screen.getByRole('button', { name: /整理项目的错误处理 示例/ }));
    expect(screen.getByRole('heading', { name: '整理项目的错误处理' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: '新会话' }));
    expect(screen.getByRole('textbox', { name: '消息草稿' })).toHaveValue('保留的想法');
    await user.click(screen.getByRole('button', { name: '发送到本地预览' }));
    expect(await screen.findByRole('status')).toHaveTextContent('未调用模型');
  });

  it('creates a clean quick chat and filters local sessions', async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.type(screen.getByRole('textbox', { name: '消息草稿' }), '第一个会话{Enter}');
    expect(await screen.findByRole('status')).toHaveTextContent('未启动 Agent');
    await user.click(screen.getByRole('button', { name: '快速对话' }));
    expect(screen.getByRole('textbox', { name: '消息草稿' })).toHaveValue('');
    await user.type(screen.getByRole('textbox', { name: '搜索会话' }), '不存在');
    expect(screen.getByText('没有匹配的会话')).toBeInTheDocument();
    await user.clear(screen.getByRole('textbox', { name: '搜索会话' }));
    expect(screen.getByRole('button', { name: '第一个会话' })).toBeInTheDocument();
  });

  it('supports keyboard mode tabs and keeps workflow execution explicitly unwired', async () => {
    const user = userEvent.setup();
    render(<App />);
    const direct = screen.getByRole('tab', { name: 'Direct' });
    direct.focus();
    await user.keyboard('{ArrowRight}');
    expect(screen.getByRole('tab', { name: '工作流' })).toHaveFocus();
    expect(screen.getByRole('tab', { name: '工作流' })).toHaveAttribute('aria-selected', 'true');
    await user.click(screen.getByRole('button', { name: /查看工作流/ }));
    expect(screen.getByRole('heading', { name: '运行模式' })).toBeInTheDocument();
    const managementWorkflow = screen.getByRole('tab', { name: '工作流' });
    expect(managementWorkflow).toHaveAttribute('aria-selected', 'true');
    managementWorkflow.focus();
    await user.keyboard('{ArrowLeft}');
    expect(screen.getByRole('tab', { name: 'Direct' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('静态 UI · 未接执行引擎')).toBeInTheDocument();
    expect(screen.getByText('后续执行层')).toBeInTheDocument();
    expect(screen.getByText(/本轮没有引入、配置或验证这两个执行库/)).toBeInTheDocument();
  });

  it('opens the example artifact and collaborator previews without implying execution', async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole('button', { name: /整理项目的错误处理 示例/ }));
    await user.click(screen.getByText('协作者与独立审查'));
    expect(screen.getByText(/没有创建任何子环境/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /统一错误响应方案/ }));
    expect(screen.getByRole('complementary', { name: '右侧工作区' })).toHaveTextContent('docs/error-response.md');
    expect(screen.getByText(/这是静态示例差异/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '应用成果' })).toBeDisabled();
  });

  it('supports sidebar collapse and restore with an accessible control', async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole('button', { name: '收起侧边栏' }));
    expect(screen.getByRole('button', { name: '展开侧边栏' })).toHaveAttribute('aria-expanded', 'false');
    expect(document.querySelector('.sidebar')).toHaveClass('is-collapsed');
    await user.click(screen.getByRole('button', { name: '展开侧边栏' }));
    expect(document.querySelector('.sidebar')).not.toHaveClass('is-collapsed');
  });

  it('keeps settings keyboard focus and persists actual appearance choices', async () => {
    const user = userEvent.setup();
    render(<App />);
    const trigger = screen.getByRole('button', { name: '设置' });
    trigger.focus();
    await user.keyboard('{Enter}');
    const dialog = screen.getByRole('dialog', { name: '设置' });
    expect(dialog).toContainElement(document.activeElement as HTMLElement);
    await user.click(screen.getByRole('button', { name: '外观' }));
    await user.selectOptions(screen.getByLabelText('主题'), 'dark');
    expect(document.querySelector('.app')).toHaveAttribute('data-theme', 'dark');
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark');
    await user.click(screen.getByRole('checkbox', { name: '紧凑消息间距' }));
    await user.click(screen.getByRole('checkbox', { name: '默认展开右侧工作区' }));
    expect(JSON.parse(localStorage.getItem('pulsar-dock.preferences')!)).toEqual({ theme: 'dark', inspector: true, compact: true });
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
    expect(screen.getByRole('complementary', { name: '右侧工作区' })).toBeInTheDocument();
  });

  it('uses local-only model and approval controls without changing real policy', async () => {
    const user = userEvent.setup();
    render(<App />);
    const selector = screen.getByRole('button', { name: '权限：标准模式' });
    selector.focus();
    await user.keyboard('{Enter}');
    expect(screen.getByRole('menu')).toHaveTextContent('权限 · 本地预览');
    await user.click(screen.getByRole('menuitemradio', { name: '逐项确认' }));
    expect(screen.getByRole('button', { name: '权限：逐项确认' })).toBeInTheDocument();
    expect(screen.getByText('本地 UI 预览 · 不调用模型、不创建任务')).toBeInTheDocument();
  });

  it('navigates Agent and connection pages without enabling unavailable operations', async () => {
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByRole('button', { name: 'Agent' }));
    expect(screen.getByRole('heading', { name: 'Agent' })).toBeInTheDocument();
    await user.click(screen.getAllByRole('button', { name: /用于本地预览/ })[1]);
    expect(screen.getByRole('button', { name: 'Codex' })).toHaveAttribute('aria-pressed', 'true');
    await user.click(screen.getByRole('button', { name: '连接中心' }));
    expect(screen.getByRole('heading', { name: '连接中心' })).toBeInTheDocument();
    for (const button of screen.getAllByRole('button', { name: '检查连接' })) expect(button).toBeDisabled();
    expect(screen.getByText(/未接入可信操作桥/)).toBeInTheDocument();
  });

  it('shows hostile host and local message content only as text', async () => {
    const user = userEvent.setup();
    const hostile = '<img src=x onerror="alert(1)"> javascript:alert(2)';
    render(<App bridge={bridgeFor({ ...connected, tasks: [{ ...connected.tasks[0], title: hostile }], events: [{ id: 'event-1', taskId: 'task-1', summary: hostile }] })} />);
    await user.click(await screen.findByRole('button', { name: hostile }));
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(hostile);
    expect(screen.getAllByText(hostile).length).toBeGreaterThan(1);
    await user.click(screen.getByRole('button', { name: '快速对话' }));
    await user.type(screen.getByRole('textbox', { name: '消息草稿' }), hostile);
    await user.click(screen.getByRole('button', { name: '发送到本地预览' }));
    expect(document.querySelector('img')).toBeNull();
    expect(document.querySelector('a[href^="javascript:"]')).toBeNull();
    expect(await screen.findByRole('status')).toHaveTextContent('未启动 Agent');
  });

  it('keeps real artifacts and permissions read-only', async () => {
    const user = userEvent.setup();
    const value = { ...connected, artifacts: [{ id: 'artifact-1', taskId: 'task-1', name: 'synthetic.diff', digest: 'a'.repeat(64) }] };
    render(<App bridge={bridgeFor(value)} />);
    await user.click(await screen.findByRole('button', { name: /合成测试任务/ }));
    await user.click(screen.getByRole('button', { name: '展开右侧工作区' }));
    const resourceTab = screen.getByRole('tab', { name: '资源' });
    resourceTab.focus();
    await user.keyboard('{ArrowRight}');
    expect(screen.getByRole('tab', { name: '成果' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('synthetic.diff')).toBeInTheDocument();
    expect(screen.getByText(`SHA-256 ${'a'.repeat(64)}`)).toBeInTheDocument();
    await user.click(screen.getByRole('tab', { name: '权限' }));
    expect(screen.getByRole('button', { name: '撤销权限' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: '更多' }));
    await user.click(screen.getByRole('button', { name: /审批与成果/ }));
    expect(screen.getByRole('button', { name: '应用成果' })).toBeDisabled();
  });

  it('uses the late-night greeting before 05:00 instead of the dawn greeting', () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date('2026-10-25T04:00:00'));
      render(<App />);
      expect(screen.getByRole('heading', { name: '夜深了，想一起做点什么？' })).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it('rejects malformed host snapshots and removes previously displayed data', async () => {
    let emit: (value: unknown) => void = () => {};
    const unsubscribe = vi.fn();
    const bridge: HostBridge = { getSnapshot: async () => connected, subscribe: listener => { emit = listener; return unsubscribe; } };
    const view = render(<App bridge={bridge} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: /合成测试任务/ }));
    act(() => emit({ ...connected, tasks: [{ ...connected.tasks[0], state: 'magic-success' }] }));
    expect(screen.getByRole('status')).toHaveTextContent('主机状态格式无效');
    expect(screen.queryByRole('button', { name: /合成测试任务/ })).not.toBeInTheDocument();
    expect(screen.getByRole('heading', { name: /今天想一起做点什么|想一起做点什么/ })).toBeInTheDocument();
    view.unmount();
    expect(unsubscribe).toHaveBeenCalledOnce();
  });

  it('does not overwrite newer subscription data with a late initial read', async () => {
    let resolve: (value: unknown) => void = () => {};
    let emit: (value: unknown) => void = () => {};
    render(<App bridge={{ getSnapshot: () => new Promise(r => { resolve = r; }), subscribe: listener => { emit = listener; return () => {}; } }} />);
    act(() => emit(connected));
    await act(async () => resolve(disconnectedSnapshot));
    expect(screen.getByRole('button', { name: '主机已连接 · 只读' })).toBeInTheDocument();
  });

  it('explains read errors without exposing exception details', async () => {
    render(<App bridge={{ getSnapshot: async () => { throw new Error('synthetic-private-value'); }, subscribe: () => () => {} }} />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('无法读取主机状态'));
    expect(screen.queryByText(/synthetic-private-value/)).not.toBeInTheDocument();
  });
});

describe('snapshot boundary', () => {
  it('copies and freezes valid records', () => {
    const input = structuredClone(connected);
    const parsed = parseSnapshot(input);
    input.tasks[0].title = 'changed';
    expect(parsed.tasks[0].title).toBe('合成测试任务');
    expect(Object.isFrozen(parsed.tasks[0].permissions)).toBe(true);
  });

  it.each([
    null, {}, { ...connected, version: 2 }, { ...connected, connection: { toString: () => 'connected' } },
    { ...connected, connection: 'disconnected' },
    { ...connected, tasks: [{ ...connected.tasks[0], permissions: [42] }] },
    { ...connected, tasks: [connected.tasks[0], connected.tasks[0]] },
    { ...connected, tasks: [{ ...connected.tasks[0], parentId: 'task-1' }] },
    { ...connected, events: [{ id: 'event-1', taskId: 'unknown', summary: 'bad reference' }] },
    { ...connected, artifacts: [{ id: 'a', taskId: 'task-1', name: 'file', digest: 'not-a-digest' }] },
  ])('rejects invalid data %#', input => {
    expect(() => parseSnapshot(input)).toThrow('主机状态格式无效');
  });
});
