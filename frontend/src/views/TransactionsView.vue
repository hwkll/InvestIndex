<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { Api } from '../api';
import { useApp } from '../store';
import ModalDialog from '../components/ModalDialog.vue';
import { CATEGORIES, CATEGORY_LABEL, date, dateInput, money, num, price } from '../format';

const app = useApp();

const page = ref(1);
const size = ref(20);
const filter = reactive({ category: '', direction: '', asset_id: '' });
const data = ref({ items: [], total: 0 });
const assets = ref([]);
const loading = ref(true);

const show = ref(false);
const busy = ref(false);
const form = reactive({ id: '', assetId: '', direction: 'buy', quantity: '', price: '', fee: '0', date: dateInput(), remark: '' });

const importOpen = ref(false);
const importText = ref('');

async function load() {
  loading.value = true;
  try {
    data.value = await Api.transactions({
      page: page.value, size: size.value,
      category: filter.category || undefined,
      direction: filter.direction || undefined,
      asset_id: filter.asset_id || undefined,
    });
  } catch (e) {
    app.toast('加载失败', e.message, 'error');
  } finally {
    loading.value = false;
  }
}

async function loadAssets() {
  try { assets.value = await Api.assets(); } catch { /* ignore */ }
}

onMounted(async () => { await loadAssets(); await load(); });
watch([() => filter.category, () => filter.direction, () => filter.asset_id], () => { page.value = 1; load(); });
watch(page, load);

const pages = computed(() => Math.max(1, Math.ceil((data.value.total || 0) / size.value)));

function open(t) {
  Object.assign(form, t
    ? { id: t.id, assetId: t.assetId, direction: t.direction, quantity: String(t.quantity), price: String(t.price), fee: String(t.fee || 0), date: dateInput(t.tradeTime), remark: t.remark || '' }
    : { id: '', assetId: assets.value[0]?.id || '', direction: 'buy', quantity: '', price: '', fee: '0', date: dateInput(), remark: '' });
  show.value = true;
}

async function save() {
  const qty = parseFloat(form.quantity);
  const px = parseFloat(form.price);
  if (!form.assetId) return app.toast('请选择标的', '', 'error');
  if (!(qty > 0) || !(px >= 0)) return app.toast('数量与价格必须为正数', '', 'error');
  busy.value = true;
  try {
    const body = {
      assetId: form.assetId, direction: form.direction, quantity: qty, price: px,
      fee: parseFloat(form.fee) || 0, tradeTime: new Date(form.date + 'T12:00:00').getTime(),
      remark: form.remark || undefined,
    };
    if (form.id) await Api.updateTx(form.id, body);
    else await Api.createTx(body);
    show.value = false;
    app.toast('已保存', '', 'success');
    await load();
  } catch (e) {
    app.toast('保存失败', e.message, 'error');
  } finally {
    busy.value = false;
  }
}

async function remove(t) {
  if (!await app.confirm({ title: '删除交易', message: '确认删除这条交易记录？持仓将重新计算。', danger: true })) return;
  try {
    await Api.deleteTx(t.id);
    app.toast('已删除', '', 'success');
    await load();
  } catch (e) {
    app.toast('删除失败', e.message, 'error');
  }
}

async function doImport() {
  busy.value = true;
  try {
    const r = await Api.importCSV(importText.value);
    app.toast('导入完成', `成功 ${r.imported} 条，跳过 ${r.skipped} 条`, 'success');
    importOpen.value = false;
    importText.value = '';
    await load();
  } catch (e) {
    app.toast('导入失败', e.message, 'error');
  } finally {
    busy.value = false;
  }
}

