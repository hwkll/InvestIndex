<script setup>
import { computed, onMounted, ref } from 'vue';
import { Api } from '../api';
import { useApp } from '../store';
import ModalDialog from '../components/ModalDialog.vue';
import { date, SIGNAL_LABEL, signalClass } from '../format';

const app = useApp();

const assets = ref([]);
const list = ref({ items: [], total: 0 });
const page = ref(1);
const scope = ref('');
const running = ref(false);
const detail = ref(null);
const detailLoading = ref(false);
const target = ref('');
const confirmDel = ref(null);

async function load() {
  try {
    list.value = await Api.analyses({ page: page.value, size: 20, scope: scope.value || undefined });
  } catch (e) {
    app.toast('加载失败', e.message, 'error');
  }
}

onMounted(async () => {
  try { assets.value = await Api.assets(); } catch { /* ignore */ }
  await load();
});

const pages = computed(() => Math.max(1, Math.ceil((list.value.total || 0) / 20)));

async function run(kind) {
  running.value = true;
  try {
    const body = kind === 'global' ? { scope: 'global' } : { scope: 'asset', assetId: target.value };
    if (kind === 'asset' && !target.value) { app.toast('请选择标的', '', 'error'); return; }
    const r = await Api.analyze(body);
    if (r.degraded) app.toast('已降级为本地启发式分析', r.notice || '', 'alert');
    await load();
    detail.value = { ...r, id: r.analysisId };
  } catch (e) {
    app.toast('分析失败', e.message, 'error');
  } finally {
    running.value = false;
  }
}

async function open(row) {
  detailLoading.value = true;
  detail.value = null;
  try {
    detail.value = await Api.analysis(row.id);
  } catch (e) {
    app.toast('读取失败', e.message, 'error');
  } finally {
    detailLoading.value = false;
  }
}

function askDelete(row) { confirmDel.value = row; }
async function doDelete() {
  if (!confirmDel.value) return;
  const row = confirmDel.value;
  confirmDel.value = null;
  try {
    await Api.deleteAnalysis(row.id);
    await load();
  } catch (e) {
    app.toast('删除失败', e.message, 'error');
  }
}

const conclusion = computed(() => detail.value?.conclusion || null);
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <div class="page-title">AI 分析</div>
        <div class="page-sub">基于持仓、行情与技术指标生成结构化结论；未配置 DeepSeek Key 时自动降级为本地启发式分析</div>
      </div>
    </div>

    <div class="card card-pad">
      <div class="inline-form">
        <select v-model="target" style="min-width: 200px">
          <option value="">选择要分析的标的…</option>
          <option v-for="a in assets" :key="a.id" :value="a.id">{{ a.name }}（{{ a.symbol }}）</option>
        </select>
        <button class="btn sm" :disabled="running" @click="run('asset')">
          <span v-if="running" class="spin"></span>分析该标的
        </button>
        <button class="btn sm sec" :disabled="running" @click="run('global')">分析整体组合</button>
        <span class="muted">结论仅供参考，不构成投资建议</span>
      </div>
    </div>

    <div class="card section">
      <div class="card-pad flex between center">
        <div class="section-title" style="margin: 0">分析历史</div>
        <select v-model="scope" style="width: auto" @change="page = 1; load()">
          <option value="">全部范围</option><option value="asset">单标的</option><option value="global">整体组合</option>
        </select>
      </div>
      <div class="table-wrap">
        <table>
          <thead><tr><th>时间</th><th>范围</th><th>标的</th><th>信号</th><th>置信度</th><th>模型</th><th>耗时</th><th>操作</th></tr></thead>
          <tbody>
            <tr v-for="r in list.items" :key="r.id">
              <td>{{ date(r.createdAt, true) }}</td>
              <td>{{ r.scope === 'global' ? '整体组合' : '单标的' }}</td>
              <td>{{ r.assetName || '--' }}</td>
              <td :class="signalClass(r.conclusion?.signal)" class="strong">{{ SIGNAL_LABEL[r.conclusion?.signal] || '--' }}</td>
              <td class="num">{{ r.conclusion ? ((r.conclusion.confidence || 0) * 100).toFixed(0) + '%' : '--' }}</td>
              <td class="muted">{{ r.model }}</td>
              <td class="num muted">{{ (r.durationMs / 1000).toFixed(1) }}s</td>
              <td>
                <div class="flex gap6" style="justify-content: flex-end">
                  <button class="btn sm ghost2" @click="open(r)">查看</button>
                  <button class="btn sm danger" @click="askDelete(r)">删除</button>
                </div>
              </td>
            </tr>
            <tr v-if="!list.items.length"><td colspan="8"><div class="empty"><div class="big">✦</div>暂无分析记录</div></td></tr>
          </tbody>
        </table>
      </div>
      <div v-if="pages > 1" class="card-pad flex between center">
        <span class="muted">第 {{ page }} / {{ pages }} 页</span>
        <div class="flex gap8">
          <button class="btn sm ghost2" :disabled="page <= 1" @click="page--; load()">上一页</button>
          <button class="btn sm ghost2" :disabled="page >= pages" @click="page++; load()">下一页</button>
        </div>
      </div>
    </div>

    <ModalDialog v-if="detail" title="分析详情" hide-foot @close="detail = null">
      <div v-if="detailLoading" class="empty" style="padding: 30px"><span class="spin" style="width:20px;height:20px;border-width:3px"></span></div>
      <template v-else-if="conclusion">
        <div class="flex between center">
          <div class="strong" :class="signalClass(conclusion.signal)" style="font-size: 20px">{{ SIGNAL_LABEL[conclusion.signal] || conclusion.signal }}</div>
          <div class="muted">置信度 {{ ((conclusion.confidence || 0) * 100).toFixed(0) }}% · {{ detail.model }}</div>
        </div>
        <div class="bar mt8"><i :style="{ width: ((conclusion.confidence || 0) * 100) + '%' }"></i></div>
        <p class="mt12" style="line-height: 1.7">{{ conclusion.summary }}</p>
        <template v-if="conclusion.reasons?.length">
          <div class="muted mt8">主要理由</div>
          <ul class="reasons"><li v-for="(r, i) in conclusion.reasons" :key="i">{{ r }}</li></ul>
        </template>
        <template v-if="conclusion.risks?.length">
          <div class="muted mt8">风险提示</div>
          <ul class="reasons"><li v-for="(r, i) in conclusion.risks" :key="i">{{ r }}</li></ul>
        </template>
        <template v-if="conclusion.actions?.length">
          <div class="muted mt8">建议动作</div>
          <ul class="reasons"><li v-for="(a, i) in conclusion.actions" :key="i"><b>{{ a.action }}</b> — {{ a.suggestion }}</li></ul>
        </template>
        <div class="muted mt12" style="font-size: 12px">{{ date(detail.createdAt, true) }} · 结论仅供参考，不构成投资建议</div>
      </template>
      <div v-else class="muted">该记录没有结构化结论</div>
      <div class="modal-foot"><button class="btn ghost2" @click="detail = null">关闭</button></div>
    </ModalDialog>

    <ModalDialog v-if="confirmDel" title="删除确认" ok-text="确认删除" @close="confirmDel = null" @ok="doDelete">
      <p>确定要删除这条分析记录吗？此操作不可恢复。</p>
      <p class="muted" style="font-size:13px">{{ date(confirmDel.createdAt, true) }} · {{ confirmDel.model }}</p>
    </ModalDialog>
  </div>
</template>
