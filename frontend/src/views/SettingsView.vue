<script setup>
import { computed, onMounted, reactive, ref } from 'vue';
import { Api } from '../api';
import { useApp } from '../store';
import ModalDialog from '../components/ModalDialog.vue';

const app = useApp();

/** Keys that participate in the "unsaved changes" diff. */
const DEFAULTS = {
  currency: 'CNY',
  data_source_mode: 'auto',
  deepseek_model: 'deepseek-v4-flash',
  smtp_host: '',
  smtp_port: '587',
  smtp_from: '',
  smtp_to: '',
  smtp_tls: '0',
  webhook_url: '',
  ai_market_context: 'true',
  benchmark: '',
  poll_interval: '30',
  fx_refresh_interval: '1800',
};

const form = reactive({ ...DEFAULTS, deepseek_api_key: '', smtp_user: '', smtp_pass: '' });
const saved = reactive({ ...DEFAULTS }); // server state, for diffing
const keyHasValue = ref(false);
const smtpUserHasValue = ref(false);
const smtpPassHasValue = ref(false);
const whHasValue = ref(false);
const loading = ref(true);
const busy = ref(false);
const showKey = ref(false);
const showSmtpPass = ref(false);

// Diff-based dirty tracking: editing a value back to its original clears it.
const changed = computed(() => {
  const list = Object.keys(DEFAULTS).filter((k) => String(form[k]) !== String(saved[k]));
  if (form.deepseek_api_key) list.push('deepseek_api_key');
  if (form.smtp_user) list.push('smtp_user');
  if (form.smtp_pass) list.push('smtp_pass');
  return list;
});
const dirty = computed(() => changed.value.length > 0);

const SOURCES = [
  { v: 'auto', ico: '◈', name: '自动', desc: '联网取真实行情，断网/无源时显示「暂无行情源」（绝不回退模拟）' },
  { v: 'real', ico: '◉', name: '真实数据', desc: 'CoinGecko / 新浪财经' },
];

async function load() {
  loading.value = true;
  try {
    const s = await Api.settings();
    for (const k of Object.keys(DEFAULTS)) {
      const v = s[k] && s[k].value;
      form[k] = v || DEFAULTS[k];
      saved[k] = form[k];
    }
    keyHasValue.value = !!(s.deepseek_api_key && s.deepseek_api_key.has_value);
    smtpUserHasValue.value = !!(s.smtp_user && s.smtp_user.has_value);
    smtpPassHasValue.value = !!(s.smtp_pass && s.smtp_pass.has_value);
    whHasValue.value = !!(s.webhook_url && s.webhook_url.has_value);
    await loadFx();
    await loadAssets();
  } catch (e) {
    app.toast('加载失败', e.message, 'error');
  } finally {
    loading.value = false;
  }
}
onMounted(load);

async function save() {
  busy.value = true;
  try {
    // Clamp refresh intervals so the persisted value matches the effective one.
    form.poll_interval = clampNum(form.poll_interval, 5, 600, 30);
    form.fx_refresh_interval = clampNum(form.fx_refresh_interval, 60, 86400, 1800);
    const body = {};
    for (const k of Object.keys(DEFAULTS)) {
      if (k === 'webhook_url') continue; // 单独处理，避免清空已加密原值
      body[k] = String(form[k]);
    }
    // 仅在用户实际输入时才提交敏感字段，空值保留已存储（加密）原值。
    if (form.webhook_url) body.webhook_url = form.webhook_url;
    if (form.deepseek_api_key) body.deepseek_api_key = form.deepseek_api_key;
    if (form.smtp_user) body.smtp_user = form.smtp_user;
    if (form.smtp_pass) body.smtp_pass = form.smtp_pass;
    await Api.saveSettings(body);
    for (const k of Object.keys(DEFAULTS)) saved[k] = form[k];
    if (form.webhook_url) {
      whHasValue.value = true;
      form.webhook_url = '';
    }
    if (form.deepseek_api_key) {
      keyHasValue.value = true;
      form.deepseek_api_key = '';
      showKey.value = false;
    }
    if (form.smtp_user) {
      smtpUserHasValue.value = true;
      form.smtp_user = '';
    }
    if (form.smtp_pass) {
      smtpPassHasValue.value = true;
      form.smtp_pass = '';
      showSmtpPass.value = false;
    }
    app.toast('已保存', '设置已生效', 'success');
  } catch (e) {
    app.toast('保存失败', e.message, 'error');
  } finally {
    busy.value = false;
  }
}

function reset() {
  for (const k of Object.keys(DEFAULTS)) form[k] = saved[k];
  form.deepseek_api_key = '';
  form.smtp_user = '';
  form.smtp_pass = '';
}

/** Coerce a user-entered refresh interval to a finite number within [min,max]. */
function clampNum(v, min, max, def) {
  let n = Number(v);
  if (!Number.isFinite(n)) n = def;
  if (n < min) n = min;
  if (n > max) n = max;
  return n;
}

/* ---------- AI connectivity test ---------- */
const testBusy = ref(false);
const testResult = reactive({ ok: null, msg: '' });

async function testAI() {
  testBusy.value = true;
  testResult.ok = null;
  testResult.msg = '';
  try {
    const r = await Api.testAI(form.deepseek_api_key, form.deepseek_model);
    testResult.ok = !!r.ok;
    testResult.msg = r.ok ? r.model || form.deepseek_model : r.error || '未知错误';
  } catch (e) {
    testResult.ok = false;
    testResult.msg = e.message;
  } finally {
    testBusy.value = false;
  }
}

