<template>
  <ElDialog
    v-model="visible"
    :title="dialogType === 'add' ? t('新增') : t('编辑')"
    width="600px"
    align-center
    :close-on-click-modal="false"
    @closed="formRef?.clearValidate()"
  >
    <ElForm
      ref="formRef"
      v-loading="reading"
      :model="formData"
      label-width="120px"
      @submit.prevent="submit"
    >
      <ElFormItem
        v-for="column in columns.filter((column) => !column.readonly)"
        :key="column.prop"
        :label="column.label"
        :prop="column.prop"
        :rules="
          column.required
            ? [
                {
                  required: true,
                  message: t('请填写{label}', { label: column.label }),
                  trigger: 'blur'
                }
              ]
            : []
        "
      >
        <ElSelect
          v-if="column.options"
          v-model="formData[column.prop]"
          :disabled="dialogType === 'edit' && column.immutable"
        >
          <ElOption
            v-for="option in column.options"
            :key="option.value"
            :label="option.label"
            :value="option.value"
          />
        </ElSelect>
        <ElInputNumber
          v-else-if="column.type === 'number'"
          v-model="formData[column.prop]"
          :disabled="dialogType === 'edit' && column.immutable"
        />
        <ElSwitch v-else-if="column.type === 'boolean'" v-model="formData[column.prop]" />
        <ElInput
          v-else
          v-model="formData[column.prop]"
          :type="
            column.type === 'textarea'
              ? 'textarea'
              : column.type === 'password'
                ? 'password'
                : 'text'
          "
          :rows="3"
          :maxlength="column.maxlength"
          :show-password="column.type === 'password'"
          :placeholder="t('请填写{label}', { label: column.label })"
          :disabled="dialogType === 'edit' && column.immutable"
        />
      </ElFormItem>
      <slot name="form" :form="formData" />
    </ElForm>
    <template #footer
      ><ElButton @click="visible = false">{{ t('取消') }}</ElButton
      ><ElButton type="primary" :loading="saving" :disabled="reading" @click="submit">{{
        t('提交')
      }}</ElButton></template
    >
  </ElDialog>
</template>
<script setup lang="ts">
  import { computed, nextTick, reactive, ref, watch } from 'vue'
  import { useI18n } from 'vue-i18n'
  import {
    ElDialog,
    ElForm,
    ElFormItem,
    ElInput,
    ElInputNumber,
    ElSwitch,
    ElSelect,
    ElOption,
    ElButton,
    ElMessage,
    type FormInstance
  } from 'element-plus'
  import type { Column, CrudApi } from '@dawnim/admin'
  const { t } = useI18n()
  const props = defineProps<{
    modelValue: boolean
    dialogType: string
    data?: Record<string, any>
    columns: Column[]
    defaults: Record<string, any>
    api: CrudApi
  }>()
  const emit = defineEmits<{ 'update:modelValue': [boolean]; success: [] }>()
  const visible = computed({
    get: () => props.modelValue,
    set: (value) => emit('update:modelValue', value)
  })
  const formRef = ref<FormInstance>(),
    saving = ref(false),
    reading = ref(false)
  const formData = reactive<Record<string, any>>({})
  watch(
    () => props.modelValue,
    async (opened) => {
      if (!opened) return
      const defaults = Object.fromEntries(
        props.columns
          .filter((column) => !column.readonly)
          .map((column) => [
            column.prop,
            column.type === 'number' ? 0 : column.type === 'boolean' ? false : ''
          ])
      )
      Object.keys(formData).forEach((key) => delete formData[key])
      Object.assign(formData, defaults, props.defaults)
      if (props.dialogType === 'edit' && props.data) {
        reading.value = true
        try {
          Object.assign(formData, await props.api.read(props.data.id))
        } catch {
          visible.value = false
        } finally {
          reading.value = false
        }
      }
      props.columns
        .filter((column) => column.type === 'password')
        .forEach((column) => (formData[column.prop] = ''))
      await nextTick()
      formRef.value?.clearValidate()
    }
  )
  async function submit() {
    if (reading.value || saving.value || !(await formRef.value?.validate().catch(() => false)))
      return
    saving.value = true
    try {
      if (props.dialogType === 'add') await props.api.save(formData)
      else await props.api.update(formData)
      ElMessage.success(t(props.dialogType === 'add' ? '新增成功' : '修改成功'))
      emit('success')
      visible.value = false
    } finally {
      saving.value = false
    }
  }
</script>
