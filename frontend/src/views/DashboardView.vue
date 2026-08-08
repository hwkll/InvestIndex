<script setup>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue';
import { useRouter } from 'vue-router';
import { Api } from '../api';
import { useApp } from '../store';
import EChart from '../components/EChart.vue';
import StatCard from '../components/StatCard.vue';
import { CATEGORIES, CATEGORY_LABEL, dirClass, money, pct, pctRaw, signed } from '../format';

const app = useApp();
const router = useRouter();

const summary = ref(null);
const trend = ref(null);
const range = ref('30d');
const loading = ref(true);
const posCache = ref({});   // category -> { items, subtotal }  (cached positions for live merge)
const fx = ref({ CNY: 1 }); // currency -> CNY per unit
const benchmark = ref('');  // current benchmark symbol (mirrors settings)
let liveTick = ref(0);      // bumped on every SSE quote frame
let reloadTimer = null;

const CAT_COLOR = { crypto: '#f7931a', fund: '#3b5bff', gold: '#e0a800', stock: '#1faa59', cash: '#9aa3b2' };

const cur = computed(() => summary.value?.mainCurrency || 'CNY');
const mainRate = computed(() => fx.value[cur.value] || 1);
// client-side FX so the live category table can merge SSE prices without a server round-trip
function conv(amount, from) {
  return amount * (fx.value[from] || 1) / mainRate.value;
}

async function loadSummary() {
  try {
    summary.value = await Api.summary();
    app.unread = summary.value.unreadAlertsCount || app.unread;
    if (summary.value.benchmark) benchmark.value = summary.value.benchmark;
  } catch (e) {
    app.toast('加载失败', e.message, 'error');
  }
}

async function loadPositions() {
  try {
    const out = {};
    for (const c of CATEGORIES) {
      out[c] = await Api.positions(c);
    }
    posCache.value = out;
  } catch { /* ignore — table falls back to server summary */ }
}

async function loadFx() {
  try { fx.value = await Api.fxRates().then((r) => Object.fromEntries(r.map((x) => [x.currency, x.rate]))); }
  catch { /* ignore */ }
}

async function loadTrend() {
  try {
    trend.value = await Api.trend(range.value);
    if (trend.value.benchmarkLabel) benchmark.value = trend.value.benchmarkLabel;
  } catch (e) {
    app.toast('趋势加载失败', e.message, 'error');
  }
}

async function loadAll() {
  await Promise.all([loadSummary(), loadPositions(), loadFx(), loadTrend()]);
  loading.value = false;
}

// Next-⑥: live category table — merge SSE prices into the cached positions and
// re-sum client-side, so the detail table ticks every frame WITHOUT a full
// dashboard/summary reload (only the headline cards + charts refresh on the 30s timer).
const catRows = computed(() => {
  const out = [];
  for (const c of CATEGORIES) {
    const pc = posCache.value[c];
    if (!pc) {
      const s = summary.value?.categories?.[c];
      if (s) out.push(s);
      continue;
    }
    const items = (pc.items || []).map((r) => app.live(r));
    let mv = 0, cost = 0, fl = 0;
    for (const it of items) {
      mv += conv(it.marketValue ?? 0, it.currency);
      cost += conv(it.costTotal ?? 0, it.currency);
      fl += conv(it.floatingPnl ?? 0, it.currency);
    }
    const fp = cost > 0 ? fl / cost : null;
    out.push({
      category: c, label: CATEGORY_LABEL[c],
      count: items.length, marketValue: mv, costTotal: cost,
      floatingPnl: fl, floatingPct: fp,
      // subtotal carries the realised P&L; top movers come from the summary
      // (PositionsByCategory does not populate them), refreshed every 30s.
      realizedPnl: pc.subtotal?.realizedPnl || 0,
      top: summary.value?.categories?.[c]?.top || null,
    });
  }
  liveTick.value; // dependency so the table re-computes on each quote frame
  return out.filter((c) => c.count > 0 || c.marketValue > 0);
});

async function changeBenchmark(sym) {
  benchmark.value = sym;
  try {
    await Api.saveSettings({ benchmark: sym });
    await loadTrend();
  } catch (e) {
    app.toast('设置基准失败', e.message, 'error');
  }
}

function goCat(cat) {
  router.push({ path: '/positions', query: { category: cat } });
}

onMounted(async () => {
  await loadAll();
  reloadTimer = setInterval(loadAll, 30000); // throttled full refresh
});
onUnmounted(() => { if (reloadTimer) clearInterval(reloadTimer); });

watch(range, loadTrend);
watch(() => app._quoteTick, () => { liveTick.value++; }); // only bumps the live table