/* ---------- Mail (SMTP) connectivity test ---------- */
const mailBusy = ref(false);
const mailResult = reactive({ ok: null, msg: '' });

async function testMail() {
  mailBusy.value = true;
  mailResult.ok = null;
  mailResult.msg = '';
  try {
    const r = await Api.testMail();
    mailResult.ok = !!r.ok;
    mailResult.msg = r.ok ? '测试邮件已发送，请查收' : (r.error || '未知错误');
  } catch (e) {
    mailResult.ok = false;
    mailResult.msg = e.message;
  } finally {
    mailBusy.value = false;
  }
}

/* ---------- FX 多币种汇率表 (Next-②) ---------- */
const fxList = ref([]);
const fxBusy = ref(false);
const fxLoaded = ref(false);
const fxResult = reactive({ ok: null, msg: '' });

async function loadFx() {
  try {
    const rows = await Api.fxRates();
    fxList.value = (rows || []).map((r) => ({ currency: r.currency, rate: r.rate, auto: !!r.auto, updatedAt: r.updatedAt || 0 }));
  } catch (e) {
    app.toast('汇率加载失败', e.message, 'error');
  } finally {
    fxLoaded.value = true;
  }
}

function fmtFxTime(ts) {
  if (!ts) return '';
  const d = new Date(ts);
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getMonth() + 1}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

async function saveFx() {
  fxBusy.value = true;
  fxResult.ok = null;
  fxResult.msg = '';
  try {
    const body = {};
    for (const r of fxList.value) {
      const v = Number(r.rate);
      if (r.currency && v > 0) body[r.currency] = v;
    }
    await Api.saveFx(body);
    fxResult.ok = true;
    fxResult.msg = '汇率已保存';
    app.toast('已保存', '多币种汇率已更新', 'success');
    await loadFx(); // 刷新 auto/updatedAt 状态（保存即锁定为手动）
  } catch (e) {
    fxResult.ok = false;
    fxResult.msg = e.message;
  } finally {
    fxBusy.value = false;
  }
}

const newCur = ref('');
const newRate = ref('');
function addCur() {
  const c = newCur.value.trim().toUpperCase();
  const v = Number(newRate.value);
  if (!c) return app.toast('请填写币种代码', '', 'error');
  if (!c.match(/^[A-Z]{3}$/)) return app.toast('币种代码应为 3 位大写字母', '', 'error');
  if (!(v > 0)) return app.toast('汇率必须大于 0', '', 'error');
  if (fxList.value.some((r) => r.currency === c)) return app.toast('该币种已存在', '', 'error');
  fxList.value.push({ currency: c, rate: v });
  newCur.value = '';
  newRate.value = '';
}

/* ---------- 基准对比可选标的 (Later-②) ---------- */
const assetOptions = ref([]);
async function loadAssets() {
  try {
    const list = await Api.assets();
    assetOptions.value = (list || []).filter((a) => a.status === 'active');
  } catch (e) { /* non-fatal: benchmark dropdown simply stays empty */ }
}

/* ---------- Webhook 连接测试 (Next-⑤) ---------- */
const whBusy = ref(false);
const whResult = reactive({ ok: null, msg: '' });
async function testWebhook() {
  whBusy.value = true;
  whResult.ok = null;
  whResult.msg = '';
  try {
    const r = await Api.testWebhook();
    whResult.ok = !!r.ok;
    whResult.msg = r.ok ? '测试消息已发送，请检查你的接收端' : (r.error || '未知错误');
  } catch (e) {
    whResult.ok = false;
    whResult.msg = e.message;
  } finally {
    whBusy.value = false;
  }
}

/* ---------- Access PIN ---------- */
const pinModal = ref(false);
const pinMode = ref('set'); // set | change | clear
const pinBusy = ref(false);
const pinForm = reactive({ oldPin: '', pin: '', confirm: '' });
const pinError = ref('');

function openPin(mode) {
  pinMode.value = mode;
  pinForm.oldPin = '';
  pinForm.pin = '';
  pinForm.confirm = '';
  pinError.value = '';
  pinModal.value = true;
}

const pinTitle = computed(
  () => ({ set: '设置访问口令', change: '修改访问口令', clear: '关闭访问保护' })[pinMode.value],
);

async function submitPin() {
  pinError.value = '';
  if (pinMode.value !== 'set' && !pinForm.oldPin) {
    pinError.value = '请输入当前口令';
    return;
  }
  if (pinMode.value !== 'clear') {
    if (pinForm.pin.length < 6) {
      pinError.value = '口令长度至少 6 位';
      return;
    }
    if (pinForm.pin !== pinForm.confirm) {
      pinError.value = '两次输入的口令不一致';
      return;
    }
  }
  pinBusy.value = true;
  try {
    if (pinMode.value === 'clear') {
      await Api.setPin('', pinForm.oldPin);
      app.authRequired = false;
      app.toast('已关闭', '访问保护已解除，所有会话已失效', 'success');
    } else {
      await Api.setPin(pinForm.pin, pinForm.oldPin);
      app.authRequired = true;
      app.toast('已生效', pinMode.value === 'set' ? '访问口令已设置' : '访问口令已更新', 'success');
    }
    pinModal.value = false;
  } catch (e) {
    pinError.value = e.message;
  } finally {
    pinBusy.value = false;
  }
}

