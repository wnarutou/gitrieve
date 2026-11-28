const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { pathToFileURL } = require('node:url');

const browser = [
    process.env.GITRIEVE_TEST_BROWSER,
    'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
    'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
    '/usr/bin/chromium',
    '/usr/bin/chromium-browser',
    '/usr/bin/google-chrome',
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
].find(candidate => candidate && fs.existsSync(candidate));

test('all pages use available width and keep wide tables locally scrollable', {
    skip: browser ? false : 'Set GITRIEVE_TEST_BROWSER to a Chromium browser executable',
}, () => {
    const web = path.join(__dirname, '..');
    const read = file => fs.readFileSync(path.join(web, file), 'utf8');
    const css = read('static/css/main.css');
    const scripts = ['theme', 'pagination', 'main'].map(name =>
        `<script>${read(`static/js/${name}.js`).replace(/<\/script/gi, '<\\/script')}</script>`).join('');
    // Only API responses are stubbed; use the actual shell, CSS and page renderers.
    const responses = {
        '/api/maintenance': { enabled: false },
        '/api/metrics': { uptime: '1h' },
        '/api/jobs': { total: 1, jobs: [{
            id: 'job-1', name: 'archive', url: 'github.com/acme/repository', status: 'completed',
        }] },
        '/api/repositories': { total: 1, summary: { total: 1, healthy: 1 }, repositories: [{
            Name: 'repository', URL: 'github.com/acme/repository', Storage: [], health_status: 'healthy',
        }] },
        '/api/storage': [{ Name: 'backup', Type: 'file', Path: '/backups/repositories' }],
        '/api/config/export': { yaml: 'repository: []' },
    };
    const shell = read('templates/index.html')
        .replace(/<script\b[^>]*>[\s\S]*?<\/script>/g, '')
        .replace(/<link\b[^>]*rel="stylesheet"[^>]*>/g, `<style>${css}</style>`)
        .replace('</body>', () => `<script>
            const responses = ${JSON.stringify(responses)};
            window.fetch = async url => {
                const key = url.split('?')[0];
                if (!(key in responses)) throw new Error('Unexpected API request: ' + url);
                return { ok: true, json: async () => ({ data: responses[key] }) };
            };
        </script>${scripts}</body>`);
    const cases = [1920, 2560, 1280, 375].flatMap(width =>
        ['jobs', 'repositories', 'storage', 'config'].map(route => ({ width, route })));
    const frames = cases.map(({ width, route }) => {
        const page = shell.replace('<head>', `<head><script>location.hash = '#/${route}';</script>`);
        return `<iframe style="width:${width}px;height:900px;border:0" srcdoc="${page
            .replace(/&/g, '&amp;').replace(/"/g, '&quot;')}"></iframe>`;
    }).join('');
    const fixture = `<!doctype html><body>${frames}<pre id="results"></pre><script>
        window.addEventListener('load', () => setTimeout(() => {
            const cases = ${JSON.stringify(cases)};
            const results = [...document.querySelectorAll('iframe')].map((frame, i) => {
                const doc = frame.contentDocument;
                const app = doc.querySelector('.app');
                const content = app.getBoundingClientRect();
                const style = frame.contentWindow.getComputedStyle(app);
                const bar = doc.querySelector('.maintenance-bar').getBoundingClientRect();
                const wrap = doc.querySelector('.table-wrap');
                const result = {
                    ...cases[i], rendered: !!app.querySelector('.panel'),
                    activeRoute: doc.querySelector('.nav-links a.active')?.dataset.route,
                    width: content.width, padding: parseFloat(style.paddingLeft),
                    contentLeft: content.left + parseFloat(style.paddingLeft),
                    contentRight: content.right - parseFloat(style.paddingRight),
                    barLeft: bar.left, barRight: bar.right,
                    viewport: doc.documentElement.clientWidth,
                    documentWidth: doc.documentElement.scrollWidth,
                };
                if (wrap) {
                    wrap.querySelector('table').style.minWidth = '3000px';
                    wrap.scrollLeft = 100;
                    result.tableScrolls = wrap.scrollLeft > 0;
                    result.documentWidthAfterWideTable = doc.documentElement.scrollWidth;
                }
                return result;
            });
            document.querySelector('#results').textContent = JSON.stringify(results);
        }, 50));
    </script></body>`;
    const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'gitrieve-layout-'));
    try {
        const file = path.join(temp, 'layout.html');
        fs.writeFileSync(file, fixture);
        const run = spawnSync(browser, [
            '--headless', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
            `--user-data-dir=${path.join(temp, 'profile')}`,
            '--virtual-time-budget=3000', '--dump-dom', pathToFileURL(file).href,
        ], { encoding: 'utf8', timeout: 30000, maxBuffer: 16 * 1024 * 1024 });
        assert.ifError(run.error);
        assert.equal(run.status, 0, run.stderr);
        const output = run.stdout.match(/<pre id="results">([^<]+)<\/pre>/);
        assert.ok(output, 'browser did not return layout measurements');
        const results = JSON.parse(output[1]);
        assert.equal(results.length, cases.length);
        for (const result of results) {
            const label = `${result.route} at ${result.viewport}px`;
            assert.ok(result.rendered, `${label}: page did not render`);
            assert.equal(result.activeRoute, result.route, `${label}: wrong page rendered`);
            assert.equal(result.width, result.viewport, `${label}: main width is capped`);
            assert.equal(result.padding, result.viewport <= 640 ? 12 : 24, `${label}: page gutter`);
            assert.equal(result.barLeft, result.contentLeft, `${label}: maintenance left alignment`);
            assert.equal(result.barRight, result.contentRight, `${label}: maintenance right alignment`);
            assert.equal(result.documentWidth, result.viewport, `${label}: document overflows`);
            if (result.tableScrolls !== undefined) {
                assert.ok(result.tableScrolls, `${label}: wide table cannot scroll`);
                assert.equal(result.documentWidthAfterWideTable, result.viewport,
                    `${label}: wide table overflows the document`);
            }
        }
    } finally {
        fs.rmSync(temp, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
    }
});
