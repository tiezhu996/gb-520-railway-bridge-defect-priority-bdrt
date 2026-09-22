
<script setup lang="ts">
import { onMounted, ref } from 'vue';
import EntityPage from '../components/EntityPage.vue';
import { ENTITY_CONFIGS } from '../types/status';
import { useDefectFindingStore } from '../stores/defect-finding';
import { listCompletionChecks } from '../api/inspection-round';
import type { CompletionCheck } from '../types/domain';

const store = useDefectFindingStore();
const checks = ref<CompletionCheck[] | null>(null);

async function loadChecks() {
	try {
		const result = await listCompletionChecks();
		checks.value = result.data ?? [];
	} catch {
		checks.value = [];
	}
}

onMounted(loadChecks);
</script>
<template><EntityPage :config="ENTITY_CONFIGS[2]" :store="store" show-evidence :checks="checks" @refresh-checks="loadChecks"/></template>
