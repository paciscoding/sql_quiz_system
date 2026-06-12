<script setup lang="ts">
defineProps<{
  message: string
  id?: string
}>()
</script>

<template>
  <p v-if="message" :id="id" class="field-error" role="alert">{{ message }}</p>
</template>

<style scoped>
.field-error {
  color: var(--color-danger);
  font-size: 0.8rem;
  margin: 4px 0 0 0;
  font-weight: 700;
}
</style>