/* ---------- Backup / restore ---------- */
const fileInput = ref(null);
const importModal = ref(false);
const importBusy = ref(false);
const importInfo = reactive({ name: '', tables: 0, rows: 0 });
let importPayload = null;

function pickFile() {
  if (fileInput.value) fileInput.value.value = '';
  fileInput.value?.click();
}

async function onFile(ev) {
  const f = ev.target.files && ev.target.files[0];
  if (!f) return;
  try {
    const text = await f.text();
    const json = JSON.parse(text);
    if (!json || typeof json !== 'object' || !json.tables) {
      throw new Error('不是有效的 InvestHub 备份文件');
    }
    const names = Object.keys(json.tables);
    importInfo.name = f.name;
    importInfo.tables = names.length;
    importInfo.rows = names.reduce((n, t) => n + (json.tables[t]?.length || 0), 0);
    importPayload = json;
    importModal.value = true;
  } catch (e) {
    app.toast('读取失败', e.message, 'error');
  }
}

async function doImport() {
  if (!importPayload) return;
  importBusy.value = true;
  try {
    const r = await Api.importJSON(importPayload);
    importModal.value = false;
    importPayload = null;
    // backend returns {imported: true, rows: N} — `imported` is a bool, not a count
    app.toast('导入完成', `已恢复 ${r?.rows ?? importInfo.rows} 条记录`, 'success');
    await load();
  } catch (e) {
    app.toast('导入失败', e.message, 'error');
  } finally {
    importBusy.value = false;
  }
}
</script>