function onFile(e) {
  const f = e.target.files?.[0];
  if (!f) return;
  // Reject files larger than 10 MB to prevent browser hang.
  if (f.size > 10 * 1024 * 1024) {
    app.toast('文件过大，请选择小于 10 MB 的 CSV 文件', '', 'error');
    return;
  }
  const rd = new FileReader();
  rd.onload = () => { importText.value = String(rd.result || ''); };
  rd.readAsText(f, 'utf-8');
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="page-title">交易流水</div>
        <div class="page-sub">所有买入/卖出记录，持仓与已实现盈亏均由此推导</div>
      </div>
      <div class="flex gap8">
        <a class="btn sec sm" :href="Api.exportCSVUrl('transactions')" download>导出 CSV</a>
        <button class="btn sec sm" @click="importOpen = true">导入 CSV</button>
        <button class="btn sm" @click="open(null)">记一笔</button>
      </div>
    </div>

    <div class="card card-pad">
      <div class="inline-form">
        <select v-model="filter.category">
          <option value="">全部分类</option>
          <option v-for="c in CATEGORIES" :key="c" :value="c">{{ CATEGORY_LABEL[c] }}</option>
        </select>
        <select v-model="filter.direction">
          <option value="">全部方向</option><option value="buy">买入</option><option value="sell">卖出</option>
        </select>
        <select v-model="filter.asset_id" style="min-width: 180px">
          <option value="">全部标的</option>
          <option v-for="a in assets" :key="a.id" :value="a.id">{{ a.name }}（{{ a.symbol }}）</option>
        </select>
        <span class="muted">共 {{ data.total }} 条</span>
      </div>
    </div>

    <div class="card section">
      <div class="table-wrap">
        <table>
          <thead><tr><th>日期</th><th>标的</th><th>分类</th><th>方向</th><th>数量</th><th>价格</th><th>手续费</th><th>金额</th><th>备注</th><th>操作</th></tr></thead>
          <tbody>
            <tr v-if="loading"><td colspan="10" class="muted" style="text-align: center">加载中…</td></tr>
            <tr v-for="t in data.items" v-else :key="t.id">
              <td>{{ date(t.tradeTime) }}</td>
              <td><router-link class="link" :to="`/assets/${t.assetId}`">{{ t.assetName }}</router-link><span class="tag">{{ t.assetSymbol }}</span></td>
              <td class="muted">{{ CATEGORY_LABEL[t.category] || t.category }}</td>
              <td><span class="pill" :class="t.direction === 'buy' ? 'red' : 'green'">{{ t.direction === 'buy' ? '买入' : '卖出' }}</span></td>
              <td class="num">{{ price(t.quantity) }}</td>
              <td class="num">{{ price(t.price) }}</td>
              <td class="num">{{ num(t.fee) }}</td>
              <td class="num">{{ money(t.quantity * t.price) }}</td>
              <td class="muted">{{ t.remark || '--' }}</td>
              <td>
                <div class="flex gap6" style="justify-content: flex-end">
                  <button class="btn sm ghost2" @click="open(t)">编辑</button>
                  <button class="btn sm danger" @click="remove(t)">删除</button>
                </div>
              </td>
            </tr>
            <tr v-if="!loading && !data.items.length"><td colspan="10"><div class="empty"><div class="big">⇄</div>暂无交易记录</div></td></tr>
          </tbody>
        </table>
      </div>
      <div v-if="pages > 1" class="card-pad flex between center">
        <span class="muted">第 {{ page }} / {{ pages }} 页</span>
        <div class="flex gap8">
          <button class="btn sm ghost2" :disabled="page <= 1" @click="page--">上一页</button>
          <button class="btn sm ghost2" :disabled="page >= pages" @click="page++">下一页</button>
        </div>
      </div>
    </div>

    <ModalDialog v-if="show" :title="form.id ? '编辑交易' : '记录交易'" :busy="busy" @close="show = false" @ok="save">
      <div class="field">
        <label>标的</label>
        <select v-model="form.assetId" :disabled="!!form.id">
          <option v-for="a in assets" :key="a.id" :value="a.id">{{ a.name }}（{{ a.symbol }}）</option>
        </select>
      </div>
      <div class="row2">
        <div class="field"><label>方向</label><select v-model="form.direction"><option value="buy">买入</option><option value="sell">卖出</option></select></div>
        <div class="field"><label>成交日期</label><input v-model="form.date" type="date" /></div>
      </div>
      <div class="row2">
        <div class="field"><label>数量</label><input v-model="form.quantity" type="number" step="any" /></div>
        <div class="field"><label>成交价</label><input v-model="form.price" type="number" step="any" /></div>
      </div>
      <div class="row2">
        <div class="field"><label>手续费</label><input v-model="form.fee" type="number" step="any" /></div>
        <div class="field"><label>备注</label><input v-model="form.remark" placeholder="可选" /></div>
      </div>
    </ModalDialog>

    <ModalDialog v-if="importOpen" title="导入交易 CSV" :busy="busy" ok-text="导入" @close="importOpen = false" @ok="doImport">
      <div class="field">
        <label>选择文件</label>
        <input type="file" accept=".csv,text/csv" @change="onFile" />
        <div class="hint">表头需包含：日期,标的,代码,方向,数量,单价,手续费（与导出格式一致）。仅会匹配已存在的标的。</div>
      </div>
      <div class="field"><label>或直接粘贴内容</label><textarea v-model="importText" rows="8" placeholder="日期,标的,代码,方向,数量,单价,手续费"></textarea></div>
    </ModalDialog>
  </div>
</template>
