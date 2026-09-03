const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

test('renders repository actions before the repository name', async () => {
    const app = { innerHTML: '' };
    const control = () => ({
        addEventListener() {},
        value: '',
    });
    const controls = {
        '#btn-add-repo': control(),
        '#btn-refresh-repos': control(),
        '#repos-search': control(),
        '#btn-search-repos': control(),
    };
    const document = {
        addEventListener() {},
        querySelector(selector) {
            if (selector === '#app') return app;
            return controls[selector] || null;
        },
        querySelectorAll() {
            return [];
        },
    };
    const context = vm.createContext({
        document,
        fetch: async () => ({
            ok: true,
            status: 200,
            json: async () => ({
                data: {
                    repositories: [{
                        Name: 'gitrieve',
                        URL: 'github.com/wnarutou/gitrieve',
                        Type: 'repo',
                        Storage: [],
                        total_runs: 0,
                        success_runs: 0,
                        failed_runs: 0,
                    }],
                    total: 1,
                },
            }),
        }),
        location: { hash: '#/repositories' },
        setInterval() {},
        URLSearchParams,
        window: { addEventListener() {} },
    });

    const scriptPath = path.join(__dirname, 'static', 'js', 'main.js');
    vm.runInContext(fs.readFileSync(scriptPath, 'utf8'), context);
    await vm.runInContext('renderRepositories()', context);

    const headerCells = [...app.innerHTML.matchAll(/<th(?:\s[^>]*)?>([\s\S]*?)<\/th>/g)]
        .map((match) => match[1].trim());
    const firstRow = app.innerHTML.match(/<tbody>\s*<tr>([\s\S]*?)<\/tr>/);
    assert.ok(firstRow, 'expected a rendered repository row');
    const bodyCells = [...firstRow[1].matchAll(/<td(?:\s+class="([^"]*)")?[^>]*>([\s\S]*?)<\/td>/g)];

    assert.deepEqual(headerCells.slice(0, 2), ['', 'Name']);
    assert.equal(bodyCells[0][1], 'actions');
    assert.match(bodyCells[0][2], /btn-run-repo/);
    assert.match(bodyCells[1][2], /<strong>gitrieve<\/strong>/);
});