<template>
  <div class="set-wrap">
    <div class="page-head">
      <div>
        <div class="page-title">设置</div>
        <div class="page-sub">本地自托管，所有数据仅保存在你自己的设备上</div>
      </div>
    </div>

    <!-- sticky save bar, only when there are unsaved edits -->
    <Transition name="savebar">
      <div v-if="dirty" class="save-bar">
        <span class="dot"></span>
        <span class="sb-text"><b>{{ changed.length }}</b> 项修改未保存</span>
        <button class="btn ghost2 sm" :disabled="busy" @click="reset">放弃</button>
        <button class="btn sm" :disabled="busy" @click="save">
          <span v-if="busy" class="spin"></span>{{ busy ? ' 保存中' : '保存更改' }}
        </button>
      </div>
    </Transition>

    <!-- ============ 通用 ============ -->
    <section class="set-card">
      <header class="set-head">
        <div class="set-ico blue">¥</div>
        <div>
          <h3>通用</h3>
          <p>金额展示与货币换算</p>
        </div>
      </header>

      <div class="set-row">
        <div class="rl">
          <label>主显示币种</label>
          <p>仪表盘、持仓市值等金额的默认计价单位</p>
        </div>
        <div class="rr">
          <div class="seg">
            <button :class="{ on: form.currency === 'CNY' }" @click="form.currency = 'CNY'">¥ CNY</button>
            <button :class="{ on: form.currency === 'USD' }" @click="form.currency = 'USD'">$ USD</button>
          </div>
        </div>
      </div>
    </section>

    <!-- ============ 行情数据源 ============ -->
    <section class="set-card">
      <header class="set-head">
        <div class="set-ico green">◉</div>
        <div>
          <h3>行情数据源</h3>
          <p>决定持仓价格从哪里获取，每 {{ Number(form.poll_interval) || 30 }} 秒刷新一次</p>
        </div>
      </header>

      <div class="src-grid">
        <button
          v-for="s in SOURCES"
          :key="s.v"
          class="src"
          :class="{ on: form.data_source_mode === s.v }"
          @click="form.data_source_mode = s.v"
        >
          <span class="src-ico">{{ s.ico }}</span>
          <span class="src-name">{{ s.name }}</span>
          <span class="src-desc">{{ s.desc }}</span>
          <span class="src-check">✓</span>
        </button>
      </div>
    </section>

    <!-- ============ 刷新频率 ============ -->
    <section class="set-card">
      <header class="set-head">
        <div class="set-ico green">⟳</div>
        <div>
          <h3>刷新频率</h3>
          <p>行情轮询与多币种汇率的刷新间隔；保存后即时生效，无需重启</p>
        </div>
      </header>

      <div class="set-row">
        <div class="rl">
          <label>行情刷新间隔（秒）</label>
          <p>多久向数据源轮询一次实时价格并推送行情更新；建议 5–600 秒</p>
        </div>
        <div class="rr">
          <input type="number" min="5" max="600" step="5" class="rate-in" style="width:140px"
                 v-model.number="form.poll_interval" />
        </div>
      </div>

      <div class="set-row">
        <div class="rl">
          <label>汇率刷新间隔（秒）</label>
          <p>多久抓取一次实时汇率（USD/HKD→CNY）；建议 60–86400 秒</p>
        </div>
        <div class="rr">
          <input type="number" min="60" max="86400" step="60" class="rate-in" style="width:140px"
                 v-model.number="form.fx_refresh_interval" />
        </div>
      </div>

      <div class="banner warn" style="margin-top:14px">
        <span class="bi">!</span>
        <div>修改后点击右上角「保存更改」即可即时生效：行情轮询与汇率抓取会按新间隔重新计时，当前进程不会重启。</div>
      </div>
    </section>

    <!-- ============ 多币种汇率表 (Next-②) ============ -->
    <section class="set-card">
      <header class="set-head">
        <div class="set-ico amber">⇄</div>
        <div>
          <h3>多币种汇率表</h3>
          <p>各币种相对人民币的汇率；外币资产市值按此折算展示</p>
        </div>
      </header>

      <div v-if="!fxLoaded" class="loading-line">加载汇率中…</div>
      <div v-else-if="!fxList.length" class="muted" style="text-align:center;padding:20px">暂无汇率数据，系统默认含 CNY、USD、HKD</div>
      <table v-else class="fx-table">
        <thead>
          <tr><th>币种</th><th>1 单位 = ? CNY</th><th></th></tr>
        </thead>
        <tbody>
          <tr v-for="(r, i) in fxList" :key="r.currency">
            <td>
              <span class="cur-badge" :class="{ base: r.currency === 'CNY' }">{{ r.currency }}</span>
              <div v-if="r.currency !== 'CNY'" style="margin-top:4px;font-size:11px">
                <span :style="r.auto ? 'color:#1a9e5f' : 'color:#999'">{{ r.auto ? '实时' : '已锁定' }}</span>
                <span class="muted mini" v-if="r.updatedAt"> · {{ fmtFxTime(r.updatedAt) }}</span>
              </div>
              <div v-else class="muted mini" style="margin-top:4px">换算枢纽</div>
            </td>
            <td class="num">
              <input
                v-if="r.currency === 'CNY'"
                class="rate-in"
                :value="'1.0000'"
                disabled
                title="人民币为换算枢纽，固定为 1"
              />
              <input v-else type="number" step="any" min="0" class="rate-in" v-model="r.rate" />
            </td>
            <td class="act">
              <button
                v-if="r.currency !== 'CNY'"
                class="btn ghost2 sm danger-txt"
                type="button"
                @click="fxList.splice(i, 1)"
              >移除</button>
              <span v-else class="muted mini">基准</span>
            </td>
          </tr>
        </tbody>
      </table>

      <div class="fx-add">
        <input v-model="newCur" maxlength="3" placeholder="如 EUR" class="cur-in" style="width: 90px" />
        <div class="input-affix">
          <span class="pre">1 =</span>
          <input type="number" step="any" min="0" v-model="newRate" placeholder="汇率" />
          <span class="suf">CNY</span>
        </div>
        <button class="btn ghost2 sm" type="button" @click="addCur">添加币种</button>
      </div>

      <div class="set-row col" style="border-top: 1px solid var(--line); margin-top: 14px;">
        <div class="rr auto" style="width: auto;">
          <button class="btn sm" :disabled="fxBusy" @click="saveFx">
            <span v-if="fxBusy" class="spin"></span>{{ fxBusy ? ' 保存中' : '保存汇率表' }}
          </button>
        </div>
        <div v-if="fxResult.ok === true" class="note ok">✓ {{ fxResult.msg }}</div>
        <div v-else-if="fxResult.ok === false" class="note bad">✗ {{ fxResult.msg }}</div>
      </div>
    </section>

    <!-- ============ AI 分析 ============ -->
    <section class="set-card">
      <header class="set-head">
        <div class="set-ico violet">✦</div>
        <div>
          <h3>AI 分析</h3>
          <p>需配置 DeepSeek API Key 后方可使用 AI 分析（请选用 V4 系列模型）</p>
          <div class="note warn" style="margin-top:8px;">
            ⚠ 数据出境提示：使用 AI 分析时，相关持仓与行情数据将发送至 DeepSeek 服务器（第三方）处理。请确认你已了解并同意该数据传输，仅在合规前提下使用。
          </div>
        </div>
      </header>

      <div class="set-row">
        <div class="rl">
          <label>市场上下文增强</label>
          <p>AI 分析时自动附带真实大盘指数 / 行业板块 / 宏观经济作为背景参考；数据缺失时自动降级，不影响分析结论</p>
        </div>
        <div class="rr">
          <label class="switch">
            <input type="checkbox" :checked="form.ai_market_context === 'true'" @change="form.ai_market_context = $event.target.checked ? 'true' : 'false'" />
            <span class="slider"></span>
          </label>
        </div>
      </div>

      <div class="set-row">
        <div class="rl">
          <label>模型名称</label>
          <p>DeepSeek V4 模型标识（官方已于 2026-07-24 弃用 deepseek-chat）</p>
        </div>
        <div class="rr">
          <input v-model="form.deepseek_model" list="ds-models" placeholder="deepseek-v4-flash" />
          <datalist id="ds-models">
            <option value="deepseek-v4-flash">deepseek-v4-flash（高速推理，推荐）</option>
            <option value="deepseek-v4-pro">deepseek-v4-pro（深度推理）</option>
          </datalist>
        </div>
      </div>

      <div class="set-row col">
        <div class="rl">
          <label>
            API Key
            <span v-if="keyHasValue" class="pill green mini">已配置</span>
            <span v-else class="pill gray mini">未配置</span>
          </label>
          <p>加密后存储在本地数据库，不会上传到任何第三方</p>
        </div>
        <div class="key-row">
          <div class="input-affix grow">
            <input
              :type="showKey ? 'text' : 'password'"
              v-model="form.deepseek_api_key"
              :placeholder="keyHasValue ? '已保存，留空则不修改' : 'sk-...'"
              autocomplete="new-password"
            />
            <button class="eye" type="button" :title="showKey ? '隐藏' : '显示'" @click="showKey = !showKey">
              {{ showKey ? '◎' : '◉' }}
            </button>
          </div>
          <button class="btn ghost2" :disabled="testBusy" @click="testAI">
            <span v-if="testBusy" class="spin"></span>{{ testBusy ? ' 测试中' : '测试连接' }}
          </button>
        </div>
        <div v-if="testResult.ok === true" class="note ok">✓ 连接成功 · {{ testResult.msg }}</div>
        <div v-else-if="testResult.ok === false" class="note bad">✗ {{ testResult.msg }}</div>
      </div>
    </section>

    <!-- ============ 邮件提醒 (SMTP) ============ -->
    <section class="set-card">
      <header class="set-head">
        <div class="set-ico teal">✉</div>
        <div>
          <h3>邮件提醒 (SMTP)</h3>
          <p>提醒触发时把通知发到你的邮箱；不填则仅在应用内提示</p>
        </div>
      </header>

      <div class="set-row">
        <div class="rl">
          <label>SMTP 服务器</label>
          <p>例如 smtp.qq.com / smtp.gmail.com</p>
        </div>
        <div class="rr"><input v-model="form.smtp_host" placeholder="smtp.example.com" /></div>
      </div>

      <div class="set-row">
        <div class="rl">
          <label>端口</label>
          <p>587(STARTTLS) / 465(隐式 TLS) / 25(明文)</p>
        </div>
        <div class="rr"><input v-model="form.smtp_port" placeholder="587" /></div>
      </div>

      <div class="set-row">
        <div class="rl"><label>发件人</label><p>显示用的发件地址</p></div>
        <div class="rr"><input v-model="form.smtp_from" placeholder="investhub@example.com" /></div>
      </div>

      <div class="set-row">
        <div class="rl"><label>收件人</label><p>提醒发送到的邮箱；留空则用登录账号</p></div>
        <div class="rr"><input v-model="form.smtp_to" placeholder="you@example.com" /></div>
      </div>

      <div class="set-row">
        <div class="rl"><label>隐式 TLS</label><p>端口 465 时勾选；587 保持不勾</p></div>
        <div class="rr auto">
          <label class="switch">
            <input type="checkbox" :checked="form.smtp_tls === '1'" @change="form.smtp_tls = $event.target.checked ? '1' : '0'" />
            <span class="track"></span>
          </label>
        </div>
      </div>

      <div class="set-row col">
        <div class="rl">
          <label>
            登录账号
            <span v-if="smtpUserHasValue" class="pill green mini">已配置</span>
            <span v-else class="pill gray mini">未配置</span>
          </label>
          <p>SMTP 用户名（通常与邮箱相同）</p>
        </div>
        <input v-model="form.smtp_user" placeholder="未配置则留空" />
      </div>

      <div class="set-row col">
        <div class="rl">
          <label>
            登录密码
            <span v-if="smtpPassHasValue" class="pill green mini">已配置</span>
            <span v-else class="pill gray mini">未配置</span>
          </label>
          <p>加密后存储在本地数据库，授权码/密码不会明文保存</p>
        </div>
        <div class="key-row">
          <div class="input-affix grow">
            <input
              :type="showSmtpPass ? 'text' : 'password'"
              v-model="form.smtp_pass"
              :placeholder="smtpPassHasValue ? '已保存，留空则不修改' : '授权码 / 密码'"
              autocomplete="new-password"
            />
            <button class="eye" type="button" :title="showSmtpPass ? '隐藏' : '显示'" @click="showSmtpPass = !showSmtpPass">
              {{ showSmtpPass ? '◎' : '◉' }}
            </button>
          </div>
          <button class="btn ghost2" :disabled="mailBusy" @click="testMail">
            <span v-if="mailBusy" class="spin"></span>{{ mailBusy ? ' 发送中' : '发送测试邮件' }}
          </button>
        </div>
        <div v-if="mailResult.ok === true" class="note ok">✓ {{ mailResult.msg }}</div>
        <div v-else-if="mailResult.ok === false" class="note bad">✗ {{ mailResult.msg }}</div>
      </div>
    </section>

    <!-- ============ Webhook 提醒 (Next-⑤) ============ -->
    <section class="set-card">
      <header class="set-head">
        <div class="set-ico violet">⚡</div>
        <div>
          <h3>Webhook 提醒</h3>
          <p>提醒触发时把消息 POST 到你的地址（如飞书/钉钉/企业微信机器人）</p>
        </div>
      </header>

      <div class="set-row col">
        <div class="rl">
          <label>Webhook 地址
            <span v-if="whHasValue" style="font-size:12px;color:#16a34a;margin-left:6px;">● 已配置</span>
            <span v-else style="font-size:12px;color:#94a3b8;margin-left:6px;">○ 未配置</span>
          </label>
          <p>创建或编辑提醒时，将通道选为「含 Webhook」即可推送到此地址；留空则不使用</p>
        </div>
        <input v-model="form.webhook_url"
               :placeholder="whHasValue ? '已保存，留空则不修改' : 'https://open.feishu.cn/open-apis/bot/v2/hook/xxxx'" />
      </div>

      <div class="set-row col">
        <div class="rr auto" style="width: auto;">
          <button class="btn ghost2" :disabled="whBusy || (!form.webhook_url && !whHasValue)" @click="testWebhook">
            <span v-if="whBusy" class="spin"></span>{{ whBusy ? ' 发送中' : '发送测试消息' }}
          </button>
        </div>
        <div v-if="whResult.ok === true" class="note ok">✓ {{ whResult.msg }}</div>
        <div v-else-if="whResult.ok === false" class="note bad">✗ {{ whResult.msg }}</div>
      </div>

      <div v-if="!whHasValue" class="banner warn">
        <span class="bi">!</span>
        <div>尚未配置 Webhook 地址。配置后在「提醒」页把规则通道设为含 Webhook 即可启用多渠道推送。</div>
      </div>
    </section>

    <!-- ============ 基准对比 (Later-②) ============ -->
    <section class="set-card">
      <header class="set-head">
        <div class="set-ico blue">▦</div>
        <div>
          <h3>基准对比</h3>
          <p>在仪表盘趋势图叠加一条基准曲线，直观看到组合相对基准的超额收益</p>
        </div>
      </header>

      <div class="set-row">
        <div class="rl">
          <label>对比基准</label>
          <p>选择某个持仓标的作为基准；留空「无」则仅展示组合曲线</p>
        </div>
        <div class="rr">
          <select v-model="form.benchmark" class="sel">
            <option value="">无（不对比）</option>
            <option v-for="a in assetOptions" :key="a.id" :value="a.symbol">
              {{ a.symbol }} · {{ a.name }}
            </option>
          </select>
        </div>
      </div>
    </section>

    <!-- ============ 访问安全 ============ -->
    <section class="set-card">
      <header class="set-head">
        <div class="set-ico amber">⚿</div>
        <div>
          <h3>访问安全</h3>
          <p>为整个应用加一道口令，适合开放局域网访问时使用</p>
        </div>
      </header>

      <div class="set-row">
        <div class="rl">
          <label>
            访问口令
            <span class="pill mini" :class="app.authRequired ? 'green' : 'gray'">
              {{ app.authRequired ? '已启用' : '未启用' }}
            </span>
          </label>
          <p v-if="app.authRequired">已开启保护，浏览器会话 30 天内免重复登录</p>
          <p v-else>当前任何能访问该端口的人都可直接查看你的投资数据</p>
        </div>
        <div class="rr auto">
          <template v-if="app.authRequired">
            <button class="btn ghost2 sm" @click="openPin('change')">修改口令</button>
            <button class="btn danger sm" @click="openPin('clear')">关闭保护</button>
          </template>
          <button v-else class="btn sm" @click="openPin('set')">设置口令</button>
        </div>
      </div>

      <div v-if="!app.authRequired" class="banner warn">
        <span class="bi">!</span>
        <div>
          若用 <code>HOST=0.0.0.0</code> 开放局域网访问，<b>务必先设置访问口令</b>。仅本机使用可保持关闭。
        </div>
      </div>
    </section>

    <!-- ============ 数据备份 ============ -->
    <section class="set-card">
      <header class="set-head">
        <div class="set-ico slate">⤓</div>
        <div>
          <h3>数据备份</h3>
          <p>全部数据都在本地，建议定期导出留档</p>
        </div>
      </header>

      <div class="set-row">
        <div class="rl">
          <label>导出</label>
          <p>JSON 为全量备份（含标的、交易、提醒、现金）；CSV 仅交易流水，可用 Excel 打开</p>
        </div>
        <div class="rr auto">
          <a class="btn ghost2 sm" :href="Api.exportJSONUrl()" download>导出 JSON</a>
          <a class="btn ghost2 sm" :href="Api.exportCSVUrl('transactions')" download>导出 CSV</a>
        </div>
      </div>

      <div class="set-row">
        <div class="rl">
          <label>恢复</label>
          <p>从 JSON 备份还原</p>
        </div>
        <div class="rr auto">
          <input ref="fileInput" type="file" accept="application/json,.json" hidden @change="onFile" />
          <button class="btn ghost2 sm" @click="pickFile">选择备份文件…</button>
        </div>
      </div>

      <div class="banner danger">
        <span class="bi">!</span>
        <div>导入会<b>清空并覆盖</b>现有全部数据，此操作不可撤销。请先导出当前数据留档。</div>
      </div>
    </section>

    <!-- ============ 关于 ============ -->
    <section class="set-card">
      <header class="set-head">
        <div class="set-ico slate">i</div>
        <div>
          <h3>关于</h3>
          <p>InvestHub · 个人投资管理平台</p>
        </div>
      </header>
      <div class="about">
        <div><span>版本</span><b>{{ app.version || '—' }}</b></div>
        <div>
          <span>实时连接</span>
          <b :class="app.sseOpen ? 'ok' : 'bad'">{{ app.sseOpen ? '● 已连接' : '○ 未连接' }}</b>
        </div>
        <div><span>数据存储</span><b>本机 SQLite（data/investhub.db）</b></div>
        <div><span>技术栈</span><b>Go + Vue 3 + ECharts</b></div>
      </div>
    </section>

    <!-- ---------- PIN modal ---------- -->
    <ModalDialog
      v-if="pinModal"
      :title="pinTitle"
      :ok-text="pinMode === 'clear' ? '确认关闭' : '确认'"
      :busy="pinBusy"
      @close="pinModal = false"
      @ok="submitPin"
    >
      <div v-if="pinMode === 'clear'" class="banner danger" style="margin-top: 0">
        <span class="bi">!</span>
        <div>关闭后任何人都能直接访问，且当前所有登录会话会立即失效。</div>
      </div>

      <div v-if="pinMode !== 'set'" class="field">
        <label>当前口令</label>
        <input type="password" v-model="pinForm.oldPin" autocomplete="current-password" placeholder="请输入当前口令" />
      </div>
      <template v-if="pinMode !== 'clear'">
        <div class="field">
          <label>新口令</label>
          <input type="password" v-model="pinForm.pin" autocomplete="new-password" placeholder="至少 6 位" />
        </div>
        <div class="field">
          <label>确认新口令</label>
          <input type="password" v-model="pinForm.confirm" autocomplete="new-password" placeholder="再输入一次" />
        </div>
      </template>
      <div v-if="pinError" class="note bad">✗ {{ pinError }}</div>
    </ModalDialog>

    <!-- ---------- import confirm modal ---------- -->
    <ModalDialog
      v-if="importModal"
      title="确认导入备份"
      ok-text="覆盖并导入"
      :busy="importBusy"
      @close="importModal = false"
      @ok="doImport"
    >
      <div class="banner danger" style="margin-top: 0">
        <span class="bi">!</span>
        <div>现有全部数据将被<b>清空并替换</b>，此操作不可撤销。</div>
      </div>
      <div class="about" style="margin-top: 14px">
        <div><span>文件</span><b>{{ importInfo.name }}</b></div>
        <div><span>数据表</span><b>{{ importInfo.tables }} 张</b></div>
        <div><span>记录数</span><b>{{ importInfo.rows }} 条</b></div>
      </div>
    </ModalDialog>
  </div>
