<script setup>
import { computed, onMounted, reactive, ref } from 'vue';
import { Api } from '../api';
import { useApp } from '../store';
import ModalDialog from '../components/ModalDialog.vue';
import { date, num } from '../format';

const app = useApp();

const rules = ref([]);
const events = ref([]);
const assets = ref([]);
const tab = ref('rules');
const show = ref(false);
const busy = ref(false);

const TYPES = [
  { v: 'price', t: '价格触达' },
  { v: 'percent', t: '涨跌幅' },
  { v: 'range_break', t: '区间突破' },
  { v: 'ai_signal', t: 'AI 信号' },
  { v: 'schedule', t: '定时提醒' },
];
const TYPE_LABEL = Object.fromEntries(TYPES.map((t) => [t.v, t.t]));

const form = reactive({
  id: '', assetId: '', name: '', type: 'price', direction: 'up',
  threshold: '', windowDays: '90', scheduleCron: '09:00', channel: 'web', remark: '',
});

async function load() {
  try {
    const [r, e] = await Promise.all([Api.alertRules(), Api.alertEvents()]);
    rules.value = r || [];
    events.value = e.items || [];
    app.unread = e.unread || 0;
  } catch (err) {
    app.toast('加载失败', err.message, 'error');
  }
}

onMounted(async () => {
  try { assets.value = await Api.assets(); } catch { /* ignore */ }
  await load();
});

function open(r) {
  Object.assign(form, r
    ? {
        id: r.id, assetId: r.assetId || '', name: r.name, type: r.type, direction: r.direction || 'up',
        threshold: r.threshold != null ? String(r.threshold) : '', windowDays: r.windowDays != null ? String(r.windowDays) : '90',
        scheduleCron: r.scheduleCron || '09:00', channel: r.channel || 'web', remark: r.remark || '',
      }
    : { id: '', assetId: assets.value[0]?.id || '', name: '', type: 'price', direction: 'up', threshold: '', windowDays: '90', scheduleCron: '09:00', channel: 'web', remark: '' });
  show.value = true;
}

const needsAsset = computed(() => form.type !== 'schedule');
const needsThreshold = computed(() => form.type === 'price' || form.type === 'percent');

async function save() {
  if (!form.name) return app.toast('请填写提醒名称', '', 'error');
  if (needsAsset.value && !form.assetId) return app.toast('请选择标的', '', 'error');
  if (needsThreshold.value) {
    const th = parseFloat(form.threshold);
    if (isNaN(th) || (form.type === 'price' && th <= 0)) {
      return app.toast('请输入有效的阈值', '', 'error');
    }
  }
  busy.value = true;
  try {
    const body = {
      assetId: needsAsset.value ? form.assetId : undefined,
      name: form.name, type: form.type,
      direction: form.type === 'ai_signal' || form.type === 'schedule' ? undefined : form.direction,
      threshold: needsThreshold.value ? parseFloat(form.threshold) : undefined,
      windowDays: form.type === 'range_break' ? parseInt(form.windowDays, 10) : undefined,
      scheduleCron: form.type === 'schedule' ? form.scheduleCron : undefined,
      channel: form.channel, remark: form.remark || undefined,
    };
    if (form.id) await Api.updateAlert(form.id, body);
    else await Api.createAlert(body);
    show.value = false;
    app.toast('已保存', form.name, 'success');
    await load();
  } catch (e) {
    app.toast('保存失败', e.message, 'error');
  } finally {
    busy.value = false;
  }
}

async function toggle(r) {
  try {
    await Api.updateAlert(r.id, { enabled: !r.enabled });
    await load();
  } catch (e) {
    app.toast('操作失败', e.message, 'error');
  }
}

async function remove(r) {
  if (!await app.confirm({ title: '删除提醒', message: `删除提醒「${r.name}」？`, danger: true })) return;
  try { await Api.deleteAlert(r.id); await load(); } catch (e) { app.toast('删除失败', e.message, 'error'); }
}

async function markRead(e) {
  try { await Api.markAlertRead(e.eventId); await load(); } catch (err) { app.toast('操作失败', err.message, 'error'); }
}

