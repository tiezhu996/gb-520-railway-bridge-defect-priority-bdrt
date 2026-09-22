import type { EntityConfig } from './domain';

export type DefectState = 'new' | 'verified' | 'monitoring' | 'mitigated' | 'closed';
export const ALL_DEFECT_STATE: readonly DefectState[] = ['new', 'verified', 'monitoring', 'mitigated', 'closed'];
export type PriorityLevel = 'observe' | 'restrict' | 'urgent';
export const ALL_PRIORITY_LEVEL: readonly PriorityLevel[] = ['observe', 'restrict', 'urgent'];

export const COMPLETION_BLOCKER_REASON_LABELS: Record<string, string> = {
  pending_disposition: '缺陷尚未处置：须转为监测、缓解或关闭并写明依据',
  missing_basis: '缺少处置依据：迁移说明或已定稿优先级证据缺失',
  missing_finalized_priority: '严重/关键风险缺陷缺少已定稿的处置优先级',
};

export const ENTITY_CONFIGS: readonly EntityConfig[] = [
  { key: 'bridgeAsset', path: 'bridges', label: '桥梁资产', statuses: ['active', 'restricted', 'closed', 'retired'] as const },
  { key: 'inspectionRound', path: 'inspections', label: '检查批次', statuses: ['planned', 'running', 'review', 'completed'] as const },
  { key: 'defectFinding', path: 'defects', label: '缺陷发现', statuses: ['new', 'verified', 'monitoring', 'mitigated', 'closed'] as const },
  { key: 'priorityDecision', path: 'priorities', label: '优先级决定', statuses: ['draft', 'observe', 'restrict', 'urgent'] as const }
];
