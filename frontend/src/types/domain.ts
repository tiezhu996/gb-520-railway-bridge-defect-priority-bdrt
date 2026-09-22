
export interface CompletionDefectDetail {
  defectId: number;
  defectCode: string;
  state: string;
  riskLevel: string;
  disposed: boolean;
  dispositionBasis: string;
  priorityCode: string;
  priorityStatus: string;
  blockerReason?: string;
  blockerMessage?: string;
}

export interface CompletionCheck {
  inspectionRoundId: number;
  roundCode: string;
  status: 'passed' | 'blocked';
  actor: string;
  requestId: string;
  relatedDefectCount: number;
  blockerCodes: string[];
  details: { roundStatus: string; defects: CompletionDefectDetail[] };
  createdAt: string;
}

export interface DefectCompletionCheck {
  inspectionRoundId: number;
  roundCode: string;
  status: 'passed' | 'blocked';
  actor: string;
  requestId: string;
  defect: CompletionDefectDetail;
  createdAt: string;
}

export interface DomainRecord {
  id: number;
  code: string;
  name: string;
  status: string;
  version: number;
  description: string;
  facility: string;
  owner: string;
  category: string;
  riskLevel: 'low' | 'medium' | 'high' | 'critical';
  metricValue: number;
  metricUnit: string;
  effectiveAt: string;
  evidence: string;
  relatedCode: string;
  preparedBy?: string;
  revisions?: PriorityDecisionRevision[];
  latestCompletionCheck?: CompletionCheck | null;
  completionCheck?: DefectCompletionCheck | null;
  createdAt: string;
  updatedAt: string;
}

export interface PriorityDecisionRevision {
  id: number;
  priorityDecisionId: number;
  version: number;
  status: string;
  evidence: string;
  reason: string;
  actor: string;
  requestId: string;
  snapshot: string;
  createdAt: string;
}

export interface PageMeta { page: number; pageSize: number; total: number }
export interface ApiEnvelope<T> { data: T; error?: string; message?: string; meta?: PageMeta }
export type UserRole = 'viewer' | 'operator' | 'reviewer' | 'admin';
export interface UserSession { token: string; username: string; displayName: string; role: UserRole; expiresIn: number }
export interface AuditLog {
  id: number; requestId: string; actor: string; action: string; entityType: string;
  entityId: number; beforeState: string; afterState: string; detail: string; createdAt: string;
}
export interface EntityConfig { key: string; path: string; label: string; statuses: readonly string[] }
