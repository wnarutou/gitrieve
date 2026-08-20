const $ = (sel, root) => (root || document).querySelector(sel);
const $$ = (sel, root) => Array.from((root || document).querySelectorAll(sel));

function esc(value) {
    return String(value === null || value === undefined ? '' : value).replace(/[&<>"']/g, c => ({
        '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    }[c]));
}

// repoKey 返回仓库身份键。后端以规范化 URL 为唯一身份，前端不做规范化。
const repoKey = (r) => r.URL || '';

function fmtTime(value) {
    if (!value) return '-';
    const d = new Date(value);
    if (isNaN(d.getTime())) return esc(value);
    return d.toLocaleString();
}

function fmtDuration(start, end) {
    if (!start) return '-';
    const s = new Date(start);
    if (isNaN(s.getTime())) return '-';
    const e = end ? new Date(end) : new Date();
    if (isNaN(e.getTime())) return '-';
    let sec = Math.max(0, Math.floor((e - s) / 1000));
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    const r = sec % 60;
    if (h > 0) return h + 'h ' + m + 'm';
    if (m > 0) return m + 'm ' + r + 's';
    return r + 's';
}

async function api(path, opts) {
    opts = opts || {};
    const resp = await fetch(path, Object.assign({}, opts, {
        headers: Object.assign({ 'Content-Type': 'application/json' }, opts.headers || {})
    }));
    let body = null;
    try { body = await resp.json(); } catch (e) { /* ignore */ }
    if (!resp.ok || (body && typeof body.code === 'number' && body.code >= 400)) {
        const err = new Error((body && body.message) || ('HTTP ' + resp.status));
        if (body && body.data && Array.isArray(body.data.errors)) err.errors = body.data.errors;
        throw err;
    }
    return body ? body.data : null;
}

function paginationHTML(page, pages, total, idPrefix) {
    return `
        <div class="pagination">
            <button class="btn btn-sm" id="pg-prev-${idPrefix}" ${page > 1 ? '' : 'disabled'}>Prev</button>
            <span class="pg-info">Page ${page} of ${pages} (${total} total)</span>
            <button class="btn btn-sm" id="pg-next-${idPrefix}" ${page < pages ? '' : 'disabled'}>Next</button>
        </div>`;
}

function optionsCell(r) {
    const parts = [];
    if (r.UseCache) parts.push('cache');
    if (r.AllBranches) parts.push('allBranches');
    if (r.DownloadReleases) parts.push('releases');
    if (r.DownloadIssues) parts.push('issues');
    if (r.DownloadWiki) parts.push('wiki');
    if (r.DownloadDiscussion) parts.push('discussion');
    return parts.length ? esc(parts.join(' ')) : '-';
}

const state = {
    jobsPage: 1,
    jobsStatus: '',
    jobsRepo: '',
    reposPage: 1,
    reposSearch: '',
    es: null,
    logJob: null,
    logIds: {}
};

function toast(message, isError) {
    const el = $('#toast');
    el.textContent = message;
    el.className = 'toast ' + (isError ? 'error' : '');
    clearTimeout(el._t);
    el._t = setTimeout(() => { el.className = 'toast hidden'; }, 4000);
}

function setActiveNav(route) {
    $$('.nav-links a').forEach(a => a.classList.toggle('active', a.dataset.route === route));
}

function renderApp() {
    const hash = (location.hash || '#/jobs').replace(/^#\/?/, '');
    const parts = hash.split('/');
    const route = parts[0] || 'jobs';
    setActiveNav(route);
    if (route === 'repositories') renderRepositories();
    else if (route === 'storage') renderStorage();
    else if (route === 'config') renderConfig();
    else renderJobs();
}

window.addEventListener('hashchange', renderApp);

async function refreshMetrics() {
    const dot = $('#server-status');
    try {
        const data = await api('/api/metrics');
        dot.className = 'status-dot ok';
        $('#metrics').textContent = 'uptime ' + (data.uptime || '');
    } catch (e) {
        dot.className = 'status-dot err';
        $('#metrics').textContent = 'offline';
    }
}

function statusBadge(status) {
    const cls = ['pending', 'running', 'completed', 'failed', 'cancelled'].includes(status) ? status : 'pending';
    return '<span class="badge ' + cls + '">' + esc(status) + '</span>';
}

function jobsToolbar(jobCount) {
    return `
        <div class="panel">
            <div class="toolbar">
                <div class="toolbar-group">
                    <input type="text" id="jobs-repo-filter" placeholder="Filter by repository name or URL…" value="${esc(state.jobsRepo)}">
                    <button id="btn-search-jobs" class="btn">Search</button>
                </div>
                <div class="toolbar-group">
                    <select id="jobs-status">
                        <option value="">All statuses</option>
                        <option value="pending">Pending</option>
                        <option value="running">Running</option>
                        <option value="completed">Completed</option>
                        <option value="failed">Failed</option>
                        <option value="cancelled">Cancelled</option>
                    </select>
                    <button id="btn-refresh" class="btn">Refresh</button>
                </div>
                <div class="toolbar-group toolbar-count">${jobCount} job(s)</div>
            </div>
        </div>`;
}

function jobsTable(jobs, page, limit, total) {
    if (!jobs.length) {
        return '<div class="empty">No jobs yet. Run a repository from the <strong>Repositories</strong> page, or click <strong>Refresh</strong>.</div>';
    }
    const rows = jobs.map(j => `
        <tr>
            <td>${statusBadge(j.status)}</td>
            <td><strong>${esc(j.name)}</strong></td>
            <td class="muted">${esc(j.url || '-')}</td>
            <td>${fmtTime(j.start_time)}</td>
            <td>${fmtTime(j.end_time)}</td>
            <td>${fmtDuration(j.start_time, j.end_time)}</td>
            <td class="err-cell" title="${esc(j.error_message)}">${esc(j.error_message || '-')}</td>
            <td class="actions">
                <button class="btn btn-sm btn-log" data-jobid="${esc(j.id)}" data-jobname="${esc(j.name)}">Logs</button>
                ${(j.status === 'running' || j.status === 'pending') ?
                    '<button class="btn btn-sm btn-danger btn-cancel" data-jobid="' + esc(j.id) + '">Cancel</button>' : ''}
            </td>
        </tr>`).join('');

    const pages = Math.max(1, Math.ceil(total / limit));
    return `
        <div class="table-wrap">
            <table class="table">
                <thead>
                    <tr>
                        <th>Status</th><th>Job</th><th>Repository</th>
                        <th>Started</th><th>Finished</th><th>Duration</th>
                        <th>Message</th><th></th>
                    </tr>
                </thead>
                <tbody>${rows}</tbody>
            </table>
        </div>
        ${paginationHTML(page, pages, total, 'jobs')}`;
}

async function renderJobs() {
    $('#app').innerHTML = '<div class="loading">Loading jobs\u2026</div>';

    const params = new URLSearchParams({ page: state.jobsPage, limit: 20 });
    if (state.jobsStatus) params.set('status', state.jobsStatus);
    if (state.jobsRepo) params.set('repository', state.jobsRepo);

    let jobs = [], total = 0;
    try {
        const data = await api('/api/jobs?' + params.toString());
        jobs = (data && data.jobs) || [];
        total = (data && data.total) || 0;
    } catch (e) {
        $('#app').innerHTML = '<div class="empty error-text">Failed to load jobs: ' + esc(e.message) + '</div>';
        return;
    }

    $('#app').innerHTML = jobsToolbar(total) + jobsTable(jobs, state.jobsPage, 20, total);

    $('#btn-refresh').addEventListener('click', () => renderJobs());
    $('#jobs-status').value = state.jobsStatus;
    $('#jobs-status').addEventListener('change', (ev) => {
        state.jobsStatus = ev.target.value;
        state.jobsPage = 1;
        renderJobs();
    });

    // Query only on explicit action (Search button or Enter), never on every
    // keystroke, so the input keeps focus while typing.
    const applyJobFilter = () => {
        state.jobsRepo = $('#jobs-repo-filter').value.trim();
        state.jobsPage = 1;
        renderJobs();
    };
    $('#btn-search-jobs').addEventListener('click', applyJobFilter);
    $('#jobs-repo-filter').addEventListener('keydown', (ev) => {
        if (ev.key === 'Enter') {
            ev.preventDefault();
            applyJobFilter();
        }
    });

    const prev = $('#pg-prev-jobs');
    const next = $('#pg-next-jobs');
    if (prev) prev.addEventListener('click', () => { if (state.jobsPage > 1) { state.jobsPage--; renderJobs(); } });
    if (next) next.addEventListener('click', () => { state.jobsPage++; renderJobs(); });

    $$('.btn-log').forEach(b => b.addEventListener('click', () => openLogModal(b.dataset.jobid, b.dataset.jobname)));
    $$('.btn-cancel').forEach(b => b.addEventListener('click', () => cancelJob(b.dataset.jobid)));
}

async function cancelJob(jobId) {
    if (!confirm('Cancel this job?')) return;
    try {
        await api('/api/jobs/' + encodeURIComponent(jobId), { method: 'DELETE' });
        toast('Job cancelled');
        renderJobs();
    } catch (e) {
        toast('Failed to cancel job: ' + e.message, true);
    }
}

async function runRepo(key, name, btn) {
    if (!key) return;
    btn.disabled = true;
    try {
        const data = await api('/api/jobs', { method: 'POST', body: JSON.stringify({ repository_key: key }) });
        const jobIDs = (data && data.job_ids) || [];
        if (jobIDs.length === 1) {
            toast('Job started (' + jobIDs[0].slice(0, 8) + '…)');
            openLogModal(jobIDs[0], name);
        } else {
            toast('Started ' + jobIDs.length + ' jobs (org expansion)');
        }
        renderRepositories();
    } catch (e) {
        toast('Failed to start job: ' + e.message, true);
        btn.disabled = false;
    }
}

function openLogModal(jobId, jobName) {
    if (state.es) state.es.close();
    state.logJob = jobId;
    state.logIds = {};
    $('#log-modal-title').textContent = 'Logs: ' + jobName;
    $('#log-modal-state').textContent = '';
    const consoleEl = $('#log-console');
    consoleEl.innerHTML = '<div class="log-line muted">Waiting for logs\u2026</div>';
    $('#log-modal').classList.remove('hidden');

    const es = new EventSource('/api/jobs/' + encodeURIComponent(jobId) + '/logs');
    state.es = es;

    es.addEventListener('done', (ev) => {
        let status = '';
        try { status = (JSON.parse(ev.data) || {}).status || ''; } catch (e) { /* ignore */ }
        appendLogLine({ level: 'info', message: 'Job finished (status: ' + status + ')' });
        $('#log-modal-state').textContent = 'finished';
        es.close();
        if (state.es === es) state.es = null;
        if (state.logJob === jobId) renderApp();
    });

    es.onmessage = (ev) => {
        let entry = null;
        try { entry = JSON.parse(ev.data); } catch (e) { return; }
        if (entry && entry.id && state.logIds[entry.id]) return;
        if (entry && entry.id) state.logIds[entry.id] = true;
        appendLogLine(entry);
    };

    es.onerror = () => { /* EventSource auto-reconnects; dedupe + done event handle the rest */ };
}

function appendLogLine(entry) {
    if (!entry) return;
    const consoleEl = $('#log-console');
    const level = ['info', 'error', 'warn', 'debug'].includes(entry.level) ? entry.level : 'info';
    const div = document.createElement('div');
    div.className = 'log-line ' + level;
    const ts = entry.timestamp ? new Date(entry.timestamp).toLocaleTimeString() : '';
    div.textContent = (ts ? '[' + ts + '] ' : '') + (entry.message || '');
    consoleEl.appendChild(div);
    while (consoleEl.childNodes.length > 1000) consoleEl.removeChild(consoleEl.firstChild);
    consoleEl.scrollTop = consoleEl.scrollHeight;
}

async function closeLogModal() {
    if (state.es) { state.es.close(); state.es = null; }
    $('#log-modal').classList.add('hidden');
}

function openRepoForm(repo) {
    $('#repo-modal-title').textContent = repo ? 'Edit Repository' : 'Add Repository';
    $('#repo-original-key').value = repo ? repoKey(repo) : '';
    $('#repo-name').value = repo ? repo.Name : '';
    $('#repo-name').disabled = false; // name 仅展示/查询，可重复可改名
    $('#repo-url').value = repo ? (repo.URL || '') : '';
    $('#repo-type').value = repo && repo.Type ? repo.Type : 'repo';
    $('#repo-org').value = repo ? (repo.OrgName || '') : '';
    $('#repo-cron').value = repo ? (repo.Cron || '') : '';
    $('#repo-depth').value = repo ? (repo.Depth || 0) : 0;
    $('#repo-uses').checked = !!(repo && repo.UseCache);
    $('#repo-allbranches').checked = !!(repo && repo.AllBranches);
    $('#repo-releases').checked = !!(repo && repo.DownloadReleases);
    $('#repo-issues').checked = !!(repo && repo.DownloadIssues);
    $('#repo-wiki').checked = !!(repo && repo.DownloadWiki);
    $('#repo-discussion').checked = !!(repo && repo.DownloadDiscussion);

    const selected = new Set(repo ? (repo.Storage || []) : []);
    const box = $('#repo-storage');
    api('/api/storage').then(storages => {
        const names = (storages || []).map(s => s.Name);
        if (!names.length) {
            box.innerHTML = '<span class="muted">No storage backends configured.</span>';
            return;
        }
        box.innerHTML = names.map(n => `
            <label class="checkbox">
                <input type="checkbox" value="${esc(n)}" ${selected.has(n) ? 'checked' : ''}> ${esc(n)}
            </label>`).join('');
    }).catch(() => { box.innerHTML = '<span class="muted">Failed to load storages.</span>'; });

    $('#repo-modal').classList.remove('hidden');
}

async function saveRepo(ev) {
    ev.preventDefault();
    const originalKey = $('#repo-original-key').value;
    const name = $('#repo-name').value.trim();
    if (!name) { toast('Name is required', true); return; }

    const storage = $$('#repo-storage input:checked').map(i => i.value);
    const repo = {
        Name: name,
        URL: $('#repo-url').value.trim(),
        Type: $('#repo-type').value,
        OrgName: $('#repo-org').value.trim(),
        Cron: $('#repo-cron').value.trim(),
        Storage: storage,
        UseCache: $('#repo-uses').checked,
        AllBranches: $('#repo-allbranches').checked,
        Depth: parseInt($('#repo-depth').value, 10) || 0,
        DownloadReleases: $('#repo-releases').checked,
        DownloadIssues: $('#repo-issues').checked,
        DownloadWiki: $('#repo-wiki').checked,
        DownloadDiscussion: $('#repo-discussion').checked
    };

    try {
        if (originalKey) {
            await api('/api/repositories/' + encodeURIComponent(originalKey), { method: 'PUT', body: JSON.stringify(repo) });
            toast('Repository updated');
        } else {
            await api('/api/repositories', { method: 'POST', body: JSON.stringify(repo) });
            toast('Repository added');
        }
        $('#repo-modal').classList.add('hidden');
        renderRepositories();
    } catch (e) {
        toast('Failed to save repository: ' + e.message, true);
    }
}

async function deleteRepo(key) {
    if (!confirm('Delete repository?')) return;
    try {
        await api('/api/repositories/' + encodeURIComponent(key), { method: 'DELETE' });
        toast('Repository deleted');
        renderRepositories();
    } catch (e) {
        toast('Failed to delete repository: ' + e.message, true);
    }
}

async function renderRepositories() {
    $('#app').innerHTML = '<div class="loading">Loading repositories\u2026</div>';

    const params = new URLSearchParams({ page: state.reposPage, limit: 20 });
    if (state.reposSearch) params.set('search', state.reposSearch);

    let data = null;
    try {
        data = await api('/api/repositories?' + params.toString());
    } catch (e) {
        $('#app').innerHTML = '<div class="empty error-text">Failed to load repositories: ' + esc(e.message) + '</div>';
        return;
    }
    const repos = (data && data.repositories) || [];
    const total = (data && data.total) || 0;
    const pages = Math.max(1, Math.ceil(total / 20));

    const rows = repos.map(r => `
        <tr>
            <td><strong>${esc(r.Name)}</strong></td>
            <td class="muted">${esc(r.URL || '-')}</td>
            <td>${esc(r.Type || 'repo')}</td>
            <td class="muted">${esc(r.Cron || '-')}</td>
            <td class="muted">${fmtTime(r.next_run_time)}</td>
            <td class="muted">${fmtTime(r.last_run_time)}</td>
            <td class="muted">${r.total_runs} total \u00b7 ${r.success_runs} ok \u00b7 ${r.failed_runs} fail</td>
            <td class="muted">${esc((r.Storage || []).join(', ') || '-')}</td>
            <td class="muted">${optionsCell(r)}</td>
            <td class="actions">
                <button class="btn btn-sm btn-primary btn-run-repo" data-key="${esc(repoKey(r))}">Execute</button>
                <button class="btn btn-sm btn-edit-repo" data-key="${esc(repoKey(r))}">Edit</button>
                <button class="btn btn-sm btn-danger btn-del-repo" data-key="${esc(repoKey(r))}">Delete</button>
            </td>
        </tr>`).join('');

    $('#app').innerHTML = `
        <div class="page-header">
            <h2>Repositories</h2>
            <div class="toolbar-group">
                <input type="text" id="repos-search" placeholder="Filter by name or URL\u2026" value="${esc(state.reposSearch)}">
                <button id="btn-search-repos" class="btn">Search</button>
                <button id="btn-add-repo" class="btn btn-primary">Add Repository</button>
                <button id="btn-refresh-repos" class="btn">Refresh</button>
            </div>
        </div>
        <div class="panel">
            ${repos.length
                ? '<div class="table-wrap"><table class="table"><thead><tr><th>Name</th><th>URL</th><th>Type</th><th>Cron</th><th>Next Run</th><th>Last Run</th><th>Stats</th><th>Storage</th><th>Options</th><th></th></tr></thead><tbody>' + rows + '</tbody></table></div>'
                : (state.reposSearch
                    ? '<div class="empty">No repositories match your search.</div>'
                    : '<div class="empty">No repositories configured. Click <strong>Add Repository</strong>.</div>')}
            ${repos.length ? paginationHTML(state.reposPage, pages, total, 'repos') : ''}
        </div>`;

    $('#btn-add-repo').addEventListener('click', () => openRepoForm(null));
    $('#btn-refresh-repos').addEventListener('click', () => renderRepositories());
    // Query only on explicit action (Search button or Enter), never on every
    // keystroke, so the input keeps focus while typing.
    const applyRepoSearch = () => {
        state.reposSearch = $('#repos-search').value.trim();
        state.reposPage = 1;
        renderRepositories();
    };
    $('#btn-search-repos').addEventListener('click', applyRepoSearch);
    $('#repos-search').addEventListener('keydown', (ev) => {
        if (ev.key === 'Enter') {
            ev.preventDefault();
            applyRepoSearch();
        }
    });

    const prev = $('#pg-prev-repos');
    const next = $('#pg-next-repos');
    if (prev) prev.addEventListener('click', () => { if (state.reposPage > 1) { state.reposPage--; renderRepositories(); } });
    if (next) next.addEventListener('click', () => { state.reposPage++; renderRepositories(); });

    $$('.btn-run-repo').forEach(b => b.addEventListener('click', () => {
        const r = repos.find(x => repoKey(x) === b.dataset.key);
        runRepo(b.dataset.key, r ? r.Name : '', b);
    }));
    $$('.btn-edit-repo').forEach(b => b.addEventListener('click', () => {
        const r = repos.find(x => repoKey(x) === b.dataset.key);
        if (r) openRepoForm(r);
    }));
    $$('.btn-del-repo').forEach(b => b.addEventListener('click', () => deleteRepo(b.dataset.key)));
}

function openStorageForm(storage) {
    $('#storage-modal-title').textContent = storage ? 'Edit Storage' : 'Add Storage';
    $('#storage-original-name').value = storage ? storage.Name : '';
    $('#storage-name').value = storage ? storage.Name : '';
    $('#storage-name').disabled = !!storage;
    $('#storage-type').value = storage ? (storage.Type || 'file') : 'file';
    $('#storage-path').value = storage ? (storage.Path || '') : '';
    $('#storage-endpoint').value = storage ? (storage.Endpoint || '') : '';
    $('#storage-bucket').value = storage ? (storage.Bucket || '') : '';
    $('#storage-region').value = storage ? (storage.Region || '') : '';
    $('#storage-akid').value = storage ? (storage.AccessKeyID || '') : '';
    $('#storage-sk').value = storage ? (storage.SecretAccessKey || '') : '';
    toggleStorageType();
    $('#storage-modal').classList.remove('hidden');
}

function toggleStorageType() {
    const isS3 = $('#storage-type').value === 's3';
    $('#storage-path-field').style.display = isS3 ? 'none' : '';
    $$('#storage-form .s3-fields .field').forEach(f => { f.style.display = isS3 ? '' : 'none'; });
}

async function saveStorage(ev) {
    ev.preventDefault();
    const original = $('#storage-original-name').value;
    const name = $('#storage-name').value.trim();
    if (!name) { toast('Name is required', true); return; }
    const type = $('#storage-type').value;

    const storage = {
        Name: name,
        Type: type,
        Path: type === 'file' ? $('#storage-path').value.trim() : '',
        Endpoint: type === 's3' ? $('#storage-endpoint').value.trim() : '',
        Bucket: type === 's3' ? $('#storage-bucket').value.trim() : '',
        Region: type === 's3' ? $('#storage-region').value.trim() : '',
        AccessKeyID: type === 's3' ? $('#storage-akid').value.trim() : '',
        SecretAccessKey: type === 's3' ? $('#storage-sk').value : ''
    };

    try {
        if (original) {
            await api('/api/storage/' + encodeURIComponent(original), { method: 'PUT', body: JSON.stringify(storage) });
            toast('Storage updated');
        } else {
            await api('/api/storage', { method: 'POST', body: JSON.stringify(storage) });
            toast('Storage added');
        }
        $('#storage-modal').classList.add('hidden');
        renderStorage();
    } catch (e) {
        toast('Failed to save storage: ' + e.message, true);
    }
}

async function deleteStorage(name) {
    if (!confirm('Delete storage "' + name + '"?')) return;
    try {
        await api('/api/storage/' + encodeURIComponent(name), { method: 'DELETE' });
        toast('Storage deleted');
        renderStorage();
    } catch (e) {
        toast('Failed to delete storage: ' + e.message, true);
    }
}

async function renderStorage() {
    $('#app').innerHTML = '<div class="loading">Loading storage\u2026</div>';
    let storages = [];
    try {
        storages = (await api('/api/storage')) || [];
    } catch (e) {
        $('#app').innerHTML = '<div class="empty error-text">Failed to load storage: ' + esc(e.message) + '</div>';
        return;
    }

    const rows = storages.map(s => `
        <tr>
            <td><strong>${esc(s.Name)}</strong></td>
            <td>${esc(s.Type)}</td>
            <td class="muted">${esc(s.Path || '-')}</td>
            <td class="muted">
                ${s.Type === 's3' ? esc([s.Endpoint, s.Bucket, s.Region].filter(Boolean).join(' / ') || '-') : '-'}
            </td>
            <td class="actions">
                <button class="btn btn-sm btn-edit-storage" data-name="${esc(s.Name)}">Edit</button>
                <button class="btn btn-sm btn-danger btn-del-storage" data-name="${esc(s.Name)}">Delete</button>
            </td>
        </tr>`).join('');

    $('#app').innerHTML = `
        <div class="page-header">
            <h2>Storage</h2>
            <button id="btn-add-storage" class="btn btn-primary">Add Storage</button>
        </div>
        <div class="panel">
            ${storages.length
                ? '<div class="table-wrap"><table class="table"><thead><tr><th>Name</th><th>Type</th><th>Path</th><th>S3 Target</th><th></th></tr></thead><tbody>' + rows + '</tbody></table></div>'
                : '<div class="empty">No storage backends configured. Click <strong>Add Storage</strong>.</div>'}
        </div>`;

    $('#btn-add-storage').addEventListener('click', () => openStorageForm(null));
    $$('.btn-edit-storage').forEach(b => b.addEventListener('click', () => {
        const s = storages.find(x => x.Name === b.dataset.name);
        if (s) openStorageForm(s);
    }));
    $$('.btn-del-storage').forEach(b => b.addEventListener('click', () => deleteStorage(b.dataset.name)));
}

/* ---------------- Config: export / import / reload ---------------- */

async function renderConfig() {
    $('#app').innerHTML = `
        <div class="page-header"><h2>Configuration</h2></div>
        <div class="panel"><div class="panel-pad">
            <h3>导出配置</h3>
            <textarea id="config-export" class="config-textarea" readonly placeholder="点击“刷新”加载当前配置…"></textarea>
            <div class="toolbar-group" style="margin-top:8px;">
                <button id="btn-config-copy" class="btn btn-sm">复制</button>
                <button id="btn-config-download" class="btn btn-sm">下载 config.yaml</button>
                <button id="btn-config-export-refresh" class="btn btn-sm">刷新</button>
            </div>
        </div></div>
        <div class="panel"><div class="panel-pad">
            <h3>导入配置（合并）</h3>
            <textarea id="config-import" class="config-textarea" placeholder="粘贴 config.yaml 内容，或选择文件…"></textarea>
            <div class="toolbar-group" style="margin-top:8px;">
                <button id="btn-config-file" class="btn btn-sm">选择文件</button>
                <input type="file" id="config-file" accept=".yaml,.yml,.txt" hidden>
                <button id="btn-config-preview" class="btn btn-sm btn-primary">预览差异</button>
            </div>
            <div id="config-preview"></div>
        </div></div>
        <div class="panel"><div class="panel-pad">
            <h3>配置操作</h3>
            <button id="btn-config-reload" class="btn">刷新配置（从磁盘重载 config.yaml）</button>
            <p class="muted" style="margin-top:8px;">server 段（host/port/authEnabled/dbPath）改动需重启 server 生效；daemon 排程需重启 daemon。修改 authEnabled/authToken 后若忘记令牌，可能无法访问本界面。</p>
        </div></div>`;

    loadExport();
    $('#btn-config-copy').addEventListener('click', copyExport);
    $('#btn-config-download').addEventListener('click', downloadExport);
    $('#btn-config-export-refresh').addEventListener('click', loadExport);
    $('#btn-config-file').addEventListener('click', () => $('#config-file').click());
    $('#config-file').addEventListener('change', (ev) => {
        const f = ev.target.files[0];
        if (!f) return;
        const reader = new FileReader();
        reader.onload = () => { $('#config-import').value = reader.result; };
        reader.readAsText(f);
    });
    $('#btn-config-preview').addEventListener('click', previewImport);
    $('#btn-config-reload').addEventListener('click', reloadConfig);
}

async function loadExport() {
    try {
        const data = await api('/api/config/export');
        $('#config-export').value = (data && data.yaml) || '';
    } catch (e) {
        toast('导出配置失败: ' + e.message, true);
    }
}

function copyExport() {
    const text = $('#config-export').value;
    if (!text) return;
    navigator.clipboard.writeText(text).then(
        () => toast('配置已复制'),
        () => toast('复制失败', true));
}

function downloadExport() {
    const text = $('#config-export').value;
    if (!text) return;
    const blob = new Blob([text], { type: 'text/yaml' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'config.yaml';
    a.click();
    URL.revokeObjectURL(a.href);
}

async function previewImport() {
    const config = $('#config-import').value.trim();
    if (!config) { toast('请粘贴或选择配置内容', true); return; }
    try {
        const data = await api('/api/config/import/preview', { method: 'POST', body: JSON.stringify({ config }) });
        renderPreview(data);
    } catch (e) {
        if (e.errors && e.errors.length) toast('导入配置无效：' + e.errors.join('；'), true);
        else toast(e.message, true);
        $('#config-preview').innerHTML = '';
    }
}

function fmt(v) {
    if (v === null || v === undefined) return '';
    if (typeof v === 'boolean') return v ? 'true' : 'false';
    if (Array.isArray(v)) return v.join(', ') || '(空)';
    return String(v);
}

function entryLabel(e) {
    return '<strong>' + esc(e.name) + '</strong>' +
        (e.url ? ' <span class="muted">' + esc(e.url) + '</span>' : '');
}

function changeCell(changes) {
    if (!changes || !changes.length) return '<span class="muted">—</span>';
    return changes.map(c => `<code>${esc(c.field)}</code>: ${esc(fmt(c.existing))} → ${esc(fmt(c.imported))}`).join('<br>');
}

function chip(key, label, n) {
    if (!n) return '';
    return `<button type="button" class="btn btn-sm diff-chip" data-diff="${key}">${label} ${n}</button>`;
}

function renderPreview(data) {
    const p = $('#config-preview');
    const s = data.summary || {};
    const warns = (data.warnings || []).map(w => `<div class="alert warn">${esc(w)}</div>`).join('');
    const r = s.repositories || {}, st = s.storages || {}, g = s.globals || {}, sv = s.server || {};

    p.innerHTML = warns + `
        <div class="diff-summary">
            ${chip('repos-added', '仓库 新增', r.added)}
            ${chip('repos-deleted', '仓库 删除', r.deleted)}
            ${chip('repos-modified', '仓库 修改', r.modified)}
            ${chip('storages-added', '存储 新增', st.added)}
            ${chip('storages-deleted', '存储 删除', st.deleted)}
            ${chip('storages-modified', '存储 修改', st.modified)}
            ${chip('globals', '全局项 变更', g.changed)}
            ${chip('server', 'server 段 变更', sv.changed)}
        </div>
        <div id="diff-repos-added" class="diff-section hidden"></div>
        <div id="diff-repos-deleted" class="diff-section hidden"></div>
        <div id="diff-repos-modified" class="diff-section hidden"></div>
        <div id="diff-storages-added" class="diff-section hidden"></div>
        <div id="diff-storages-deleted" class="diff-section hidden"></div>
        <div id="diff-storages-modified" class="diff-section hidden"></div>
        <div id="diff-globals" class="diff-section hidden"></div>
        <div id="diff-server" class="diff-section hidden"></div>
        <div class="toolbar-group" style="margin-top:12px;">
            <button id="btn-config-apply" class="btn btn-primary">应用导入</button>
        </div>`;

    renderAdded('#diff-repos-added', data.repositories.added);
    renderDeleted('#diff-repos-deleted', data.repositories.deleted, 'repo');
    renderModified('#diff-repos-modified', data.repositories.modified, 'repo');
    renderAdded('#diff-storages-added', data.storages.added);
    renderDeleted('#diff-storages-deleted', data.storages.deleted, 'storage');
    renderModified('#diff-storages-modified', data.storages.modified, 'storage');
    renderFieldChoices('#diff-globals', data.globals, 'global', '全局项');
    renderFieldChoices('#diff-server', data.server, 'server', 'server 段');

    $$('.diff-chip').forEach(ch => ch.addEventListener('click', () => {
        const sec = $('#diff-' + ch.dataset.diff);
        if (sec) sec.classList.toggle('hidden');
    }));
    $('#btn-config-apply').addEventListener('click', applyImport);
}

function renderAdded(sel, entries) {
    const box = $(sel);
    if (!entries || !entries.length) return;
    box.innerHTML = `<div class="diff-header"><strong>新增（${entries.length}，将采用导入）</strong></div>
        <div class="table-wrap"><table class="table"><tbody>
        ${entries.map(e => `<tr><td>${entryLabel(e)}</td></tr>`).join('')}
        </tbody></table></div>`;
    box.classList.remove('hidden');
}

function renderDeleted(sel, entries, kind) {
    const box = $(sel);
    if (!entries || !entries.length) return;
    const rows = entries.map(e => `
        <tr data-key="${esc(e.key || e.name)}">
            <td>${entryLabel(e)}</td>
            <td class="muted">导入配置中不存在</td>
            <td>
                <label class="radio"><input type="radio" name="${kind}-del-${esc(e.key || e.name)}" value="delete"> 删除</label>
                <label class="radio"><input type="radio" name="${kind}-del-${esc(e.key || e.name)}" value="keep" checked> 保留</label>
            </td>
        </tr>`).join('');
    box.innerHTML = `<div class="diff-header"><strong>删除（${entries.length}，默认保留）</strong>
        <button type="button" class="btn btn-sm" data-bulk-del="delete">全部删除</button>
        <button type="button" class="btn btn-sm" data-bulk-del="keep">全部保留</button></div>
        <div class="table-wrap"><table class="table"><thead><tr><th>条目</th><th>说明</th><th>选择</th></tr></thead><tbody>${rows}</tbody></table></div>`;
    box.classList.remove('hidden');
    $$('[data-bulk-del]', box).forEach(b => b.addEventListener('click', () => {
        const val = b.dataset.bulkDel;
        $$('input[type=radio]', box).forEach(r => { r.checked = (r.value === val); });
    }));
}

function renderModified(sel, entries, kind) {
    const box = $(sel);
    if (!entries || !entries.length) return;
    const rows = entries.map(e => `
        <tr data-key="${esc(e.key || e.name)}">
            <td>${entryLabel(e)}</td>
            <td>${changeCell(e.changes)}</td>
            <td>
                <label class="radio"><input type="radio" name="${kind}-${esc(e.key || e.name)}" value="imported" checked> 采用导入</label>
                <label class="radio"><input type="radio" name="${kind}-${esc(e.key || e.name)}" value="existing"> 保留现有</label>
            </td>
        </tr>`).join('');
    box.innerHTML = `<div class="diff-header"><strong>修改（${entries.length}，默认采用导入）</strong>
        <button type="button" class="btn btn-sm" data-bulk="imported">全部采用导入</button>
        <button type="button" class="btn btn-sm" data-bulk="existing">全部保留</button></div>
        <div class="table-wrap"><table class="table"><thead><tr><th>条目</th><th>差异</th><th>选择</th></tr></thead><tbody>${rows}</tbody></table></div>`;
    box.classList.remove('hidden');
    $$('[data-bulk]', box).forEach(b => b.addEventListener('click', () => {
        const val = b.dataset.bulk;
        $$('input[type=radio]', box).forEach(r => { r.checked = (r.value === val); });
    }));
}

function renderFieldChoices(sel, changes, kind, title) {
    const box = $(sel);
    if (!changes || !changes.length) return;
    const rows = changes.map(c => `
        <tr data-field="${esc(c.field)}">
            <td><code>${esc(c.field)}</code></td>
            <td class="muted">${esc(fmt(c.existing))}</td>
            <td class="muted">${esc(fmt(c.imported))}</td>
            <td>
                <label class="radio"><input type="radio" name="${kind}-${esc(c.field)}" value="imported" checked> 采用导入</label>
                <label class="radio"><input type="radio" name="${kind}-${esc(c.field)}" value="existing"> 保留现有</label>
            </td>
        </tr>`).join('');
    box.innerHTML = `<div class="diff-header"><strong>${title}（变更 ${changes.length}，默认采用导入）</strong></div>
        <div class="table-wrap"><table class="table"><thead><tr><th>字段</th><th>现有</th><th>导入</th><th>选择</th></tr></thead><tbody>${rows}</tbody></table></div>`;
    box.classList.remove('hidden');
}

async function applyImport() {
    const config = $('#config-import').value.trim();
    if (!config) return;
    const choices = {
        repository_deletions: [],
        repository_choices: {},
        storage_deletions: [],
        storage_choices: {},
        global_choices: {},
        server_choices: {}
    };
    const collect = (sel, map, deleteArr) => {
        $$(sel + ' tr[data-key]').forEach(tr => {
            const input = $$('input[type=radio]:checked', tr)[0];
            if (!input) return;
            const key = tr.dataset.key;
            if (deleteArr && input.value === 'delete') deleteArr.push(key);
            else if (!deleteArr) map[key] = input.value;
        });
    };
    collect('#diff-repos-modified', choices.repository_choices, null);
    collect('#diff-repos-deleted', null, choices.repository_deletions);
    collect('#diff-storages-modified', choices.storage_choices, null);
    collect('#diff-storages-deleted', null, choices.storage_deletions);
    $$('#diff-globals tr[data-field]').forEach(tr => {
        const input = $$('input[type=radio]:checked', tr)[0];
        if (input) choices.global_choices[tr.dataset.field] = input.value;
    });
    $$('#diff-server tr[data-field]').forEach(tr => {
        const input = $$('input[type=radio]:checked', tr)[0];
        if (input) choices.server_choices[tr.dataset.field] = input.value;
    });
    try {
        const data = await api('/api/config/import', {
            method: 'POST',
            body: JSON.stringify({ config, choices })
        });
        const d = data || {};
        toast('导入完成：仓库 +' + (d.repositories_added || 0) + '/改 ' + (d.repositories_updated || 0) +
              '/删 ' + (d.repositories_deleted || 0) + '；存储 +' + (d.storages_added || 0) +
              '/改 ' + (d.storages_updated || 0) + '/删 ' + (d.storages_deleted || 0));
        renderApp();
    } catch (e) {
        if (e.errors && e.errors.length) toast('导入失败：' + e.errors.join('；'), true);
        else toast('导入失败: ' + e.message, true);
    }
}

async function reloadConfig() {
    try {
        await api('/api/config/reload', { method: 'POST' });
        toast('配置已从磁盘重载');
        renderApp();
    } catch (e) {
        toast('重载失败: ' + e.message, true);
    }
}

document.addEventListener('DOMContentLoaded', () => {
    $('#log-modal-close').addEventListener('click', closeLogModal);
    $('#log-modal').addEventListener('click', (ev) => {
        if (ev.target === ev.currentTarget) closeLogModal();
    });
    $('#repo-form').addEventListener('submit', saveRepo);
    $('#repo-form-cancel').addEventListener('click', () => $('#repo-modal').classList.add('hidden'));
    $('#repo-modal-close').addEventListener('click', () => $('#repo-modal').classList.add('hidden'));
    $('#repo-modal').addEventListener('click', (ev) => {
        if (ev.target === ev.currentTarget) $('#repo-modal').classList.add('hidden');
    });
    $('#storage-form').addEventListener('submit', saveStorage);
    $('#storage-type').addEventListener('change', toggleStorageType);
    $('#storage-form-cancel').addEventListener('click', () => $('#storage-modal').classList.add('hidden'));
    $('#storage-modal-close').addEventListener('click', () => $('#storage-modal').classList.add('hidden'));
    $('#storage-modal').addEventListener('click', (ev) => {
        if (ev.target === ev.currentTarget) $('#storage-modal').classList.add('hidden');
    });

    refreshMetrics();
    setInterval(refreshMetrics, 15000);

    renderApp();
});
