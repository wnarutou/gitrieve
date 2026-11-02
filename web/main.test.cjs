const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const pagination = require('./static/js/pagination.js');

function themePage({ stored = null, dark = false, storageBlocked = false } = {}) {
    const listeners = {};
    const select = {
        value: '',
        addEventListener(type, listener) { listeners['select:' + type] = listener; },
    };
    const root = { dataset: {} };
    const media = {
        matches: dark,
        addEventListener(type, listener) { listeners['media:' + type] = listener; },
    };
    const storage = new Map(stored === null ? [] : [['gitrieve-theme', stored]]);
    const context = vm.createContext({
        document: {
            documentElement: root,
            getElementById(id) { return id === 'theme-select' ? select : null; },
            addEventListener(type, listener) { listeners[type] = listener; },
        },
        window: {
            matchMedia() { return media; },
            localStorage: {
                getItem(key) {
                    if (storageBlocked) throw new Error('Storage blocked');
                    return storage.get(key) ?? null;
                },
                setItem(key, value) {
                    if (storageBlocked) throw new Error('Storage blocked');
                    storage.set(key, value);
                },
            },
        },
    });
    // Run the template's blocking head scripts, as the browser does before paint.
    const html = fs.readFileSync(path.join(__dirname, 'templates/index.html'), 'utf8');
    const head = html.split('</head>')[0];
    for (const match of head.matchAll(/<script src="(\/static\/[^\"]+)"\s*><\/script>/g)) {
        vm.runInContext(fs.readFileSync(path.join(__dirname, match[1]), 'utf8'), context);
    }
    return {
        root, select, storage,
        ready() { listeners.DOMContentLoaded?.(); },
        choose(value) {
            select.value = value;
            listeners['select:change']?.({ target: select });
        },
        systemDark(value) {
            media.matches = value;
            listeners['media:change']?.({ matches: value });
        },
    };
}

test('theme follows the system before the page is painted', () => {
    for (const [dark, expected] of [[false, 'light'], [true, 'dark']]) {
        const page = themePage({ dark });
        assert.equal(page.root.dataset.theme, expected);
        page.ready();
        assert.equal(page.select.value, 'system');
    }
});

test('explicit theme overrides the system and is restored after reload', () => {
    const page = themePage({ dark: true });
    page.ready();
    page.choose('light');
    assert.equal(page.root.dataset.theme, 'light');
    const reloaded = themePage({ stored: page.storage.get('gitrieve-theme'), dark: true });
    assert.equal(reloaded.root.dataset.theme, 'light');
    reloaded.ready();
    assert.equal(reloaded.select.value, 'light');
    reloaded.choose('dark');
    const darkReloaded = themePage({ stored: reloaded.storage.get('gitrieve-theme'), dark: false });
    assert.equal(darkReloaded.root.dataset.theme, 'dark');
});

test('system changes apply only in system mode and resume when reselected', () => {
    const page = themePage();
    page.ready();
    page.systemDark(true);
    assert.equal(page.root.dataset.theme, 'dark');
    page.choose('light');
    page.systemDark(false);
    page.systemDark(true);
    assert.equal(page.root.dataset.theme, 'light');
    page.choose('system');
    assert.equal(page.root.dataset.theme, 'dark');
    page.systemDark(false);
    assert.equal(page.root.dataset.theme, 'light');
    const reloaded = themePage({ stored: page.storage.get('gitrieve-theme'), dark: true });
    reloaded.ready();
    assert.equal(reloaded.select.value, 'system');
    assert.equal(reloaded.root.dataset.theme, 'dark');
});

test('an invalid saved theme falls back to system preference', () => {
    const page = themePage({ stored: 'invalid', dark: true });
    assert.equal(page.root.dataset.theme, 'dark');
    page.ready();
    assert.equal(page.select.value, 'system');
});

test('theme still initializes and switches when browser storage is blocked', () => {
    const page = themePage({ storageBlocked: true });
    assert.equal(page.root.dataset.theme, 'light');
    page.ready();
    page.choose('dark');
    assert.equal(page.root.dataset.theme, 'dark');
    page.systemDark(false);
    assert.equal(page.root.dataset.theme, 'dark');
});

test('renders repository actions before the repository name', async () => {
    const app = { innerHTML: '' };
    const control = () => ({
        addEventListener() {},
        value: '',
        checked: false,
    });
    const controls = {
        '#btn-add-repo': control(),
        '#btn-refresh-repos': control(),
        '#repos-search': control(),
        '#btn-search-repos': control(),
        '#repos-health': control(),
        '#repos-overdue': control(),
        '#repos-sort': control(),
        '#repos-direction': control(),
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
                        health_status: 'healthy',
                        last_status: 'completed',
                        total_runs: 0,
                        success_runs: 0,
                        failed_runs: 0,
                    }],
                    summary: { total: 1, healthy: 1 },
                    total: 1,
                },
            }),
        }),
        location: { hash: '#/repositories' },
        setInterval() {},
        setTimeout() {},
        clearTimeout() {},
        URLSearchParams,
        window: { addEventListener() {}, GitrievePagination: pagination },
    });

    const scriptPath = path.join(__dirname, 'static', 'js', 'main.js');
    vm.runInContext(fs.readFileSync(scriptPath, 'utf8'), context);
    await vm.runInContext("state.activeRoute = 'repositories'; state.routeEpoch = 1; renderRepositories(1)", context);

    const headerCells = [...app.innerHTML.matchAll(/<th(?:\s[^>]*)?>([\s\S]*?)<\/th>/g)]
        .map((match) => match[1].trim());
    const firstRow = app.innerHTML.match(/<tbody>\s*<tr>([\s\S]*?)<\/tr>/);
    assert.ok(firstRow, 'expected a rendered repository row');
    const bodyCells = [...firstRow[1].matchAll(/<td(?:\s+class="([^"]*)")?[^>]*>([\s\S]*?)<\/td>/g)];

    const actionIndex = headerCells.indexOf('Actions');
    const nameIndex = headerCells.indexOf('Name / URL');
    assert.ok(actionIndex >= 0, 'expected an Actions column');
    assert.ok(nameIndex > actionIndex, 'expected Actions before the repository name');
    assert.equal(bodyCells[0][1], 'actions');
    assert.match(bodyCells[0][2], /btn-run-repo/);
    assert.match(bodyCells[nameIndex][2], /<strong>gitrieve<\/strong>/);
});
