<script setup>
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue';
import echarts from '../echarts';

const props = defineProps({
  option: { type: Object, required: true },
  height: { type: String, default: '280px' },
  loading: { type: Boolean, default: false },
  empty: { type: Boolean, default: false },
  emptyText: { type: String, default: '暂无数据' },
});
const emit = defineEmits(['click']);

const el = ref(null);
const chart = shallowRef(null);
let ro = null;

const showEmpty = computed(() => props.empty && !props.loading);

function render() {
  if (!chart.value) return;
  if (showEmpty.value) {
    chart.value.clear();
    return;
  }
  chart.value.setOption(props.option, true);
  props.loading ? chart.value.showLoading('default', { text: '加载中', color: '#3b5bff' }) : chart.value.hideLoading();
}

onMounted(() => {
  chart.value = echarts.init(el.value, null, { renderer: 'canvas' });
  chart.value.on('click', (p) => emit('click', p));
  render();
  ro = new ResizeObserver(() => chart.value && chart.value.resize());
  ro.observe(el.value);
});

onBeforeUnmount(() => {
  if (ro) ro.disconnect();
  if (chart.value) { chart.value.dispose(); chart.value = null; }
});

watch(() => props.option, render, { deep: true });
watch(() => props.loading, render);
watch(() => props.empty, () => {
  if (chart.value && props.empty && !props.loading) {
    chart.value.clear();
  } else {
    render();
  }
});
</script>

<template>
  <div class="chart-container" :style="{ height }">
    <div v-if="showEmpty" class="chart-empty">
      <div class="ce-ico">◇</div>
      <div class="ce-text">{{ emptyText }}</div>
    </div>
    <div ref="el" :style="{ height, width: '100%' }" :class="{ hidden: showEmpty }"></div>
  </div>
</template>

<style scoped>
.chart-container { position: relative; }
.chart-container .hidden { visibility: hidden; position: absolute; top: 0; left: 0; }
</style>
