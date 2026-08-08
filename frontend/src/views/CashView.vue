<script setup>
import { onMounted, reactive, ref } from 'vue';
import { Api } from '../api';
import { useApp } from '../store';
import ModalDialog from '../components/ModalDialog.vue';
import { money } from '../format';

const app = useApp();

const data = ref({ items: [], totalBalance: 0, currency: 'CNY' });
const show = ref(false);
const busy = ref(false);
const form = reactive({ id: '', name: '', currency: 'CNY', balance: '', remark: '' });

async function load() {
  try { data.value = await Api.cash(); } catch (e) { app.toast('加载失败', e.message, 'error'); }
}
onMounted(load);

function open(a) {
  Object.assign(form, a
    ? { id: a.id, name: a.name, currency: a.currency, balance: String(a.balance), remark: a.remark || '' }
    : { id: '', name: '', currency: 'CNY', balance: '', remark: '' });
  show.value = true;
}

async function save() {
  if (!form.name) return app.toast('请填写账户名称', '', 'error');
  const bal = parseFloat(form.balance);
  if (Number.isNaN(bal)) return app.toast('余额必须是数字', '', 'error');
  busy.value = true;
  try {
    const body = { name: form.name, currency: form.currency, balance: bal, remark: form.remark || undefined };
    if (form.id) await Api.updateCash(form.id, body);
    else await Api.createCash(body);
    show.value = false;
    app.toast('已保存', form.name, 'success');
    await load();
  } catch (e) {
    app.toast('保存失败', e.message, 'error');
  } finally {
    busy.value = false;
  }
}

async function remove(a) {
  if (!await app.confirm({ title: '删除账户', message: `删除账户「${a.name}」？`, danger: true })) return;
  try { await Api.deleteCash(a.id); await load(); } catch (e) { app.toast('删除失败', e.message, 'error'); }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="page-title">现金账户</div>
        <div class="page-sub">现金余额计入总资产，用于计算现金占比</div>
      </div>
      <button class="btn sm" @click="open(null)">新增账户</button>
    </div>

    <div class="card stat" style="max-width: 320px">
      <div class="label">现金合计（{{ data.currency }}）</div>
      <div class="value num">{{ money(data.totalBalance, data.currency) }}</div>
      <div class="delta muted">{{ data.items.length }} 个账户</div>
    </div>

    <div class="card section">
      <div class="table-wrap">
        <table>
          <thead><tr><th>账户</th><th>币种</th><th>余额</th><th>折算（{{ data.currency }}）</th><th>备注</th><th>操作</th></tr></thead>
          <tbody>
            <tr v-for="a in data.items" :key="a.id">
              <td>{{ a.name }}</td>
              <td class="muted">{{ a.currency }}</td>
              <td class="num">{{ money(a.balance, a.currency) }}</td>
              <td class="num">{{ money(a.balanceMain, data.currency) }}</td>
              <td class="muted">{{ a.remark || '--' }}</td>
              <td>
                <div class="flex gap6" style="justify-content: flex-end">
                  <button class="btn sm ghost2" @click="open(a)">编辑</button>
                  <button class="btn sm danger" @click="remove(a)">删除</button>
                </div>
              </td>
            </tr>
            <tr v-if="!data.items.length"><td colspan="6"><div class="empty"><div class="big">¤</div>还没有现金账户</div></td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <ModalDialog v-if="show" :title="form.id ? '编辑账户' : '新增账户'" :busy="busy" @close="show = false" @ok="save">
      <div class="field"><label>账户名称</label><input v-model="form.name" placeholder="如：招商银行卡" /></div>
      <div class="row2">
        <div class="field">
          <label>币种</label>
          <select v-model="form.currency"><option value="CNY">CNY</option><option value="USD">USD</option><option value="HKD">HKD</option></select>
        </div>
        <div class="field"><label>余额</label><input v-model="form.balance" type="number" step="any" /></div>
      </div>
      <div class="field"><label>备注</label><input v-model="form.remark" placeholder="可选" /></div>
    </ModalDialog>
  </div>
</template>
