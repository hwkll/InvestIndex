<script setup>
import { computed, onMounted, ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import { Api } from '../api';
import { useApp } from '../store';
import EChart from '../components/EChart.vue';
import {
  CATEGORY_LABEL, date, dirClass, money, num, pct, pctRaw, price, signed, SIGNAL_LABEL, signalClass,
} from '../format';

const app = useApp();
const route = useRoute();
const id = computed(() => route.params.id);

const pos = ref(null);
const ind = ref(null);
const kl = ref([]);
const txs = ref([]);
const analysis = ref(null);
const loading = ref(true);
const analyzing = ref(false);
const limit = ref(120);

async function loadAll() {
  loading.value = true;
  try {
    const [p, i, k, t, a] = await Promise.all([
      Api.position(id.value).catch(() => null),
      Api.indicators(id.value).catch(() => null),
      Api.kline(id.value, '1d', limit.value).catch(() => []),
      Api.transactions({ asset_id: id.value, size: 10 }).catch(() => ({ items: [] })),
      Api.analyses({ scope: 'asset', asset_id: id.value, size: 1 }).catch(() => null),
    ]);
    pos.value = p;
    ind.value = i;
    kl.value = k || [];
    txs.value = t?.items || [];
    analysis.value = a?.items?.[0] || null;
  } catch (e) {
    app.toast('加载失败', e.message, 'error');
  } finally {
    loading.value = false;
  }
}

onMounted(async () => { await loadAll(); await loadWatched(); });
watch(id, async () => { await loadAll(); await loadWatched(); });
watch(limit, async () => { kl.value = await Api.kline(id.value, '1d', limit.value); });

// ---- 自选 (Next-③) ----
const watchedId = ref('');
async function loadWatched() {
  try {
    const r = await Api.watchlist();
    const found = r.find((w) => w.assetId === id.value);
    watchedId.value = found ? found.id : '';
  } catch { /* ignore */ }
}
async function toggleWatch() {
  try {
    if (watchedId.value) {
      await Api.removeWatch(watchedId.value);
      watchedId.value = '';
    } else {
      const r = await Api.addWatch({ assetId: id.value });
      watchedId.value = r.id || '';
    }
  } catch (e) {
    app.toast('操作失败', e.message, 'error');
  }
}

const livePos = computed(() => (pos.value ? app.live(pos.value) : null));

function sma(arr, n) {
  const out = [];
  let sum = 0;
  for (let i = 0; i < arr.length; i++) {
    sum += arr[i];
    if (i >= n) sum -= arr[i - n];
    out.push(i >= n - 1 ? +(sum / n).toFixed(6) : null);
  }
  return out;
}

const klineOption = computed(() => {
  const rows = kl.value;
  const dates = rows.map((c) => date(c.ts));
  const ohlc = rows.map((c) => [c.open, c.close, c.low, c.high]);
  const closes = rows.map((c) => c.close);
  const vols = rows.map((c, i) => ({ value: c.volume, itemStyle: { color: i > 0 && closes[i] < closes[i - 1] ? '#1faa59' : '#e23b3b' } }));
  return {
    animation: false,
    tooltip: { trigger: 'axis', axisPointer: { type: 'cross' }, backgroundColor: 'rgba(255,255,255,.96)', borderColor: '#e6eaf1', textStyle: { color: '#18202e', fontSize: 12 } },
    legend: { top: 0, right: 0, data: ['K线', 'MA5', 'MA20', 'MA60'], textStyle: { color: '#6b7686', fontSize: 11 } },
    axisPointer: { link: [{ xAxisIndex: 'all' }] },
    grid: [
      { left: 8, right: 12, top: 30, height: '58%', containLabel: true },
      { left: 8, right: 12, bottom: 30, height: '14%', containLabel: true },
    ],
    xAxis: [
      { type: 'category', data: dates, boundaryGap: true, axisLine: { lineStyle: { color: '#e6eaf1' } }, axisLabel: { color: '#9aa3b2', fontSize: 10 } },
      { type: 'category', gridIndex: 1, data: dates, axisLine: { lineStyle: { color: '#e6eaf1' } }, axisLabel: { show: false } },
    ],
    yAxis: [
      { scale: true, splitLine: { lineStyle: { color: '#f0f2f7' } }, axisLabel: { color: '#9aa3b2', fontSize: 10 } },
      { gridIndex: 1, splitNumber: 2, axisLabel: { show: false }, splitLine: { show: false } },
    ],
    dataZoom: [
      { type: 'inside', xAxisIndex: [0, 1], start: 55, end: 100 },
      { type: 'slider', xAxisIndex: [0, 1], bottom: 0, height: 18, start: 55, end: 100, borderColor: '#e6eaf1' },
    ],
    series: [
      {
        name: 'K线', type: 'candlestick', data: ohlc,
        itemStyle: { color: '#e23b3b', color0: '#1faa59', borderColor: '#e23b3b', borderColor0: '#1faa59' },
      },
      { name: 'MA5', type: 'line', data: sma(closes, 5), smooth: true, showSymbol: false, lineStyle: { width: 1, color: '#e08a00' } },
      { name: 'MA20', type: 'line', data: sma(closes, 20), smooth: true, showSymbol: false, lineStyle: { width: 1, color: '#3b5bff' } },
      { name: 'MA60', type: 'line', data: sma(closes, 60), smooth: true, showSymbol: false, lineStyle: { width: 1, color: '#8a63d2' } },
      { name: '成交量', type: 'bar', xAxisIndex: 1, yAxisIndex: 1, data: vols },
    ],
  };
});

const indRows = computed(() => {
  const i = ind.value;
  if (!i) return [];
  const f = (v, d = 2) => (v === null || v === undefined || Number.isNaN(v) ? '--' : Number(v).toFixed(d));
  const out = [
    { k: 'RSI(14)', v: f(i.rsi), hint: i.rsi == null ? '' : i.rsi > 70 ? '超买' : i.rsi < 30 ? '超卖' : '中性' },
    { k: 'MACD', v: f(i.macd, 4), hint: i.macdHist == null ? '' : i.macdHist > 0 ? '多头' : '空头' },
    { k: 'MACD 信号', v: f(i.macdSignal, 4) },
    { k: 'KDJ · K', v: f(i.kdjK) },
    { k: 'KDJ · D', v: f(i.kdjD) },
    { k: 'KDJ · J', v: f(i.kdjJ) },
    { k: 'BOLL 上轨', v: f(i.bollUpper, 4) },
    { k: 'BOLL 中轨', v: f(i.bollMid, 4) },
    { k: 'BOLL 下轨', v: f(i.bollLower, 4) },
  ];
  Object.entries(i.ma || {}).forEach(([k, v]) => out.push({ k: 'MA' + k, v: f(v, 4) }));
  if (i.maxDrawdown != null) out.push({ k: '最大回撤', v: pct(-Math.abs(i.maxDrawdown)) });
  if (i.annualReturn != null) out.push({ k: '年化收益', v: pct(i.annualReturn) });
  if (i.volatility != null) out.push({ k: '年化波动', v: pct(i.volatility) });
  if (i.sharpe != null) out.push({ k: '夏普比率', v: f(i.sharpe) });
  return out;
});

async function analyze() {
  if (!localStorage.getItem('ih_ai_egress_ack')) {
    if (!confirm('数据出境提示：AI 分析会将相关持仓与行情数据发送至 DeepSeek 境外服务器处理。确认你已了解并同意该跨境数据传输？')) {
      return;
    }
    localStorage.setItem('ih_ai_egress_ack', '1');
  }
  analyzing.value = true;
  try {
    const r = await Api.analyze({ scope: 'asset', assetId: id.value });
    analysis.value = { ...r, conclusion: r.conclusion, createdAt: r.createdAt, model: r.model };
    if (r.degraded) app.toast('已降级为本地分析', r.notice || '', 'alert');
  } catch (e) {
    if (e.code === 40301) {
      app.toast('需要配置', '请先在「设置 → AI 分析」配置 DeepSeek API Key 后重试', 'alert');
      return;
    }
    app.toast('分析失败', e.message, 'error');
  } finally {
    analyzing.value = false;
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="page-title">
          {{ livePos?.name || '标的详情' }}
          <span class="tag">{{ livePos?.symbol }}</span>
          <span class="tag">{{ CATEGORY_LABEL[livePos?.category] || livePos?.category }}</span>
        </div>
        <div class="page-sub">
          现价 <b class="num">{{ livePos?.quoteStatus === 'nosource' ? '—' : price(livePos?.price) }}</b>
          <span v-if="livePos?.quoteStatus !== 'nosource'" class="num" :class="dirClass(livePos?.chgPct)"> {{ pctRaw(livePos?.chgPct) }}</span>
          <span v-if="livePos?.quoteStatus === 'sim'" class="tag" title="当前为模拟行情，价格非真实市场价">模拟行情</span>
          <span v-else-if="livePos?.quoteStatus === 'stale'" class="tag warn" title="真实行情获取失败，当前显示的是上次缓存或模拟价格">行情失效</span>
          <span v-else-if="livePos?.quoteStatus === 'nosource'" class="tag warn" title="暂无可用行情源（如场外基金、白名单外币种）">暂无行情源</span>
        </div>
      </div>
      <div class="flex gap8">
        <select v-model.number="limit" style="width: auto">
          <option :value="60">近 60 日</option><option :value="120">近 120 日</option><option :value="200">近 200 日</option>
        </select>
        <button class="btn sm sec" @click="toggleWatch">{{ watchedId ? '已自选 ★' : '加自选 ☆' }}</button>
        <button class="btn" :disabled="analyzing" @click="analyze">
          <span v-if="analyzing" class="spin"></span>{{ analyzing ? ' 分析中' : '✦ AI 分析' }}
        </button>
      </div>
    </div>

    <div v-if="loading" class="empty">加载中…</div>

    <template v-else>
      <div class="grid cards-4">
        <div class="card stat"><div class="label">持仓量</div><div class="value num">{{ livePos?.qty > 0 ? price(livePos?.qty) : '--' }}</div><div class="delta muted">成本价 {{ livePos?.qty > 0 ? price(livePos?.avgCost) : '--' }}</div></div>
        <div class="card stat"><div class="label">市值</div><div class="value num">{{ money(livePos?.marketValue, livePos?.currency) }}</div><div class="delta muted">成本 {{ money(livePos?.costTotal, livePos?.currency) }}</div></div>
        <div class="card stat"><div class="label">浮动盈亏</div><div class="value num" :class="dirClass(livePos?.floatingPnl)">{{ signed(livePos?.floatingPnl, livePos?.currency) }}</div><div class="delta" :class="dirClass(livePos?.floatingPct)">{{ livePos?.floatingPct != null ? pct(livePos?.floatingPct) : '--' }}</div></div>
        <div class="card stat"><div class="label">累计盈亏</div><div class="value num" :class="dirClass(livePos?.accumulatedPnl)">{{ signed(livePos?.accumulatedPnl, livePos?.currency) }}</div><div class="delta muted">已实现 {{ signed(livePos?.realizedPnl, livePos?.currency) }} · 持有 {{ livePos?.daysHeld || 0 }} 天</div></div>
      </div>

      <div class="card section chart-wrap">
        <div class="section-title">K 线与均线</div>
        <EChart :option="klineOption" height="420px" />
      </div>

      <div class="grid cards-2 section">
        <div class="card card-pad">
          <div class="section-title">技术指标</div>
          <div class="kpi-row">
            <div v-for="r in indRows" :key="r.k" class="kpi">
              <div class="k">{{ r.k }}</div>
              <div class="v num">{{ r.v }}<span v-if="r.hint" class="tag">{{ r.hint }}</span></div>
            </div>
          </div>
          <div v-if="!indRows.length" class="muted mt8">暂无足够历史数据计算指标</div>
        </div>

        <div class="card card-pad">
          <div class="section-title">AI 结论</div>
          <template v-if="analysis && analysis.conclusion">
            <div class="flex between center">
              <div class="strong" :class="signalClass(analysis.conclusion.signal)" style="font-size: 18px">
                {{ SIGNAL_LABEL[analysis.conclusion.signal] || analysis.conclusion.signal }}
              </div>
              <div class="muted">置信度 {{ ((analysis.conclusion.confidence || 0) * 100).toFixed(0) }}% · {{ analysis.model }}</div>
            </div>
            <div class="bar mt8"><i :style="{ width: ((analysis.conclusion.confidence || 0) * 100) + '%' }"></i></div>
            <div class="flex mt12" style="gap:18px" v-if="analysis.conclusion.currentPrice || analysis.conclusion.targetPrice">
              <div v-if="analysis.conclusion.currentPrice" style="display:flex;flex-direction:column;gap:2px">
                <span class="muted" style="font-size:12px">当前价</span>
                <b class="num">{{ price(analysis.conclusion.currentPrice) }}</b>
              </div>
              <div v-if="analysis.conclusion.targetPrice" style="display:flex;flex-direction:column;gap:2px">
                <span class="muted" style="font-size:12px">目标价</span>
                <b class="num up">{{ price(analysis.conclusion.targetPrice) }}</b>
              </div>
            </div>
            <p class="mt12" style="line-height: 1.7">{{ analysis.conclusion.summary }}</p>
            <div v-if="analysis.conclusion.reasons?.length">
              <div class="muted mt8">主要理由</div>
              <ul class="reasons"><li v-for="(r, i) in analysis.conclusion.reasons" :key="i">{{ r }}</li></ul>
            </div>
            <div v-if="analysis.conclusion.risks?.length">
              <div class="muted mt8">风险提示</div>
              <ul class="reasons"><li v-for="(r, i) in analysis.conclusion.risks" :key="i">{{ r }}</li></ul>
            </div>
            <div class="muted mt8" style="font-size: 12px">{{ date(analysis.createdAt, true) }} · 分析结果仅供参考，不构成投资建议</div>
          </template>
          <div v-else class="empty"><div class="big">✦</div>尚无分析记录，点击右上角「AI 分析」生成</div>
        </div>
      </div>

      <div class="card section">
        <div class="card-pad section-title" style="margin: 0">最近交易</div>
        <div class="table-wrap">
          <table>
            <thead><tr><th>日期</th><th>方向</th><th>数量</th><th>价格</th><th>手续费</th><th>金额</th><th>来源</th></tr></thead>
            <tbody>
              <tr v-for="t in txs" :key="t.id">
                <td>{{ date(t.tradeTime) }}</td>
                <td><span class="pill" :class="t.direction === 'buy' ? 'red' : 'green'">{{ t.direction === 'buy' ? '买入' : '卖出' }}</span></td>
                <td class="num">{{ price(t.quantity) }}</td>
                <td class="num">{{ price(t.price) }}</td>
                <td class="num">{{ num(t.fee) }}</td>
                <td class="num">{{ money(t.quantity * t.price, livePos.currency) }}</td>
                <td class="muted">{{ t.source }}</td>
              </tr>
              <tr v-if="!txs.length"><td colspan="7" class="muted" style="text-align: center">暂无交易记录</td></tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>
  </div>
</template>
