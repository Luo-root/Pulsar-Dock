import type { ThreadMessageLike } from '@assistant-ui/react';

export type Page = 'home' | 'conversation' | 'agents' | 'connections' | 'workflow' | 'artifacts';
export type RunMode = 'direct' | 'workflow' | 'auto';
export type WorkspaceTab = 'resources' | 'artifacts' | 'permissions' | 'workflow';

export interface SessionConfig {
  agent: string;
  model: string;
  approval: string;
  collaboration: string;
  reviewRounds: string;
  stopCondition: string;
  mode: RunMode;
  workflow: string;
  goal: string;
}

export interface Conversation {
  id: string;
  title: string;
  messages: ThreadMessageLike[];
  draft: string;
  config: SessionConfig;
  demo?: boolean;
  notice?: string;
}

export interface Preferences {
  theme: 'light' | 'dark';
  inspector: boolean;
  compact: boolean;
}
