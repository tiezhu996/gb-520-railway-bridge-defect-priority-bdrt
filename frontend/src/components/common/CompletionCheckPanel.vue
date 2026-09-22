
<script setup lang="ts">
import type { CompletionCheck } from '../../types/domain';
import { formatDate } from '../../utils/format';

// CompletionCheckPanel renders the persisted 缺陷处置核验 outcome. It is shared
// by the inspection page (per-round results) and the defect page (blocking
// defect codes), and survives refresh because results are stored server-side.
defineProps<{ checks: CompletionCheck[]; mode: 'round' | 'defect' }>();
</script>
<template>
	<div class="check-strip">
		<div v-if="!checks.length" class="empty">暂无核验记录，批次从复核推进完成时会自动核验关联缺陷</div>
		<article v-for="check in checks" :key="check.id" :class="check.passed ? 'check-pass' : 'check-block'">
			<header>
				<strong>{{ check.roundCode }}</strong>
				<span :class="`status status--${check.passed ? 'success' : 'danger'}`">{{ check.passed ? '核验通过' : '完成受阻' }}</span>
			</header>
			<p>覆盖缺陷 {{ check.defectTotal }} 条 · {{ check.actor }} · {{ formatDate(check.checkedAt) }}</p>
			<template v-if="!check.passed && check.blockers.length">
				<p class="blocker-line">阻塞编号：<code v-for="blocker in check.blockers" :key="blocker.defectId">{{ blocker.defectCode }}</code></p>
				<small v-for="blocker in check.blockers" :key="`reason-${blocker.defectId}`">
					{{ blocker.defectCode }}（{{ blocker.riskLevel }} · {{ blocker.status }}）：{{ blocker.reason }}
				</small>
			</template>
			<small v-else-if="check.passed">同批次缺陷均已处置并写明依据，严重缺陷已有定稿优先级</small>
		</article>
	</div>
</template>
