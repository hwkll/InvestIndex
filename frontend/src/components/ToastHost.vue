<script setup>
import { useApp } from '../store';
const app = useApp();
</script>

<template>
  <Teleport to="body">
    <div class="toast-root">
      <TransitionGroup name="toast">
        <div v-for="t in app.toasts" :key="t.id" class="toast" :class="t.kind" @click="app.dismiss(t.id)">
          <div class="t-title">{{ t.title }}</div>
          <div v-if="t.body" class="t-body">{{ t.body }}</div>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>