function describe(r) {
  const th = r.threshold;
  switch (r.type) {
    case 'price': return `价格${r.direction === 'up' ? '≥' : '≤'} ${num(th, 6)}`;
    case 'percent': return `当日${r.direction === 'up' ? '涨幅' : '跌幅'} ≥ ${num(Math.abs(th || 0))}%`;
    case 'range_break': return `突破近 ${r.windowDays || 90} 日${r.direction === 'up' ? '新高' : r.direction === 'down' ? '新低' : '高/低点'}`;
    case 'ai_signal': return 'AI 给出买入/卖出信号时提醒';
    case 'schedule': return `每日 ${r.scheduleCron || '09:00'} 提醒`;
    default: return '--';
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="page-title">提醒</div>
        <div class="page-sub">价格、涨跌幅、区间突破与 AI 信号提醒，触发后实时推送到页面</div>
      </div>
      <button class="btn sm" @click="open(null)">新建提醒</button>
    </div>

    <div class="tabs">
      <div class="tab" :class="{ active: tab === 'rules' }" @click="tab = 'rules'">规则（{{ rules.length }}）</div>
      <div class="tab" :class="{ active: tab === 'events' }" @click="tab = 'events'">
        触发记录<span v-if="app.unread" class="pill red" style="margin-left: 6px">{{ app.unread }}</span>
      </div>
    </div>

    <div v-if="tab === 'rules'" class="card">
      <div class="table-wrap">
        <table>
          <thead><tr><th>名称</th><th>标的</th><th>类型</th><th>条件</th><th>通道</th><th>状态</th><th>创建时间</th><th>操作</th></tr></thead>
          <tbody>
            <tr v-for="r in rules" :key="r.id">
              <td>{{ r.name }}</td>
              <td>{{ r.assetName || '--' }}<span v-if="r.assetSymbol" class="tag">{{ r.assetSymbol }}</span></td>
              <td class="muted">{{ TYPE_LABEL[r.type] || r.type }}</td>
              <td>{{ describe(r) }}</td>
              <td class="muted">{{ r.channel }}</td>
              <td><span class="pill" :class="r.enabled ? 'green' : 'gray'">{{ r.enabled ? '启用' : '停用' }}</span></td>
              <td class="muted">{{ date(r.createdAt) }}</td>
              <td>
                <div class="flex gap6" style="justify-content: flex-end">
                  <button class="btn sm ghost2" @click="toggle(r)">{{ r.enabled ? '停用' : '启用' }}</button>
                  <button class="btn sm ghost2" @click="open(r)">编辑</button>
                  <button class="btn sm danger" @click="remove(r)">删除</button>
                </div>
              </td>
            </tr>
            <tr v-if="!rules.length"><td colspan="8"><div class="empty"><div class="big">◑</div>还没有提醒规则</div></td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <div v-else class="card">
      <div class="table-wrap">
        <table>
          <thead><tr><th>时间</th><th>内容</th><th>触发值</th><th>状态</th><th>操作</th></tr></thead>
          <tbody>
            <tr v-for="e in events" :key="e.eventId">
              <td>{{ date(e.createdAt, true) }}</td>
              <td>{{ e.message }}</td>
              <td class="num">{{ e.triggerValue != null ? num(e.triggerValue, 4) : '--' }}</td>
              <td><span class="pill" :class="e.read ? 'gray' : 'warn'">{{ e.read ? '已读' : '未读' }}</span></td>
              <td><button v-if="!e.read" class="btn sm ghost2" @click="markRead(e)">标记已读</button></td>
            </tr>
            <tr v-if="!events.length"><td colspan="5"><div class="empty"><div class="big">◔</div>暂无触发记录</div></td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <ModalDialog v-if="show" :title="form.id ? '编辑提醒' : '新建提醒'" :busy="busy" @close="show = false" @ok="save">
      <div class="field"><label>提醒名称</label><input v-model="form.name" placeholder="如：BTC 跌破 5 万" /></div>
      <div class="row2">
        <div class="field">
          <label>类型</label>
          <select v-model="form.type" :disabled="!!form.id">
            <option v-for="t in TYPES" :key="t.v" :value="t.v">{{ t.t }}</option>
          </select>
        </div>
        <div class="field" v-if="needsAsset">
          <label>标的</label>
          <select v-model="form.assetId" :disabled="!!form.id">
            <option v-for="a in assets" :key="a.id" :value="a.id">{{ a.name }}（{{ a.symbol }}）</option>
          </select>
        </div>
      </div>
      <div class="row2">
        <div class="field" v-if="form.type === 'price' || form.type === 'percent' || form.type === 'range_break'">
          <label>方向</label>
          <select v-model="form.direction">
            <option value="up">向上 / 涨</option><option value="down">向下 / 跌</option>
            <option v-if="form.type === 'range_break'" value="both">双向</option>
          </select>
        </div>
        <div class="field" v-if="needsThreshold">
          <label>{{ form.type === 'price' ? '目标价' : '阈值（%）' }}</label>
          <input v-model="form.threshold" type="number" step="any" />
        </div>
        <div class="field" v-if="form.type === 'range_break'"><label>回看天数</label><input v-model="form.windowDays" type="number" /></div>
        <div class="field" v-if="form.type === 'schedule'"><label>每日时间</label><input v-model="form.scheduleCron" placeholder="09:00" /></div>
      </div>
      <div class="row2">
        <div class="field">
          <label>通知通道</label>
          <select v-model="form.channel">
            <option value="web">站内</option>
            <option value="web,mail">站内 + 邮件</option>
            <option value="web,webhook">站内 + Webhook</option>
            <option value="web,mail,webhook">站内 + 邮件 + Webhook</option>
          </select>
          <div class="hint">Webhook 通道需在设置中填写 Webhook 地址</div>
        </div>
        <div class="field"><label>备注</label><input v-model="form.remark" placeholder="可选" /></div>
      </div>
    </ModalDialog>
  </div>
</template>
