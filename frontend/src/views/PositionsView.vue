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

const assetForm = reactive({ id: '', category: 'crypto', name: '', symbol: '', subType: '', market: '', currency: '', remark: '' });

// 分类元数据：驱动新增/编辑标的弹窗的动态字段与默认值。
const GOLD_TYPES = [
  { value: 'physical', label: '实物金/纸黄金', unit: '克', currency: 'CNY', codeHint: '可不填代码（按上海金交所 Au99.99 现货价 CNY/克）' },
  { value: 'sge', label: '上海金交所 Au(T+D)', unit: '克', currency: 'CNY', codeHint: '可不填代码（按 SGE Au(T+D) 现货价 CNY/克）' },
  { value: 'xau', label: 'XAU 伦敦金', unit: '盎司', currency: 'USD', codeHint: '填 XAU（按国际现货金 USD/盎司）' },
  { value: 'etf', label: '黄金 ETF', unit: '股', currency: 'CNY', codeHint: '填 6 位代码，如 518880' },
];
const STOCK_MARKETS = [
  { value: 'ashare', label: 'A股', currency: 'CNY', hint: '如 sh.600519 或 sz.000001' },
  { value: 'hk', label: '港股', currency: 'CNY', hint: '如 9866.HK' },
  { value: 'us', label: '美股', currency: 'USD', hint: '如 AAPL' },
];
const FUND_TYPES = [
  { value: 'etf', label: 'ETF（场内）' },
  { value: '场外', label: '场外基金' },
];

const goldType = computed(() => GOLD_TYPES.find(t => t.value === assetForm.subType) || null);
const stockMarket = computed(() => STOCK_MARKETS.find(m => m.value === assetForm.market) || null);
// 计价单位预览（仅展示用，不影响存储）
const unitHint = computed(() => {
  if (assetForm.category === 'gold' && goldType.value) return goldType.value.unit;
  if (assetForm.category === 'crypto') return '枚';
  if (assetForm.category === 'stock') return '股';
  if (assetForm.category === 'fund') return '份';
  return '';
});
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

// 持仓页计价单位（仅展示用）：黄金按子类型区分克/盎司/股
function unitOf(r) {
  if (r.category === 'gold') {
    if (r.subType === 'xau') return '盎司';
    if (r.subType === 'etf') return '股';
    return '克';
  }
  if (r.category === 'crypto') return '枚';
  if (r.category === 'stock') return '股';
  if (r.category === 'fund') return '份';
  return '';
}

function openAsset(a) {
  // 编辑时由 symbol 反推股票市场（A股 sh./sz.、港股 .hk），黄金/基金直接取 subType
  let market = '';
  const sym = a?.symbol || '';
  if (a?.category === 'stock') {
    if (sym.toLowerCase().endsWith('.hk')) market = 'hk';
    else if (/^sh\.|^sz\./i.test(sym)) market = 'ashare';
    else if (/^[a-z]{1,5}$/i.test(sym)) market = 'us';
  }
  Object.assign(assetForm, {
    id: a?.assetId || '',
    category: a ? a.category : cat.value,
    name: a?.name || '',
    symbol: a?.symbol || '',
    subType: a?.subType || '',
    market,
    currency: a?.currency || '',
    remark: '',
  });
  showAsset.value = true;
}

// 切换分类时清空串类字段并带出默认币种
watch(() => assetForm.category, (c) => {
  assetForm.symbol = '';
  assetForm.subType = '';
  assetForm.market = '';
  if (c === 'crypto') assetForm.currency = 'USD';
  else if (c === 'gold') assetForm.currency = 'CNY';
  else if (c === 'stock') assetForm.currency = 'CNY';
  else if (c === 'fund') assetForm.currency = 'CNY';
});
// 选股票市场后带出对应币种
watch(() => assetForm.market, (m) => {
  const mk = STOCK_MARKETS.find(x => x.value === m);
  if (mk) assetForm.currency = mk.currency;
});
// 选黄金类型后带出对应币种
watch(() => assetForm.subType, (st) => {
  if (assetForm.category === 'gold') {
    const gt = GOLD_TYPES.find(x => x.value === st);
    if (gt) assetForm.currency = gt.currency;
  }
});