</template>

<style scoped>
.set-wrap { max-width: 880px; padding-bottom: 40px; }

/* ---- sticky unsaved-changes bar ---- */
.save-bar {
  position: sticky; top: 0; z-index: 6;
  display: flex; align-items: center; gap: 10px;
  padding: 10px 14px; margin-bottom: 16px;
  background: #fff; border: 1px solid var(--brand);
  border-radius: 12px; box-shadow: 0 6px 20px rgba(59, 91, 255, 0.16);
}
.save-bar .dot {
  width: 8px; height: 8px; border-radius: 50%; background: var(--brand);
  box-shadow: 0 0 0 4px rgba(59, 91, 255, 0.14);
}
.save-bar .sb-text { flex: 1; font-size: 13px; color: var(--ink); }
.save-bar .sb-text b { color: var(--brand); }
.savebar-enter-active, .savebar-leave-active { transition: all 0.18s ease; }
.savebar-enter-from, .savebar-leave-to { opacity: 0; transform: translateY(-8px); }

/* ---- section card ---- */
.set-card {
  background: var(--panel); border: 1px solid var(--line);
  border-radius: 14px; box-shadow: var(--shadow);
  padding: 4px 20px 18px; margin-bottom: 16px;
}
.set-head { display: flex; align-items: flex-start; gap: 12px; padding: 18px 0 14px; }
.set-head h3 { margin: 0; font-size: 15px; font-weight: 700; }
.set-head p { margin: 3px 0 0; font-size: 12px; color: var(--muted); line-height: 1.5; }
.set-ico {
  width: 34px; height: 34px; flex-shrink: 0; border-radius: 10px;
  display: grid; place-items: center; font-size: 16px; font-weight: 700;
}
.set-ico.blue { background: #eef1ff; color: var(--brand); }
.set-ico.green { background: #e7f7ee; color: var(--down); }
.set-ico.violet { background: #f2ecff; color: #7c4dff; }
.set-ico.amber { background: #fdf3e0; color: var(--warn); }
.set-ico.slate { background: #eef0f4; color: var(--muted); }
.set-ico.teal { background: #e3f7f4; color: #0d9488; }

/* ---- toggle switch ---- */
.switch { position: relative; display: inline-block; width: 42px; height: 24px; }
.switch input { position: absolute; inset: 0; opacity: 0; margin: 0; cursor: pointer; z-index: 1; }
.switch .track { position: absolute; inset: 0; background: #d4d9e2; border-radius: 999px; transition: 0.18s; }
.switch .track::before {
  content: ''; position: absolute; top: 3px; left: 3px;
  width: 18px; height: 18px; background: #fff; border-radius: 50%; transition: 0.18s;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.2);
}
.switch input:checked + .track { background: var(--brand); }
.switch input:checked + .track::before { transform: translateX(18px); }

/* ---- setting row ---- */
.set-row {
  display: flex; align-items: center; justify-content: space-between;
  gap: 24px; padding: 14px 0; border-top: 1px solid var(--line);
}
.set-row.col { flex-direction: column; align-items: stretch; gap: 10px; }
.rl label {
  display: flex; align-items: center; gap: 7px;
  font-size: 13px; font-weight: 600; color: var(--ink);
}
.rl p { margin: 3px 0 0; font-size: 12px; color: var(--muted); line-height: 1.5; }
.rr { width: 260px; flex-shrink: 0; }
.rr.auto { width: auto; display: flex; gap: 8px; }
.pill.mini { font-size: 10px; padding: 1px 7px; font-weight: 600; }

/* ---- segmented control ---- */
.seg { display: flex; background: #f1f3f9; border-radius: 10px; padding: 3px; }
.seg button {
  flex: 1; border: 0; background: transparent; color: var(--muted);
  padding: 7px 0; border-radius: 8px; font-size: 13px; font-weight: 600; transition: 0.15s;
}
.seg button.on { background: #fff; color: var(--brand); box-shadow: 0 1px 3px rgba(20, 30, 50, 0.12); }

/* ---- input with prefix/suffix ---- */
.input-affix {
  display: flex; align-items: center; gap: 8px;
  border: 1px solid var(--line); border-radius: 9px; padding: 0 11px; background: #fff;
  transition: border-color 0.15s;
}
.input-affix:focus-within { border-color: var(--brand); }
.input-affix.grow { flex: 1; }
.input-affix input { border: 0; padding: 9px 0; border-radius: 0; }
.input-affix input:focus { outline: none; }
.input-affix .pre, .input-affix .suf { font-size: 12px; color: var(--muted); white-space: nowrap; }
.input-affix .eye {
  border: 0; background: transparent; color: var(--muted); font-size: 14px; padding: 0 2px;
}
.input-affix .eye:hover { color: var(--brand); }
.key-row { display: flex; gap: 10px; align-items: stretch; }
.key-row .btn { white-space: nowrap; }

/* ---- data-source radio cards ---- */
.src-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px; padding-top: 14px; border-top: 1px solid var(--line); }
.src {
  position: relative; text-align: left; background: #fff; cursor: pointer;
  border: 1.5px solid var(--line); border-radius: 12px; padding: 13px 14px; transition: 0.15s;
  display: flex; flex-direction: column; gap: 3px;
}
.src:hover { border-color: var(--brand-2); background: #fbfcff; }
.src.on { border-color: var(--brand); background: #f7f9ff; }
.src-ico { font-size: 17px; color: var(--muted); line-height: 1.2; }
.src.on .src-ico { color: var(--brand); }
.src-name { font-size: 13px; font-weight: 700; color: var(--ink); }
.src-desc { font-size: 11px; color: var(--muted); line-height: 1.45; }
.src-check {
  position: absolute; top: 10px; right: 11px; width: 17px; height: 17px; border-radius: 50%;
  background: var(--brand); color: #fff; font-size: 10px; display: grid; place-items: center;
  opacity: 0; transform: scale(0.6); transition: 0.15s;
}
.src.on .src-check { opacity: 1; transform: scale(1); }

/* ---- inline notes ---- */
.note { font-size: 12px; padding: 8px 11px; border-radius: 8px; line-height: 1.5; }
.note.ok { background: #e7f7ee; color: var(--down); }
.note.bad { background: #fdeaea; color: var(--up); }

/* ---- banners ---- */
.banner {
  display: flex; gap: 10px; align-items: flex-start;
  font-size: 12px; line-height: 1.6; padding: 11px 13px; border-radius: 10px; margin-top: 14px;
}
.banner code { background: rgba(0, 0, 0, 0.06); padding: 1px 5px; border-radius: 4px; font-size: 11px; }
.banner .bi {
  width: 16px; height: 16px; flex-shrink: 0; border-radius: 50%; margin-top: 1px;
  display: grid; place-items: center; font-size: 11px; font-weight: 700; color: #fff;
}
.banner.warn { background: #fdf3e0; color: #8a5a00; }
.banner.warn .bi { background: var(--warn); }
.banner.danger { background: #fdeaea; color: #9d2b2b; }
.banner.danger .bi { background: var(--up); }

/* ---- about list ---- */
.about { border-top: 1px solid var(--line); padding-top: 6px; }
.about > div {
  display: flex; justify-content: space-between; align-items: center;
  padding: 8px 0; font-size: 13px; border-bottom: 1px dashed var(--line);
}
.about > div:last-child { border-bottom: 0; }
.about span { color: var(--muted); font-size: 12px; }
.about b { font-weight: 600; font-variant-numeric: tabular-nums; }
.about b.ok { color: var(--down); }
.about b.bad { color: var(--muted); }

/* ---- FX table ---- */
.loading-line { font-size: 13px; color: var(--muted); padding: 12px 0; }
.fx-table { width: 100%; border-collapse: collapse; margin-top: 6px; }
.fx-table th {
  text-align: left; font-size: 12px; font-weight: 600; color: var(--muted);
  padding: 8px 6px; border-bottom: 1px solid var(--line);
}
.fx-table td { padding: 7px 6px; border-bottom: 1px dashed var(--line); vertical-align: middle; }
.fx-table td.num { text-align: left; }
.fx-table td.act { text-align: right; width: 70px; }
.rate-in {
  width: 120px; padding: 7px 9px; border: 1px solid var(--line); border-radius: 8px;
  font-size: 13px; font-variant-numeric: tabular-nums; background: #fff; color: var(--ink);
}
.rate-in:focus { outline: none; border-color: var(--brand); }
.rate-in:disabled { background: #f3f5f9; color: var(--muted); cursor: not-allowed; }
.cur-badge {
  display: inline-block; min-width: 46px; text-align: center; padding: 3px 8px;
  font-size: 12px; font-weight: 700; border-radius: 7px; background: #eef0f4; color: var(--ink);
}
.cur-badge.base { background: #e7f7ee; color: var(--down); }
.fx-add { display: flex; align-items: center; gap: 10px; margin-top: 12px; }
.cur-in {
  padding: 8px 9px; border: 1px solid var(--line); border-radius: 8px; font-size: 13px;
  text-transform: uppercase; letter-spacing: 0.04em; background: #fff; color: var(--ink);
}
.cur-in:focus { outline: none; border-color: var(--brand); }
.danger-txt { color: var(--up); }
.muted.mini { font-size: 11px; color: var(--muted); }

/* ---- select ---- */
.sel {
  width: 100%; padding: 9px 11px; border: 1px solid var(--line); border-radius: 9px;
  font-size: 13px; background: #fff; color: var(--ink); appearance: none;
}
.sel:focus { outline: none; border-color: var(--brand); }

/* ---- responsive ---- */
@media (max-width: 760px) {
  .set-row { flex-direction: column; align-items: stretch; gap: 10px; }
  .rr, .rr.auto { width: 100%; }
  .src-grid { grid-template-columns: 1fr; }
  .key-row { flex-direction: column; }
}
</style>
