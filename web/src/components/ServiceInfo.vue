<script setup lang="ts">
import { onMounted, useTemplateRef } from 'vue'
defineProps<{ mode: 'direct' | 'sub2api' }>()
defineEmits<{ close: [] }>()
const dialog = useTemplateRef<HTMLDialogElement>('dialog')
onMounted(() => dialog.value?.showModal())
</script>

<template>
  <dialog ref="dialog" class="card m-auto w-[calc(100%-3rem)] max-w-md p-6 text-gray-900 backdrop:bg-black/50 dark:text-gray-100" aria-labelledby="connection-title" @cancel.prevent="$emit('close')" @click.self="$emit('close')">
      <h2 id="connection-title" class="text-base font-semibold">连接说明</h2>
      <p class="mt-4 text-sm">当前模式：{{ mode === 'direct' ? '上游直连' : 'Sub2API 管理接口兼容模式' }}</p>
      <p class="mt-3 text-sm text-gray-500 dark:text-gray-400">
        渠道地址、协议和密钥由服务器管理员配置。页面无需填写 Sub2API 管理员令牌。
      </p>
      <p v-if="mode === 'direct'" class="mt-3 text-sm text-gray-500 dark:text-gray-400">
        检测直接请求所选渠道，不修改 Sub2API 账号协议或 Codex 设置。新增渠道请修改服务端配置后重启检测服务。
      </p>
      <p class="mt-3 text-sm text-gray-500 dark:text-gray-400">本实例所有登录者共享检测结果；这不是多租户隔离服务。</p>
      <div class="mt-5 flex justify-end">
        <button class="btn btn-secondary" autofocus @click="$emit('close')">关闭</button>
      </div>
  </dialog>
</template>