const donutOption = computed(() => {
  const segs = (summary.value?.distribution || []).filter((d) => d.value > 0);
  return {
    tooltip: { trigger: 'item', formatter: (p) => `${p.name}<br/>${money(p.value, cur.value)} (${p.percent}%)` },
    legend: { bottom: 0, icon: 'roundRect', itemWidth: 10, itemHeight: 10, textStyle: { color: '#6b7686', fontSize: 12 } },
    series: [{
      type: 'pie', radius: ['52%', '76%'], center: ['50%', '44%'], avoidLabelOverlap: true,
      itemStyle: { borderColor: '#fff', borderWidth: 2 }, label: { show: false },
      data: segs.map((d) => ({ name: d.label, value: d.value, itemStyle: { color: CAT_COLOR[d.key] || '#8a63d2' } })),
    }],
    graphic: summary.value ? [
      { type: 'text', left: 'center', top: '38%', style: { text: '总资产', fill: '#6b7686', fontSize: 12 } },
      { type: 'text', left: 'center', top: '45%', style: { text: money(summary.value.totalAssets, cur.value, 0), fill: '#18202e', fontSize: 17, fontWeight: 700 } },
    ] : [],
  };
});

const trendOption = computed(() => {
  const s = trend.value?.series || [];
  const bench = trend.value?.benchmark || [];
  const hasBench = bench.length > 0 && s.length > 0;
  // When a benchmark is shown, normalise the portfolio line to a 100 base so the
  // two series are comparable on the same axis.
  const base = s.length ? s[0].totalAssets : 1;
  const portNorm = s.map((p) => (base ? (p.totalAssets / base) * 100 : 0));
  const benchNorm = bench.map((b) => b.value);
  const series = [
    {
      name: '总资产(指数化)', type: 'line', smooth: true, showSymbol: false,
      data: hasBench ? portNorm : s.map((p) => p.totalAssets),
      lineStyle: { width: 2, color: '#3b5bff' },
      areaStyle: hasBench ? undefined : {
        color: { type: 'linear', x: 0, y: 0, x2: 0, y2: 1, colorStops: [{ offset: 0, color: 'rgba(59,91,255,.25)' }, { offset: 1, color: 'rgba(59,91,255,0)' }] },
      },
    },
    { name: '投入成本', type: 'line', smooth: true, showSymbol: false, data: s.map((p) => p.cost), lineStyle: { width: 1.5, color: '#9aa3b2', type: 'dashed' } },
  ];
  if (hasBench) {
    series.push({ name: '基准 ' + (trend.value.benchmarkLabel || ''), type: 'line', smooth: true, showSymbol: false, data: benchNorm, lineStyle: { width: 2, color: '#e08a00' } });
  }
  return {
    tooltip: { trigger: 'axis', axisPointer: { type: 'cross', label: { backgroundColor: '#3b5bff' } }, valueFormatter: (v) => (hasBench ? v.toFixed(1) : money(v, cur.value, 0)) },
    legend: { top: 0, right: 0, textStyle: { color: '#6b7686', fontSize: 12 } },
    grid: { left: 8, right: 16, top: 34, bottom: 6, containLabel: true },
    xAxis: { type: 'category', boundaryGap: false, data: s.map((p) => p.date), axisLine: { lineStyle: { color: '#e6eaf1' } }, axisLabel: { color: '#9aa3b2', fontSize: 11 } },
    yAxis: { type: 'value', scale: true, splitLine: { style: { color: '#f0f2f7' } }, axisLabel: { color: '#9aa3b2', fontSize: 11, formatter: (v) => (hasBench ? v.toFixed(0) : (Math.abs(v) >= 10000 ? (v / 10000).toFixed(1) + '万' : v)) } },
    series,
  };
});
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="page-title">仪表盘</div>
        <div class="page-sub">
          全局资产总览 · 主币种 {{ cur }}
          <span v-if="summary" class="tag">{{ summary.assetCount }} 个标的</span>
        </div>
      </div>
      <div class="flex gap8">
        <button class="btn sec sm" @click="loadAll">刷新</button>
        <router-link class="btn sm" to="/transactions">记一笔</router-link>
      </div>
    </div>

    <div v-if="app.demo && !loading" class="demo-banner">
      <span class="db-ico">i</span>
      <div class="db-text">
        <b>当前为演示数据</b>：系统自动生成的示例持仓与行情，并非你的真实资产。
        <router-link to="/positions">去添加我的标的</router-link>，添加首个标的后即退出演示模式。
      </div>
    </div>

    <!-- Next-①: quote transparency banner -->
    <div v-if="summary && (summary.quoteSimCount > 0 || summary.quoteStaleCount > 0)" class="quote-banner">
      <span class="qb-ico">⚠</span>
      <div>
        当前有 <b>{{ summary.quoteSimCount }}</b> 个标的为模拟行情<template v-if="summary.quoteStaleCount > 0">，<b>{{ summary.quoteStaleCount }}</b> 个行情获取失败（显示上次/模拟价）</template>，价格非真实市场价，仅供参考。
      </div>
    </div>

    <div v-if="loading" class="empty">加载中…</div>

    <div v-else-if="!summary" class="empty">加载失败，请<a href="#" @click.prevent="loadAll()">点击重试</a></div>

    <template v-else>
      <div class="grid cards-4">
        <StatCard label="总资产（含现金）" :value="money(summary?.totalAssets, cur)" :delta="`现金 ${money(summary?.cashTotal || 0, cur)} · 占比 ${((summary?.cashRatio || 0) * 100).toFixed(1)}%`" :loading="false" />
        <StatCard label="投资市值" :value="money(summary?.investmentValue, cur)" :delta="`成本 ${money(summary?.totalCost || 0, cur)}`" :loading="false" />
        <StatCard label="浮动盈亏" :value="signed(summary?.totalFloatingPnl, cur)" :delta="summary?.totalReturn != null ? `总收益率 ${pct(summary.totalReturn)}` : '暂无成本基准'" :deltaClass="dirClass(summary?.totalFloatingPnl)" :loading="false" />
        <StatCard label="今日盈亏" :value="signed(summary?.dayPnl, cur)" :delta="`已实现 ${signed(summary?.totalRealizedPnl || 0, cur)}`" :deltaClass="dirClass(summary?.dayPnl)" :loading="false" />
      </div>

      <div class="grid cards-3 section">
        <div class="card chart-wrap">
          <div class="section-title">资产分布</div>
          <EChart :option="donutOption" height="270px" />
        </div>
        <div class="card chart-wrap span2">
          <div class="flex between center">
            <div class="section-title" style="margin: 0">资产趋势</div>
            <div class="flex gap8 center">
              <select v-if="trend && trend.benchmarkLabel" :value="benchmark" @change="changeBenchmark($event.target.value)" class="mini-sel">
                <option value="">无基准</option>
                <option :value="trend.benchmarkLabel">{{ trend.benchmarkLabel }}</option>
              </select>
              <div class="tabs" style="border: none; margin: 0">
                <div v-for="r in ['7d', '30d', '90d', 'all']" :key="r" class="tab" :class="{ active: range === r }" @click="range = r">
                  {{ r === 'all' ? '全部' : r.replace('d', '天') }}
                </div>
              </div>
            </div>
          </div>
          <EChart :option="trendOption" height="250px" />
          <!-- Later-②: excess return vs benchmark -->
          <div v-if="trend && trend.excessReturn != null" class="excess" :class="dirClass(trend.excessReturn)">
            相对基准 {{ trend.benchmarkLabel }} 超额收益：{{ pctRaw(trend.excessReturn * 100) }}
          </div>
        </div>
      </div>

      <div class="card section">
        <div class="card-pad section-title" style="margin: 0">分类概览（实时）</div>
        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>分类</th><th>标的数</th><th>市值</th><th>成本</th>
                <th>浮动盈亏</th><th>收益率</th><th>已实现</th><th>今日最强 / 最弱</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="c in catRows" :key="c.category">
                <td><span class="link" @click="goCat(c.category)">{{ c.label || CATEGORY_LABEL[c.category] }}</span></td>
                <td class="num">{{ c.count }}</td>
                <td class="num">{{ money(c.marketValue, cur) }}</td>
                <td class="num">{{ money(c.costTotal, cur) }}</td>
                <td class="num" :class="dirClass(c.floatingPnl)">{{ signed(c.floatingPnl, cur) }}</td>
                <td class="num" :class="dirClass(c.floatingPct)">{{ c.floatingPct != null ? pct(c.floatingPct) : '--' }}</td>
                <td class="num" :class="dirClass(c.realizedPnl)">{{ signed(c.realizedPnl, cur) }}</td>
                <td>
                  <template v-if="c.top && c.top.up">
                    <span class="up">{{ c.top.up.name }} {{ pctRaw(c.top.up.chgPct) }}</span>
                    <span class="muted"> / </span>
                    <span class="down">{{ c.top.down.name }} {{ pctRaw(c.top.down.chgPct) }}</span>
                  </template>
                  <span v-else class="muted">--</span>
                </td>
              </tr>
              <tr v-if="!catRows.length"><td colspan="8" class="muted" style="text-align: center">暂无数据</td></tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.demo-banner {
  display: flex; gap: 10px; align-items: flex-start;
  font-size: 12.5px; line-height: 1.6; padding: 11px 14px; border-radius: 10px;
  margin-bottom: 16px; background: #fdf3e0; color: #8a5a00; border: 1px solid #f3dca8;
}
.demo-banner .db-ico {
  width: 18px; height: 18px; flex-shrink: 0; border-radius: 50%; margin-top: 1px;
  display: grid; place-items: center; font-size: 12px; font-weight: 700; color: #fff; background: var(--warn);
}
.demo-banner a { color: #8a5a00; font-weight: 700; text-decoration: underline; }
.quote-banner {
  display: flex; gap: 10px; align-items: flex-start;
  font-size: 12.5px; line-height: 1.6; padding: 10px 14px; border-radius: 10px;
  margin-bottom: 16px; background: #fff7ed; color: #9a4a00; border: 1px solid #ffd8a8;
}
.quote-banner .qb-ico { font-size: 14px; margin-top: 1px; }
.excess { margin-top: 8px; font-size: 13px; font-weight: 600; }
.mini-sel { font-size: 12px; padding: 3px 6px; border-radius: 7px; border: 1px solid var(--line); background: #fff; color: var(--ink); }
</style>
