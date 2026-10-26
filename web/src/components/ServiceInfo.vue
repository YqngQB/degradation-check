<script setup lang="ts">
import { onMounted, useTemplateRef } from 'vue'
defineProps<{ mode: 'auto' | 'direct' | 'sub2api' }>()
defineEmits<{ close: [] }>()
const dialog = useTemplateRef<HTMLDialogElement>('dialog')
onMounted(() => dialog.value?.showModal())
</script>

<template>
  <dialog ref="dialog" class="card m-auto w-[calc(100%-3rem)] max-w-md p-6 text-gray-900 backdrop:bg-black/50 dark:text-gray-100" aria-labelledby="connection-title" @cancel.prevent="$emit('close')" @click.self="$emit('close')">
      <h2 id="connection-title" class="text-base font-semibold">连接说明</h2>
      <p class="mt-4 text-sm">当前模式：{{ mode === 'auto' ? '自动读取账号后直连上游' : mode === 'direct' ? '手动配置上游直连' : 'Sub2API 管理接口兼容模式' }}</p>
      <p class="mt-3 text-sm text-gray-500 dark:text-gray-400">
        自动账号模式沿用 Sub2API 菜单登录与账号加载。选择账号后，服务端读取其真实上游信息直接发送检测题目，无需另配每个账号的 Key。
      </p>
      <p v-if="mode === 'direct'" class="mt-3 text-sm text-gray-500 dark:text-gray-400">
        检测直接请求所选渠道，不修改 Sub2API 账号协议或 Codex 设置。新增渠道请修改服务端配置后重启检测服务。
      </p>
      <p class="mt-3 text-sm text-gray-500 dark:text-gray-400">同一 Sub2API 实例的管理员共享检测结果。上游密钥只在服务端内存使用；管理员会话过期后需从后台重新进入。</p>
      <div class="mt-5 flex justify-end">
        <button class="btn btn-secondary" autofocus @click="$emit('close')">关闭</button>
      </div>
  </dialog>
</template>
