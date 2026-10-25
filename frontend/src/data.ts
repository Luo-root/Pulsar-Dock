import type { AgentKind, Conversation, SessionConfig } from './types';

export const defaultConfig: SessionConfig = {
  agent: 'Claude Code',
  model: '未连接模型',
  approval: '标准模式',
  collaboration: '单 Agent',
  reviewRounds: '2 轮',
  stopCondition: '发现高风险问题即暂停',
  mode: 'direct',
  workflow: '需求 → 实现 → 审查 → 交付',
  goal: '',
};

export function createConversation(id: string): Conversation {
  return { id, title: '新会话', messages: [], draft: '', config: { ...defaultConfig } };
}

export const demoConversation: Conversation = {
  id: 'demo',
  title: '整理项目的错误处理',
  draft: '',
  demo: true,
  config: { ...defaultConfig, collaboration: '对抗审查' },
  messages: [
    {
      id: 'demo-user',
      role: 'user',
      content: [{ type: 'text', text: '帮我梳理 API 的错误处理，给出一个统一的响应方案。先不要修改文件。' }],
    },
    {
      id: 'demo-assistant',
      role: 'assistant',
      content: [{
        type: 'text',
        text: '可以先把错误分成三层：业务错误、请求校验与未知异常。对外保持一致的响应结构，对内保留可追踪的上下文。\n\n建议用 error.code 表达稳定的业务语义，message 给出可读说明，request_id 关联日志。具体堆栈与内部路径不进入响应。\n\n下面是方案的示例呈现，未读取项目、未启动协作者，也没有修改任何文件。',
      }],
    },
  ],
};

export const agentOptions: { name: AgentKind; description: string }[] = [
  { name: 'Claude Code', description: '目标适配器 · 原生会话与工具事件由主机能力报告决定' },
  { name: 'Codex', description: '目标适配器 · 版本、恢复与事件能力需要逐项实测' },
  { name: 'DSH', description: '目标适配器 · 受控 Web 入口与生命周期仍待接入' },
];
