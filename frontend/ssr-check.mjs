// Headless render check: compiles + renders SettingsView through Vite SSR so we
// catch runtime template errors without needing a browser. Not part of the build.
import { createSSRApp } from 'vue';
import { renderToString } from 'vue/server-renderer';
import { createPinia } from 'pinia';
import { createServer } from 'vite';

const vite = await createServer({
  root: process.cwd(),
  server: { middlewareMode: true },
  appType: 'custom',
  logLevel: 'error',
});

let failed = false;
try {
  const { default: SettingsView } = await vite.ssrLoadModule('/src/views/SettingsView.vue');
  const app = createSSRApp(SettingsView);
  app.use(createPinia());
  app.config.warnHandler = (msg) => {
    if (!/Hydration|onMounted/.test(msg)) console.log('  [warn]', msg.slice(0, 140));
  };
  const html = await renderToString(app);

  const checks = [
    ['分区卡片 set-card', (html.match(/set-card/g) || []).length >= 6],
    ['通用分区', html.includes('主显示币种')],
    // NB: Vue SSR emits dynamic classes first (`class="on src"`), so match the
    // child spans instead of the button's own class list.
    ['数据源三选项', (html.match(/class="src-name"/g) || []).length === 3],
    ['选中态高亮', /class="on src"/.test(html)],
    ['AI 分区', html.includes('API Key')],
    ['访问安全分区', html.includes('访问口令')],
    ['数据备份分区', html.includes('导出 JSON')],
    ['关于分区', html.includes('技术栈')],
    ['导出链接 href', html.includes('/api/v1/data/export')],
    ['CSV 导出链接', html.includes('export.csv')],
    ['币种分段控件', html.includes('¥ CNY') && html.includes('$ USD')],
    ['未启用时警告 banner', html.includes('HOST=0.0.0.0')],
    ['保存栏默认隐藏', !html.includes('项修改未保存')],
    ['弹窗默认不渲染', !html.includes('确认导入备份')],
  ];
  console.log('=== SSR 渲染检查 ===');
  for (const [name, ok] of checks) {
    console.log(`  ${ok ? '✓' : '✗'} ${name}`);
    if (!ok) failed = true;
  }
  console.log(`\n  渲染 HTML 长度: ${html.length} 字节`);
} catch (e) {
  failed = true;
  console.error('=== 渲染失败 ===\n', e.stack || e.message);
}
await vite.close();
process.exit(failed ? 1 : 0);