async function saveAsset() {
  if (!assetForm.name) return app.toast('请填写名称', '', 'error');
  // 按分类拼装 symbol / subType / 默认币种
  let symbol = assetForm.symbol.trim();
  let subType = assetForm.subType || undefined;
  let currency = assetForm.currency || undefined;
  if (assetForm.category === 'stock') {
    if (!assetForm.market) return app.toast('请选择股票市场', '', 'error');
    if (!symbol) return app.toast('请填写代码', '', 'error');
    if (assetForm.market === 'hk' && !/\.hk$/i.test(symbol)) symbol += '.HK';
    if (assetForm.market === 'ashare' && !/^\w+\.\d+$/i.test(symbol)) {
      // 用户只填了 6 位数字，自动补交易所前缀
      const code = symbol.replace(/\W/g, '');
      if (/^\d{6}$/.test(code)) symbol = (code[0] === '6' || code[0] === '5' ? 'sh.' : 'sz.') + code;
    }
  } else if (assetForm.category === 'gold') {
    if (!subType) subType = 'physical'; // 默认实物金/纸黄金
    if (!currency) currency = GOLD_TYPES.find(x => x.value === subType)?.currency || 'CNY';
    // physical / sge / xau 允许不填代码，但后端要求 symbol 必填，故空时补默认代码，
    // 避免被「代码必填」拒绝；etf 必须填 6 位 ETF 代码。
    if (!symbol) {
      if (subType === 'physical') symbol = 'Au99.99';
      else if (subType === 'sge') symbol = 'AuTD';
      else if (subType === 'xau') symbol = 'XAUUSD';
      else return app.toast('请填写黄金 ETF 代码', '', 'error');
    }
  } else if (assetForm.category === 'crypto') {
    if (!symbol) return app.toast('请填写代码', '', 'error');
    currency = currency || 'USD';
  } else if (assetForm.category === 'fund') {
    if (!symbol) return app.toast('请填写代码', '', 'error');
    currency = currency || 'CNY';
  }
  busy.value = true;
  try {
    const body = {
      category: assetForm.category, name: assetForm.name, symbol,
      subType, currency, remark: assetForm.remark || undefined,
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
                <span v-if="r.quoteStatus === 'stale'" class="tag warn" title="真实行情获取失败，当前显示上次缓存价">行情失效</span>
                <span v-else-if="r.quoteStatus === 'nosource'" class="tag warn" title="暂无可用行情源（如场外基金、白名单外币种）">无源</span>
              </td>
              <td class="num">{{ r.qty > 0 ? price(r.qty) : '--' }} <span class="muted sm">{{ unitOf(r) }}</span></td>
              <td class="num">{{ r.qty > 0 ? price(r.avgCost) : '--' }} <span class="muted sm">{{ unitOf(r) }}</span></td>
              <td class="num strong">{{ r.quoteStatus === 'nosource' ? '—' : price(r.price) }} <span class="muted sm">{{ unitOf(r) }}<template v-if="r.subType === 'xau' && r.fxRate > 0 && r.price > 0"> · ≈¥{{ (r.price * r.fxRate / 31.1035).toFixed(2) }}/g</template></span></td>
              <td class="num" :class="dirClass(r.chgPct)">{{ pctRaw(r.chgPct) }}</td>
              <td class="num" :title="r.currency !== 'CNY' ? (money(r.marketValue, r.currency) + '（' + r.fxNote + '）') : ''">{{ r.currency === 'CNY' ? money(r.marketValue, 'CNY') : money(r.marketValueCny, 'CNY') }}</td>
              <td class="num" :class="dirClass(r.currency !== 'CNY' ? r.floatingPnlCny : r.floatingPnl)" :title="r.currency !== 'CNY' ? (signed(r.floatingPnl, r.currency) + '（' + r.fxNote + '）') : ''">{{ r.currency === 'CNY' ? signed(r.floatingPnl, 'CNY') : signed(r.floatingPnlCny, 'CNY') }}</td>
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
      <div class="field">
        <label>分类</label>
        <select v-model="assetForm.category" :disabled="!!assetForm.id">
          <option v-for="c in CATEGORIES" :key="c" :value="c">{{ CATEGORY_LABEL[c] }}</option>
        </select>
      </div>

      <div class="field"><label>名称</label><input v-model="assetForm.name" placeholder="如：比特币 / 贵州茅台 / 实物金" /></div>

      <!-- 加密货币 -->
      <template v-if="assetForm.category === 'crypto'">
        <div class="field">
          <label>代码</label>
          <input v-model="assetForm.symbol" placeholder="BTC / ETH / SOL" />
          <div class="hint">交易所 ticker，如 BTC、ETH、SOL（默认 USD 计价）</div>
        </div>
        <div class="field"><label>币种</label>
          <select v-model="assetForm.currency"><option value="USD">USD</option><option value="CNY">CNY</option></select>
        </div>
      </template>

      <!-- 股票 -->
      <template v-else-if="assetForm.category === 'stock'">
        <div class="field"><label>市场</label>
          <select v-model="assetForm.market">
            <option v-for="m in STOCK_MARKETS" :key="m.value" :value="m.value">{{ m.label }}</option>
          </select>
        </div>
        <div class="field">
          <label>代码</label>
          <input v-model="assetForm.symbol" :placeholder="stockMarket ? stockMarket.hint : ''" />
          <div class="hint" v-if="stockMarket">{{ stockMarket.hint }}<span v-if="assetForm.market === 'hk'">（自动补 .HK）</span></div>
        </div>
        <div class="field"><label>币种</label>
          <select v-model="assetForm.currency"><option value="CNY">CNY</option><option value="USD">USD</option><option value="HKD">HKD</option></select>
        </div>
      </template>

      <!-- 基金 -->
      <template v-else-if="assetForm.category === 'fund'">
        <div class="field"><label>类型</label>
          <select v-model="assetForm.subType">
            <option v-for="f in FUND_TYPES" :key="f.value" :value="f.value">{{ f.label }}</option>
          </select>
        </div>
        <div class="field">
          <label>代码</label>
          <input v-model="assetForm.symbol" placeholder="6 位代码，如 510300" />
          <div class="hint">ETF 用 6 位代码（如 510300）；场外基金用天天基金代码</div>
        </div>
        <div class="field"><label>币种</label>
          <select v-model="assetForm.currency"><option value="CNY">CNY</option></select>
        </div>
      </template>

      <!-- 黄金 -->
      <template v-else-if="assetForm.category === 'gold'">
        <div class="field"><label>黄金类型</label>
          <select v-model="assetForm.subType">
            <option v-for="g in GOLD_TYPES" :key="g.value" :value="g.value">{{ g.label }}</option>
          </select>
        </div>
        <div class="field">
          <label>代码 / 标识</label>
          <input v-model="assetForm.symbol" :placeholder="goldType ? goldType.codeHint : ''" />
          <div class="hint" v-if="goldType">{{ goldType.codeHint }}</div>
        </div>
        <div class="field"><label>币种</label>
          <select v-model="assetForm.currency"><option value="CNY">CNY</option><option value="USD">USD</option></select>
        </div>
        <div class="hint" v-if="unitHint">计价单位：{{ unitHint }}<template v-if="goldType">（{{ goldType.label }}）</template></div>
      </template>

      <div class="field"><label>备注</label><input v-model="assetForm.remark" placeholder="可选" /></div>
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
