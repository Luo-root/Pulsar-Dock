import { LockKeyhole } from 'lucide-react';
import type { Snapshot } from '../bridge';
import { BrandIcon, type BrandName } from './BrandIcon';
import { Button } from './ui';

const connections: { icon: BrandName; title: string; description: string }[] = [
  { icon: 'ACP', title: '模型服务', description: '连接协议兼容的模型入口，凭据只留在可信控制面。' },
  { icon: 'Magpie', title: 'Magpie', description: '复用已有模型与配置能力，不复制上游密钥到任务目录。' },
  { icon: 'MCP', title: 'MCP 工具', description: '只向 Agent 暴露经过授权的受控工具入口。' },
];

export function ConnectionCards({ snapshot, actionReason, compact = false }: {
  snapshot: Snapshot;
  actionReason: string;
  compact?: boolean;
}) {
  const connected = snapshot.connection === 'connected';
  return (
    <div className={compact ? 'connection-settings' : ''}>
      <div className={`connection-grid ${compact ? 'is-compact' : ''}`}>
        {connections.map(({ icon, title, description }) => (
          <article className="connection-card" key={title}>
            <div className="connection-card-heading">
              <span className="connection-icon"><BrandIcon name={icon} size={icon === 'Magpie' ? 32 : 21} /></span>
              <span className={`status-pill ${connected ? 'is-connected' : ''}`}><span className="status-dot" />{connected ? '等待连接详情' : '未连接'}</span>
            </div>
            <h2>{title}</h2>
            <p>{description}</p>
            <Button variant="outline" disabled aria-describedby="connection-action-reason">检查连接</Button>
          </article>
        ))}
      </div>
      <p className="action-reason" id="connection-action-reason">{actionReason}</p>
      <section className="info-section"><LockKeyhole size={18} /><div><h2>凭据边界保持不变</h2><p>当前界面没有凭据输入、连接测试或管理操作桥。真实密钥不得进入聊天、任务快照、示例内容或普通日志。</p></div></section>
    </div>
  );
}
