// Thin fetch wrapper around the {code, data, msg} envelope used by the Go API.

const BASE = '/api/v1';

export class ApiError extends Error {
  constructor(code, msg) {
    super(msg);
    this.code = code;
  }
}

async function request(method, path, body, query) {
  let url = BASE + path;
  if (query) {
    const qs = new URLSearchParams();
    Object.entries(query).forEach(([k, v]) => {
      if (v !== undefined && v !== null && v !== '') qs.append(k, v);
    });
    const s = qs.toString();
    if (s) url += '?' + s;
  }
  const opts = { method, credentials: 'include', headers: {} };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(url, opts);
  if (!res.ok) throw new ApiError(res.status, `HTTP ${res.status}`);
  const json = await res.json();
  if (json.code !== 0) throw new ApiError(json.code, json.msg || '请求失败');
  return json.data;
}

export const api = {
  get: (p, q) => request('GET', p, undefined, q),
  post: (p, b) => request('POST', p, b ?? {}),
  put: (p, b) => request('PUT', p, b ?? {}),
  del: (p, q) => request('DELETE', p, undefined, q),
  downloadUrl: (p, q) => {
    const qs = q ? '?' + new URLSearchParams(q).toString() : '';
    return BASE + p + qs;
  },
};

// ---- domain helpers -------------------------------------------------------

export const Api = {
  authStatus: () => api.get('/auth/status'),
  login: (pin) => api.post('/auth/login', { pin }),
  logout: () => api.post('/auth/logout'),
  setPin: (pin, oldPin) => api.put('/auth/pin', { pin, oldPin }),

  summary: () => api.get('/dashboard/summary'),
  trend: (range) => api.get('/pnl/trend', { range }),

  assets: (category) => api.get('/assets', { category }),
  createAsset: (b) => api.post('/assets', b),
  updateAsset: (id, b) => api.put(`/assets/${id}`, b),
  deleteAsset: (id, mode) => api.del(`/assets/${id}`, { mode }),
  quote: (id) => api.get(`/assets/${id}/quote`),
  kline: (id, period, limit) => api.get(`/assets/${id}/kline`, { period, limit }),
  indicators: (id) => api.get(`/assets/${id}/indicators`),
  position: (id) => api.get(`/assets/${id}/position`),
  positions: (category) => api.get('/positions', { category }),

  transactions: (q) => api.get('/transactions', q),
  createTx: (b) => api.post('/transactions', b),
  updateTx: (id, b) => api.put(`/transactions/${id}`, b),
  deleteTx: (id) => api.del(`/transactions/${id}`),

  analyze: (b) => api.post('/ai/analyze', b),
  analyses: (q) => api.get('/ai/analyses', q),
  analysis: (id) => api.get(`/ai/analyses/${id}`),
  deleteAnalysis: (id) => api.del(`/ai/analyses/${id}`),

  alertRules: () => api.get('/alerts'),
  createAlert: (b) => api.post('/alerts', b),
  updateAlert: (id, b) => api.put(`/alerts/${id}`, b),
  deleteAlert: (id) => api.del(`/alerts/${id}`),
  alertEvents: (read) => api.get('/alerts/events', { read }),
  markAlertRead: (id) => api.post(`/alerts/events/${id}/read`),

  settings: () => api.get('/settings'),
  saveSettings: (b) => api.put('/settings', b),
  testAI: (apiKey, model) => api.post('/settings/ai-test', { apiKey, model }),
  testMail: () => api.post('/settings/mail-test'),
  testWebhook: () => api.post('/settings/webhook-test'),

  cash: () => api.get('/cash-accounts'),
  createCash: (b) => api.post('/cash-accounts', b),
  updateCash: (id, b) => api.put(`/cash-accounts/${id}`, b),
  deleteCash: (id) => api.del(`/cash-accounts/${id}`),

  watchlist: () => api.get('/watchlist'),
  addWatch: (b) => api.post('/watchlist', b),
  updateWatch: (id, b) => api.put(`/watchlist/${id}`, b),
  removeWatch: (id) => api.del(`/watchlist/${id}`),

  fxRates: () => api.get('/settings/fx'),
  saveFx: (b) => api.put('/settings/fx', b),

  importJSON: (b) => api.post('/data/import', b),
  importCSV: (csv) => api.post('/data/import.csv', { csv }),
  exportJSONUrl: () => api.downloadUrl('/data/export'),
  exportCSVUrl: (scope) => api.downloadUrl('/data/export.csv', { scope }),
};
