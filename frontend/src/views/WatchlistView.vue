<script setup>
import { computed, onMounted, ref } from 'vue';
import { useRouter } from 'vue-router';
import { Api } from '../api';
import { useApp } from '../store';
import { dirClass, money, pctRaw, price, signed } from '../format';

const app = useApp();
const router = useRouter();
const items = ref([]);
const loading = ref(true);

async function load() {
  loading.value = true;
  try {
    items.value = await Api.watchlist();
  } catch (e) {
    app.toast('加载失败', e.message, 'error');
  } finally {
    loading.value = false;
  }
}

// Merge live SSE prices into each row so the list ticks without a reload.
const rows = computed(() => items.value.map((w) => app.live(w)));

async function remove(id) {
  try {
    await Api.removeWatch(id);
    await load();
  } catch (e) {
    app.toast('删除失败', e.message, 'error');
  }
}

async function updateTarget(w) {
  const v = window.prompt('目标价（留空表示不设置）', w.targetPrice ? String(w.targetPrice) : '');
  if (v === null) return;
  if (v === '') {
    try {
      await Api.updateWatch(w.id, { targetPrice: null, note: w.note });
      await load();
    } catch (e) {
      app.toast('更新失败', e.message, 'error');
    }
    return;
  }
  const tp = parseFloat(v);
  if (isNaN(tp) || tp <= 0) {
    app.toast('请输入有效的正数', '', 'error');
    return;
  }
  try {
    await Api.updateWatch(w.id, { targetPrice: tp, note: w.note });
    await load();
  } catch (e) {
    app.toast('更新失败', e.message, 'error');
  }
}

onMounted(load);
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="page-title">自选</div>
        <div class="page-sub">关注的标的集中查看，行情实时刷新（在持仓 / 标的详情页可一键加入）</div>
      </div>
      <div class="flex gap8">
        <button class="btn sec sm" @click="load">刷新</button>
        <router-link class="btn sm" to="/positions">去添加</router-link>
      </div>
    </div>

    <div v-if="loading" class="empty">加载中…</div>

    <div v-else-if="!rows.length" class="card section empty">
      <div class="big">★</div>
      自选列表为空。在「持仓」页或「标的详情」页点击「加自选」即可把标的加入这里。
    </div>

    <div v-else class="grid cards-3">
      <div v-for="w in rows" :key="w.id" class="card watch-card">
        <div class="wc-head">
          <router-link class="link" :to="`/assets/${w.assetId}`">{{ w.name }}</router-link>
          <span class="tag">{{ w.symbol }}</span>
          <span v-if="w.quoteStatus === 'sim'" class="tag" title="模拟行情">模拟</span>
          <span v-else-if="w.quoteStatus === 'stale'" class="tag warn" title="真实行情获取失败">失效</span>
          <span v-else-if="w.quoteStatus === 'nosource'" class="tag warn" title="暂无可用行情源（如场外基金、白名单外币种）">无源</span>
        </div>
        <div class="wc-price num">{{ w.quoteStatus === 'nosource' ? '—' : price(w.price) }} <span v-if="w.quoteStatus !== 'nosource'" class="num" :class="dirClass(w.chgPct)">{{ pctRaw(w.chgPct) }}</span></div>
        <div class="wc-meta">
          <span v-if="w.targetPrice" :class="{ hit: w.aboveTarget }">
            目标价 {{ price(w.targetPrice) }}
            <template v-if="w.aboveTarget"> · 已触及 ✅</template>
          </span>
          <span v-else class="muted">未设目标价</span>
          <span v-if="w.note" class="muted"> · {{ w.note }}</span>
        </div>
        <div class="wc-actions">
          <button class="btn sm sec" @click="updateTarget(w)">目标价</button>
          <button class="btn sm danger" @click="remove(w.id)">移除</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.watch-card { padding: 14px 16px; display: flex; flex-direction: column; gap: 8px; }
.wc-head { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.wc-price { font-size: 20px; font-weight: 700; }
.wc-meta { font-size: 12.5px; color: var(--muted); }
.wc-meta .hit { color: var(--down); font-weight: 600; }
.wc-actions { display: flex; gap: 6px; margin-top: 2px; }
</style>
