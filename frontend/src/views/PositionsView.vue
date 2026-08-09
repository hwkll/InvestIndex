<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Api } from '../api';
import { useApp } from '../store';
import ModalDialog from '../components/ModalDialog.vue';
import { CATEGORIES, CATEGORY_LABEL, dateInput, dirClass, money, pct, pctRaw, price, signed } from '../format';

const app = useApp();
const route = useRoute();
const router = useRouter();

const cat = ref(route.query.category && CATEGORIES.includes(route.query.category) ? route.query.category : 'crypto');
const data = ref(null);
const loading = ref(true);

const showAsset = ref(false);
const showTrade = ref(false);
const busy = ref(false);

const assetForm = reactive({ id: '', category: 'crypto', name: '', symbol: '', subType: '', currency: '', remark: '' });
const tradeForm = reactive({ assetId: '', assetName: '', direction: 'buy', quantity: '', price: '', fee: '0', date: dateInput(), remark: '' });

async function load() {
  loading.value = true;
  try {
    data.value = await Api.positions(cat.value);
  } catch (e) {
    app.toast('加载失败', e.message, 'error');
  } finally {
    loading.value = false;
  }
}

onMounted(async () => {
  await load();
  await loadWatched();
});
watch(cat, () => {
  router.replace({ query: { category: cat.value } });
  load();
});

const rows = computed(() => (data.value?.items || []).map((r) => app.live(r)));
const sub = computed(() => data.value?.subtotal || {});
const cur = computed(() => sub.value.currency || 'CNY');

function openAsset(a) {
  Object.assign(assetForm, {
    id: a?.assetId || '',
    category: a ? a.category : cat.value,
    name: a?.name || '',
    symbol: a?.symbol || '',
    subType: a?.subType || '',
    currency: a?.currency || '',
    remark: '',
  });
  showAsset.value = true;
}

async function saveAsset() {
  if (!assetForm.name || !assetForm.symbol) return app.toast('请填写名称与代码', '', 'error');
  busy.value = true;
  try {
    const body = {
      category: assetForm.category, name: assetForm.name, symbol: assetForm.symbol,
      subType: assetForm.subType || undefined, currency: assetForm.currency || undefined, remark: assetForm.remark || undefined,
    };
    if (assetForm.id) await Api.updateAsset(assetForm.id, body);
    else await Api.createAsset(body);
    showAsset.value = false;
    app.toast('已保存', assetForm.name, 'success');
    if (assetForm.category !== cat.value) cat.value = assetForm.category;
    else await load();
  } catch (e) {
    app.toast('保存失败', e.message, 'error');
  } finally {
    busy.value = false;
  }
}

function openTrade(row, direction) {
  Object.assign(tradeForm, {
    assetId: row.assetId, assetName: row.name, direction,
    quantity: '', price: row.quoteStatus === 'ok' ? String(row.price) : '', fee: '0', date: dateInput(), remark: '',
  });
  showTrade.value = true;
}

async function saveTrade() {
  const qty = parseFloat(tradeForm.quantity);
  const px = parseFloat(tradeForm.price);
  if (!(qty > 0) || !(px >= 0)) return app.toast('数量与价格必须为正数', '', 'error');
  busy.value = true;
  try {
    await Api.createTx({
      assetId: tradeForm.assetId,
      direction: tradeForm.direction,
      quantity: qty,
      price: px,
      fee: parseFloat(tradeForm.fee) || 0,
      tradeTime: new Date(tradeForm.date + 'T12:00:00').getTime(),
      remark: tradeForm.remark || undefined,
    });
    showTrade.value = false;
    app.toast('已记录', `${tradeForm.direction === 'buy' ? '买入' : '卖出'} ${tradeForm.assetName}`, 'success');
    await load();
  } catch (e) {
    app.toast('记录失败', e.message, 'error');
  } finally {
    busy.value = false;
  }
}

