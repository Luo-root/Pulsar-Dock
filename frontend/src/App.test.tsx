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

describe('workbench', () => {
  it('starts disconnected with no invented tasks or enabled dangerous actions', async () => {
    render(<App />);
    expect(await screen.findByText('主机未连接')).toBeInTheDocument();
    expect(screen.getByText('还没有任务')).toBeInTheDocument();
    for (const name of ['＋ 创建任务', '启动', '停止', '撤销权限']) expect(screen.getByRole('button', { name })).toBeDisabled();
    expect(screen.getByRole('contentinfo')).toHaveTextContent(/任务 0\s*\/\s*成果 0/);
    expect(screen.getAllByText(/未接入可信操作桥/).length).toBeGreaterThan(0);
  });
  it('navigates each page without claiming unimplemented operations', async () => {
    const user = userEvent.setup(); render(<App />);
    await user.click(screen.getByRole('button', { name: '连接' }));
    expect(screen.getByRole('heading', { name: '可信桌面连接' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '检查连接' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: '成果' }));
    expect(screen.getByRole('button', { name: '应用成果' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: '设置' }));
    await user.selectOptions(screen.getByLabelText('主题'), 'dark');
    expect(document.querySelector('.app')).toHaveAttribute('data-theme', 'dark');
    await user.click(screen.getByRole('checkbox', { name: '显示权限与成果检查器' }));
    expect(screen.queryByRole('complementary', { name: '权限与成果检查器' })).not.toBeInTheDocument();
    expect(JSON.parse(localStorage.getItem('pulsar-dock.preferences')!)).toEqual({ theme: 'dark', inspector: false });
    await user.click(screen.getByRole('button', { name: '工作台' }));
    await user.click(screen.getByRole('button', { name: '折叠任务树' }));
    expect(screen.getByRole('button', { name: '展开任务树' })).toHaveAttribute('aria-expanded', 'false');
  });
  it('supports keyboard tabs and displays hostile content only as text', async () => {
    const user = userEvent.setup();
    const hostile = '<img src=x onerror="alert(1)"> javascript:alert(2)';
    render(<App bridge={bridgeFor({ ...connected, tasks: [{ ...connected.tasks[0], title: hostile }], events: [{ id: 'event-1', taskId: 'task-1', summary: hostile }] })} />);
    await user.click(await screen.findByRole('button', { name: /权限轨道/ }));
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(hostile);
    const statusTab = screen.getByRole('tab', { name: '状态' });
    statusTab.focus(); await user.keyboard('{ArrowRight}');
    expect(screen.getByRole('tab', { name: '事件' })).toHaveFocus();
    expect(screen.getByRole('tabpanel')).toHaveTextContent(hostile);
    expect(document.querySelector('img')).toBeNull();
    expect(document.querySelector('a[href^="javascript:"]')).toBeNull();
    expect(screen.getByRole('button', { name: '启动' })).toBeDisabled();
  });
  it('rejects malformed host snapshots and removes previously displayed data', async () => {
    let emit: (value: unknown) => void = () => {};
    const unsubscribe = vi.fn();
    const bridge: HostBridge = { getSnapshot: async () => connected, subscribe: listener => { emit = listener; return unsubscribe; } };
    const view = render(<App bridge={bridge} />);
    await screen.findByRole('button', { name: /合成测试任务/ });
    act(() => emit({ ...connected, tasks: [{ ...connected.tasks[0], state: 'magic-success' }] }));
    expect(screen.getByRole('status')).toHaveTextContent('主机状态格式无效');
    expect(screen.queryByRole('button', { name: /合成测试任务/ })).not.toBeInTheDocument();
    view.unmount(); expect(unsubscribe).toHaveBeenCalledOnce();
  });
  it('does not overwrite newer subscription data with a late initial read', async () => {
    let resolve: (value: unknown) => void = () => {};
    let emit: (value: unknown) => void = () => {};
    render(<App bridge={{ getSnapshot: () => new Promise(r => { resolve = r; }), subscribe: listener => { emit = listener; return () => {}; } }} />);
    act(() => emit(connected));
    await act(async () => resolve(disconnectedSnapshot));
    expect(screen.getByText('主机已连接 · 只读')).toBeInTheDocument();
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
    const parsed = parseSnapshot(input); input.tasks[0].title = 'changed';
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
  ])('rejects invalid data %#', input => { expect(() => parseSnapshot(input)).toThrow('主机状态格式无效'); });
});
