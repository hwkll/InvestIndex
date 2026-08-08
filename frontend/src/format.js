// Shared number / date formatting helpers.

const CUR_SIGN = { CNY: '¥', USD: '$', HKD: 'HK$', EUR: '€' };

export function sign(cur) {
  return CUR_SIGN[cur] || '';
}

export function money(v, cur = 'CNY', digits = 2) {
  if (v === null || v === undefined || Number.isNaN(v)) return '--';
  return sign(cur) + Number(v).toLocaleString('zh-CN', { minimumFractionDigits: digits, maximumFractionDigits: digits });
}

export function num(v, digits = 2) {
  if (v === null || v === undefined || Number.isNaN(v)) return '--';
  return Number(v).toLocaleString('zh-CN', { maximumFractionDigits: digits });
}

export function price(v) {
  if (v === null || v === undefined || Number.isNaN(v)) return '--';
  const abs = Math.abs(v);
  const digits = abs >= 100 ? 2 : abs >= 1 ? 3 : 6;
  return Number(v).toLocaleString('zh-CN', { maximumFractionDigits: digits });
}

export function pct(v, digits = 2) {
  if (v === null || v === undefined || Number.isNaN(v)) return '--';
  return (v >= 0 ? '+' : '') + (v * 100).toFixed(digits) + '%';
}

export function pctRaw(v, digits = 2) {
  if (v === null || v === undefined || Number.isNaN(v)) return '--';
  return (v >= 0 ? '+' : '') + Number(v).toFixed(digits) + '%';
}

export function signed(v, cur = 'CNY') {
  if (v === null || v === undefined || Number.isNaN(v)) return '--';
  return (v >= 0 ? '+' : '-') + money(Math.abs(v), cur);
}

// 涨红跌绿（A 股习惯）
export function dirClass(v) {
  if (v === null || v === undefined || Number.isNaN(v) || Math.abs(v) < 1e-10) return 'muted';
  return v > 0 ? 'up' : 'down';
}

export function date(ts, withTime = false) {
  if (!ts) return '--';
  const d = new Date(Number(ts));
  const p = (n) => String(n).padStart(2, '0');
  const base = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  return withTime ? `${base} ${p(d.getHours())}:${p(d.getMinutes())}` : base;
}

export function dateInput(ts) {
  const d = ts ? new Date(Number(ts)) : new Date();
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

export const CATEGORY_LABEL = { crypto: '加密货币', fund: '基金', gold: '黄金', stock: '股票' };
export const CATEGORIES = ['crypto', 'fund', 'gold', 'stock'];

export const SIGNAL_LABEL = { buy: '买入', sell: '卖出', hold: '持有', watch: '观望' };

export function signalClass(s) {
  return 'sig-' + (s || 'hold');
}

export const CHART_PALETTE = ['#3b5bff', '#6f8bff', '#e08a00', '#1faa59', '#e23b3b', '#8a63d2', '#00a3a3'];
