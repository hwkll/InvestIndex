<script setup>
defineProps({
  label: String,
  value: String,
  delta: String,
  deltaClass: { type: String, default: 'muted' },
  loading: { type: Boolean, default: false },
});
</script>

<template>
  <div v-if="loading" class="card stat skel-stat" />
  <div v-else class="card stat">
    <div class="label">{{ label }}</div>
    <div class="value num">{{ value }}</div>
    <div class="delta" :class="deltaClass">{{ delta }}</div>
  </div>
</template>