async function removeAsset(row) {
  if (!await app.confirm({ title: '删除标的', message: `确认删除「${row.name}」？该标的的交易流水也会一并删除。`, danger: true })) return;
  try {
    await Api.deleteAsset(row.assetId, 'hard');
    app.toast('已删除', row.name, 'success');
    await load();
  } catch (e) {
    app.toast('删除失败', e.message, 'error');
  }
}

// ---- 自选 (Next-③) ----
const watched = ref({}); // assetId -> watchId
async function loadWatched() {
  try {
    const r = await Api.watchlist();
    const m = {};
    r.forEach((w) => { m[w.assetId] = w.id; });
    watched.value = m;
  } catch { /* ignore */ }
}
async function toggleWatch(row) {
  const id = watched.value[row.assetId];
  try {
    if (id) {
      await Api.removeWatch(id);
      delete watched.value[row.assetId];
    } else {
      const r = await Api.addWatch({ assetId: row.assetId });
      if (r.id) watched.value[row.assetId] = r.id;
    }
    watched.value = { ...watched.value };
  } catch (e) {
    app.toast('操作失败', e.message, 'error');
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="page-title">持仓</div>
        <div class="page-sub">按分类查看持仓、成本与浮动盈亏（成本采用移动加权平均）</div>
      </div>
      <div class="flex gap8">
        <button class="btn sec sm" @click="load">刷新</button>
        <button class="btn sm" @click="openAsset(null)">新增标的</button>
      </div>
    </div>

    <div class="tabs">
      <div v-for="c in CATEGORIES" :key="c" class="tab" :class="{ active: cat === c }" @click="cat = c">
        {{ CATEGORY_LABEL[c] }}
      </div>
    </div>

    <div class="grid cards-4 mt8">
      <div class="card stat"><div class="label">市值合计</div><div class="value num">{{ money(sub.marketValue, cur) }}</div></div>
      <div class="card stat"><div class="label">成本合计</div><div class="value num">{{ money(sub.costTotal, cur) }}</div></div>
      <div class="card stat">
        <div class="label">浮动盈亏</div>
        <div class="value num" :class="dirClass(sub.floatingPnl)">{{ signed(sub.floatingPnl, cur) }}</div>
        <div class="delta" :class="dirClass(sub.floatingPct)">{{ sub.floatingPct != null ? pct(sub.floatingPct) : '--' }}</div>
      </div>
      <div class="card stat">
        <div class="label">已实现盈亏</div>
        <div class="value num" :class="dirClass(sub.realizedPnl)">{{ signed(sub.realizedPnl, cur) }}</div>
      </div>
    </div>

    <div class="card section">
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>标的</th><th>持仓量</th><th>成本价</th><th>现价</th><th>涨跌</th>
              <th>市值</th><th>浮动盈亏</th><th>收益率</th><th>持有天数</th><th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="loading"><td colspan="10" class="muted" style="text-align: center">加载中…</td></tr>
            <tr v-for="r in rows" v-else :key="r.assetId">
              <td>
                <router-link class="link" :to="`/assets/${r.assetId}`">{{ r.name }}</router-link>
                <span class="tag">{{ r.symbol }}</span>
                <span v-if="r.quoteStatus === 'sim'" class="tag" title="当前为模拟行情，价格非真实市场价">模拟</span>
                <span v-else-if="r.quoteStatus === 'stale'" class="tag warn" title="真实行情获取失败，当前显示的是上次缓存或模拟价格">行情失效</span>
                <span v-else-if="r.quoteStatus === 'nosource'" class="tag warn" title="暂无可用行情源（如场外基金、白名单外币种）">无源</span>
              </td>
              <td class="num">{{ r.qty > 0 ? price(r.qty) : '--' }}</td>
              <td class="num">{{ r.qty > 0 ? price(r.avgCost) : '--' }}</td>
              <td class="num strong">{{ r.quoteStatus === 'nosource' ? '—' : price(r.price) }}</td>
              <td class="num" :class="dirClass(r.chgPct)">{{ pctRaw(r.chgPct) }}</td>
              <td class="num">{{ money(r.marketValue, r.currency) }}</td>
              <td class="num" :class="dirClass(r.floatingPnl)">{{ signed(r.floatingPnl, r.currency) }}</td>
              <td class="num" :class="dirClass(r.floatingPct)">{{ r.floatingPct != null ? pct(r.floatingPct) : '--' }}</td>
              <td class="num muted">{{ r.daysHeld || '--' }}</td>
              <td>
                <div class="flex gap6" style="justify-content: flex-end">
                  <button class="btn sm sec" @click="toggleWatch(r)">{{ watched[r.assetId] ? '已自选' : '自选' }}</button>
                  <button class="btn sm sec" @click="openTrade(r, 'buy')">买</button>
                  <button class="btn sm ghost2" :disabled="!(r.qty > 0)" @click="openTrade(r, 'sell')">卖</button>
                  <button class="btn sm danger" @click="removeAsset(r)">删</button>
                </div>
              </td>
            </tr>
            <tr v-if="!loading && !rows.length">
              <td colspan="10">
                <div class="empty"><div class="big">◇</div>该分类暂无标的，点击右上角「新增标的」开始</div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <ModalDialog v-if="showAsset" :title="assetForm.id ? '编辑标的' : '新增标的'" :busy="busy" @close="showAsset = false" @ok="saveAsset">
      <div class="row2">
        <div class="field">
          <label>分类</label>
          <select v-model="assetForm.category" :disabled="!!assetForm.id">
            <option v-for="c in CATEGORIES" :key="c" :value="c">{{ CATEGORY_LABEL[c] }}</option>
          </select>
        </div>
        <div class="field">
          <label>币种</label>
          <select v-model="assetForm.currency">
            <option value="">自动（按分类）</option>
            <option value="CNY">CNY</option>
            <option value="USD">USD</option>
            <option value="HKD">HKD</option>
          </select>
        </div>
      </div>
      <div class="field"><label>名称</label><input v-model="assetForm.name" placeholder="如：比特币 / 贵州茅台" /></div>
      <div class="field">
        <label>代码</label>
        <input v-model="assetForm.symbol" placeholder="BTC / sh.600519 / 510300" />
        <div class="hint">加密货币用 BTC、ETH；A 股用 sh.600519 或 sz.000001；基金/ETF 用 6 位代码</div>
      </div>
      <div class="row2">
        <div class="field"><label>子类型</label><input v-model="assetForm.subType" placeholder="etf / 场外 / 实物金 …" /></div>
        <div class="field"><label>备注</label><input v-model="assetForm.remark" placeholder="可选" /></div>
      </div>
    </ModalDialog>

    <ModalDialog
      v-if="showTrade"
      :title="`${tradeForm.direction === 'buy' ? '买入' : '卖出'} · ${tradeForm.assetName}`"
      :busy="busy" ok-text="记录" @close="showTrade = false" @ok="saveTrade"
    >
      <div class="row2">
        <div class="field">
          <label>方向</label>
          <select v-model="tradeForm.direction"><option value="buy">买入</option><option value="sell">卖出</option></select>
        </div>
        <div class="field"><label>成交日期</label><input v-model="tradeForm.date" type="date" /></div>
      </div>
      <div class="row2">
        <div class="field"><label>数量</label><input v-model="tradeForm.quantity" type="number" step="any" placeholder="0" /></div>
        <div class="field"><label>成交价</label><input v-model="tradeForm.price" type="number" step="any" placeholder="0" /></div>
      </div>
      <div class="row2">
        <div class="field"><label>手续费</label><input v-model="tradeForm.fee" type="number" step="any" /></div>
        <div class="field"><label>备注</label><input v-model="tradeForm.remark" placeholder="可选" /></div>
      </div>
    </ModalDialog>
  </div>
</template>
