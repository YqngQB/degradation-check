<script setup lang="ts">
import { onMounted, ref, useTemplateRef } from 'vue'
import { verifyStepUp } from '../api'
const emit = defineEmits<{ close: []; verified: [] }>()
const dialog = useTemplateRef<HTMLDialogElement>('dialog')
const code = ref('')
const busy = ref(false)
const error = ref('')
onMounted(() => dialog.value?.showModal())
async function verify() {
  if (busy.value) return
  busy.value = true; error.value = ''
  const value = code.value; code.value = ''
  try { await verifyStepUp(value); emit('verified') }
  catch (e: any) { error.value = e?.message || '验证失败，请重试' }
  finally { busy.value = false }
}
</script>
<template>
  <dialog ref="dialog" class="card m-auto w-[calc(100%-3rem)] max-w-md p-6 text-gray-900 backdrop:bg-black/50 dark:text-gray-100" aria-labelledby="step-up-title" @cancel.prevent="!busy && emit('close')">
    <form class="space-y-4" @submit.prevent="verify">
      <h2 id="step-up-title" class="text-base font-semibold">验证后读取所选账号</h2>
      <p class="text-sm text-gray-500">Sub2API 要求二次验证才能读取上游凭据。请输入当前管理员的六位动态验证码，通过后继续本次检测。</p>
      <label class="block text-sm">动态验证码
        <input v-model="code" class="input mt-1" inputmode="numeric" pattern="[0-9]{6}" maxlength="6" autocomplete="one-time-code" autofocus required :disabled="busy" aria-label="动态验证码" />
      </label>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <div class="flex justify-end gap-2">
        <button type="button" class="btn btn-secondary" :disabled="busy" @click="emit('close')">取消</button>
        <button class="btn btn-primary" :disabled="busy || !/^[0-9]{6}$/.test(code)">{{ busy ? '正在验证…' : '验证并继续检测' }}</button>
      </div>
    </form>
  </dialog>
</template>
