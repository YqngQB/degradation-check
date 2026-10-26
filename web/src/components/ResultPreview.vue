<script setup lang="ts">
import { ref, watch } from 'vue'
import { getPreview } from '../api'
const props = defineProps<{ accountId: number; stamp?: string; title?: string }>()
const html = ref('')
const error = ref('')
watch(() => [props.accountId, props.stamp], async (_, __, onCleanup) => {
  let current = true
  onCleanup(() => { current = false })
  html.value = ''; error.value = ''
  try { const value = await getPreview(props.accountId); if (current) html.value = value }
  catch (e: any) { if (current) error.value = e?.message || '预览加载失败' }
}, { immediate: true })
</script>
<template>
  <div v-if="error" role="alert" class="p-3 text-sm text-red-600">{{ error }}</div>
  <iframe v-else-if="html" :srcdoc="html" sandbox="allow-scripts" :title="title || '检测画面预览'" referrerpolicy="no-referrer"></iframe>
  <div v-else role="status" class="p-3 text-sm text-gray-500">正在加载预览…</div>
</template>
