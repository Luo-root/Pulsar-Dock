import { Check, Info, X } from 'lucide-react';
import type { Snapshot } from '../bridge';
import type { Preferences } from '../types';
import { Button, Dialog, DialogClose, DialogContent, DialogDescription, DialogTitle } from './ui';

export type SettingsTab = '通用' | '连接' | '外观' | '诊断';

export function SettingsDialog({ open, onOpenChange, tab, onTab, snapshot, preferences, onPreferences, returnFocus }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  tab: SettingsTab;
  onTab: (tab: SettingsTab) => void;
  snapshot: Snapshot;
  preferences: Preferences;
  onPreferences: (patch: Partial<Preferences>) => void;
  returnFocus: HTMLElement | null;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="settings-dialog" onCloseAutoFocus={event => {
        if (!returnFocus) return;
        event.preventDefault();
        returnFocus.focus();
      }}>
        <div className="settings-heading"><DialogTitle>设置</DialogTitle><DialogClose asChild><Button size="icon" aria-label="关闭设置"><X size={16} /></Button></DialogClose></div>
        <DialogDescription>管理此界面的偏好，并查看真实连接边界。</DialogDescription>
        <div className="settings-layout">
          <nav aria-label="设置分类">{(['通用', '连接', '外观', '诊断'] as const).map(item => <Button key={item} aria-pressed={tab === item} onClick={() => onTab(item)}>{item}</Button>)}</nav>
          <section className="settings-body">
            <h2>{tab}</h2>
            {tab === '通用' && <><SettingRow label="界面模式" value="本地 UI 预览" /><SettingRow label="会话存储" value="仅当前窗口内存" /><p>刷新页面会清空本地草稿。发送只保留预览消息，不创建主机任务、不启动 Agent，也不改变审批策略。</p></>}
            {tab === '连接' && <><SettingRow label="桌面主机" value={snapshot.connection === 'connected' ? '已连接 · 只读' : '未连接'} /><SettingRow label="模型 / Magpie / MCP" value="未接入" /><p>当前没有连接测试、凭据输入或管理操作桥。请勿在聊天中填写真实密钥。</p><Button variant="outline" disabled>检查连接</Button></>}
            {tab === '外观' && <><div className="setting-row"><label htmlFor="theme">主题</label><select id="theme" value={preferences.theme} onChange={event => onPreferences({ theme: event.target.value as Preferences['theme'] })}><option value="light">浅色 · Gold-Band 风格</option><option value="dark">深色 · 同一布局</option></select></div><div className="setting-row"><label htmlFor="compact">紧凑消息间距</label><input id="compact" type="checkbox" checked={preferences.compact} onChange={event => onPreferences({ compact: event.target.checked })} /></div><div className="setting-row"><label htmlFor="inspector">默认展开右侧工作区</label><input id="inspector" type="checkbox" checked={preferences.inspector} onChange={event => onPreferences({ inspector: event.target.checked })} /></div><p>偏好只保存在此浏览器的本地存储中，不包含项目、任务或凭据。</p></>}
            {tab === '诊断' && <><SettingRow label="前端原型" value="0.1.0" /><SettingRow label="Host bridge" value="只读快照 · v1" /><SettingRow label="任务 / 事件 / 成果" value={`${snapshot.tasks.length} / ${snapshot.events.length} / ${snapshot.artifacts.length}`} /><div className="diagnostic-note"><Info size={15} /><span>浏览器测试不等于 Wails 桌面壳、安装包或真实 Agent 环境验收。</span></div></>}
          </section>
        </div>
        <div className="settings-footer"><Check size={13} />设置变更仅影响本地界面</div>
      </DialogContent>
    </Dialog>
  );
}

function SettingRow({ label, value }: { label: string; value: string }) {
  return <div className="setting-row"><span>{label}</span><span>{value}</span></div>;
}
