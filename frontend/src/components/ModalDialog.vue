<script setup>
import { onMounted, onUnmounted } from 'vue';

defineProps({
  title: { type: String, default: '' },
  okText: { type: String, default: '保存' },
  busy: { type: Boolean, default: false },
  hideFoot: { type: Boolean, default: false },
});
const emit = defineEmits(['close', 'ok']);

// Prevent background scrolling when modal is open.
onMounted(() => { document.body.style.overflow = 'hidden'; });
onUnmounted(() => { document.body.style.overflow = ''; });
</script>

<template>
  <Teleport to="body">
    <div class="modal-mask" @click.self="emit('close')">
      <div class="modal">
        <h3>{{ title }}</h3>
        <slot />
        <div v-if="!hideFoot" class="modal-foot">
          <button class="btn ghost2" @click="emit('close')">取消</button>
          <button class="btn" :disabled="busy" @click="emit('ok')">
            <span v-if="busy" class="spin"></span>
            {{ busy ? ' 处理中' : okText }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
