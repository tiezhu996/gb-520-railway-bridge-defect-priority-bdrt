<script setup lang="ts">
import { computed } from 'vue';
import { formatDate } from '../../utils/format';
import { COMPLETION_BLOCKER_REASON_LABELS } from '../../types/status';
import type { CompletionCheck, CompletionDefectDetail, DefectCompletionCheck } from '../../types/domain';

const props = defineProps<{
  roundCheck?: CompletionCheck | null;
  defectCheck?: DefectCompletionCheck | null;
}>();

interface CellView {
  status: 'passed' | 'blocked';
  createdAt: string;
  actor: string;
  requestId: string;
  roundCode: string;
  defects: CompletionDefectDetail[];
  blockerCodes: string[];
}

const view = computed<CellView | null>(() => {
  if (props.roundCheck) {
    return {
      status: props.roundCheck.status,
      createdAt: props.roundCheck.createdAt,
      actor: props.roundCheck.actor,
      requestId: props.roundCheck.requestId,
      roundCode: props.roundCheck.roundCode,
      defects: props.roundCheck.details.defects,
      blockerCodes: props.roundCheck.blockerCodes,
    };
  }
  if (props.defectCheck) {
    return {
      status: props.defectCheck.status,
      createdAt: props.defectCheck.createdAt,
      actor: props.defectCheck.actor,
      requestId: props.defectCheck.requestId,
      roundCode: props.defectCheck.roundCode,
      defects: [props.defectCheck.defect],
      blockerCodes: props.defectCheck.defect.blockerReason ? [props.defectCheck.defect.defectCode] : [],
    };
  }
  return null;
});

const tagType = computed(() => (view.value?.status === 'passed' ? 'success' : 'danger'));
const tagLabel = computed(() => {
  if (!view.value) return '未核验';
  return view.value.status === 'passed' ? '核验通过' : '核验阻塞';
});
</script>

<template>
	<el-popover v-if="view" placement="top" :width="360" trigger="hover">
		<template #reference>
			<el-tag :type="tagType" effect="light" class="completion-tag">{{ tagLabel }}<span v-if="view.blockerCodes.length" class="completion-count">{{ view.blockerCodes.length }}</span></el-tag>
		</template>
		<div class="completion-pop">
			<p><strong>批次 {{ view.roundCode }} · {{ view.status === 'passed' ? '核验通过' : '核验阻塞' }}</strong></p>
			<p class="muted">{{ formatDate(view.createdAt) }} · {{ view.actor }} · {{ view.requestId }}</p>
			<ul v-if="view.defects.length" class="completion-defects">
				<li v-for="defect in view.defects" :key="defect.defectCode">
					<strong>{{ defect.defectCode }}</strong>
					<span class="muted">{{ defect.state }} · {{ defect.riskLevel }}</span>
					<span v-if="defect.dispositionBasis" class="basis">依据：{{ defect.dispositionBasis }}</span>
					<span v-if="defect.priorityCode" class="basis">优先级：{{ defect.priorityCode }}（{{ defect.priorityStatus }}）</span>
					<el-alert v-if="defect.blockerReason" :title="COMPLETION_BLOCKER_REASON_LABELS[defect.blockerReason] || defect.blockerMessage || defect.blockerReason" :description="defect.blockerMessage" type="error" :closable="false" show-icon/>
				</li>
			</ul>
			<p v-else class="muted">该批次无关联缺陷，核验自动通过。</p>
		</div>
	</el-popover>
	<span v-else class="muted">未核验</span>
</template>

<style scoped>
.completion-tag { cursor: default; }
.completion-count { margin-left: 4px; font-weight: 700; }
.completion-pop p { margin: 0 0 6px; }
.completion-defects { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
.completion-defects li { display: flex; flex-direction: column; gap: 2px; padding: 6px 8px; background: var(--el-fill-color-light); border-radius: 6px; }
.basis { font-size: 12px; color: var(--el-text-color-regular); word-break: break-all; }
.muted { color: var(--el-text-color-secondary); font-size: 12px; }
</style>
