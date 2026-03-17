/**
 * ARC Runner Manager — frontend application
 *
 * All API calls proxy through /api on the frontend server, which forwards
 * requests to the backend and injects the API key server-side. The browser
 * never holds the API key.
 */

'use strict';

// ── state ─────────────────────────────────────────────────────────────────────
let editingName   = null;   // null = create mode, string = edit mode
let pendingDelete = null;   // name awaiting confirmation
let authenticated = false;  // true once auth check passes or login succeeds
let multiUserMode = false;  // true when /auth/me endpoint exists (API_KEY not set)
let serverDefaults = {};    // populated from GET /api/v1/defaults on load

// ── bootstrap modal references (initialised after DOM ready) ──────────────────
let runnerModal, deleteModal, detailCanvas, loginModal;

// ── init ──────────────────────────────────────────────────────────────────────
document.addEventListener('DOMContentLoaded', async () => {
  runnerModal  = new bootstrap.Modal(document.getElementById('runnerModal'));
  deleteModal  = new bootstrap.Modal(document.getElementById('deleteModal'));
  detailCanvas = new bootstrap.Offcanvas(document.getElementById('detailCanvas'));
  loginModal   = new bootstrap.Modal(document.getElementById('loginModal'));

  await checkAuthState();
  await loadDefaults();
  renderUI();
  loadRunners();
});

// ── defaults ──────────────────────────────────────────────────────────────────

async function loadDefaults() {
  try {
    serverDefaults = await apiFetch('/v1/defaults');
  } catch (_) {
    // Non-fatal — form will show empty optional fields.
  }
}

// ── auth ──────────────────────────────────────────────────────────────────────

async function checkAuthState() {
  try {
    const res = await fetch('/auth/me');
    const data = await res.json();
    // multiUser=false → single-key mode: always authenticated, no login UI.
    // multiUser=true  → multi-user mode: users must log in with a token.
    authenticated = data.authenticated === true;
    multiUserMode = data.multiUser === true;
  } catch (_) {
    // If /auth/me is unreachable, show read-only view with no login UI.
    authenticated = false;
    multiUserMode = false;
  }
}

function renderUI() {
  const newBtn = document.getElementById('new-runner-btn');
  if (newBtn) newBtn.classList.toggle('d-none', !authenticated);
  renderAuthControls();
}

async function upgradeAllCharts() {
  showSpinner(true);
  try {
    const result = await apiFetch('/v1/runners/upgrade-chart', { method: 'POST' });
    const up = (result.upgraded || []).join(', ') || 'none';
    const msg = `Upgraded: ${up}.` +
      (result.failed?.length ? ` Failed: ${result.failed.join(', ')}.` : '');
    showToast(result.failed?.length ? 'warning' : 'success', msg);
    await loadRunners();
  } catch (err) {
    showToast('danger', `Chart upgrade failed: ${err.message}`);
  } finally {
    showSpinner(false);
  }
}

function renderAuthControls() {
  const el = document.getElementById('auth-controls');
  if (!el) return;
  if (!multiUserMode) {
    el.innerHTML = '';
    return;
  }
  if (authenticated) {
    el.innerHTML = `
      <button class="btn btn-outline-light btn-sm" onclick="logout()">
        <i class="bi bi-box-arrow-right me-1"></i>Sign Out
      </button>`;
  } else {
    el.innerHTML = `
      <button class="btn btn-outline-light btn-sm"
              data-bs-toggle="modal" data-bs-target="#loginModal">
        <i class="bi bi-key me-1"></i>Sign In
      </button>`;
  }
}

async function submitLogin() {
  const tokenInput = document.getElementById('login-token-input');
  const errorEl    = document.getElementById('login-error');
  const token      = tokenInput.value.trim();
  errorEl.classList.add('d-none');

  if (!token) {
    errorEl.textContent = 'Token is required.';
    errorEl.classList.remove('d-none');
    return;
  }
  try {
    const res = await fetch('/auth/login', {
      method:  'POST',
      headers: { 'Content-Type': 'application/json' },
      body:    JSON.stringify({ token }),
    });
    const data = await res.json();
    if (!res.ok || !data.authenticated) {
      errorEl.textContent = data.error || 'Invalid token.';
      errorEl.classList.remove('d-none');
      return;
    }
    authenticated = true;
    tokenInput.value = '';
    loginModal.hide();
    await loadDefaults();
    renderUI();
    await loadRunners();
  } catch (err) {
    errorEl.textContent = 'Login failed: ' + err.message;
    errorEl.classList.remove('d-none');
  }
}

async function logout() {
  try {
    await fetch('/auth/logout', { method: 'POST' });
  } catch (_) {}
  authenticated = false;
  renderUI();
  await loadRunners();
}

// ── API helpers ───────────────────────────────────────────────────────────────

async function apiFetch(path, options = {}) {
  const res = await fetch('/api' + path, {
    headers: { 'Content-Type': 'application/json', ...options.headers },
    ...options,
  });
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const err = await res.json();
      msg = err.error || msg;
      if (err.details) msg += `: ${err.details}`;
    } catch (_) {}
    throw new Error(msg);
  }
  if (res.status === 204) return null;
  return res.json();
}

// ── list ──────────────────────────────────────────────────────────────────────

async function loadRunners() {
  showSpinner(true);
  try {
    const data = await apiFetch('/v1/runners');
    document.getElementById('alert-area').innerHTML = '';
    renderTable(data.items || []);
    document.getElementById('runner-count').textContent = data.total ?? 0;
  } catch (err) {
    showAlert('danger', `Failed to load runners: ${err.message}`);
    renderTable([]);
  } finally {
    showSpinner(false);
  }
}

function renderTable(runners) {
  const tbody = document.getElementById('runner-tbody');

  const anyOutdated = authenticated && serverDefaults.arcChartVersion && runners.some(r =>
    r.status?.chartVersion && r.status.chartVersion !== serverDefaults.arcChartVersion
  );
  const upgradeBtn = document.getElementById('upgrade-chart-btn');
  if (upgradeBtn) upgradeBtn.classList.toggle('d-none', !anyOutdated);

  if (runners.length === 0) {
    const createLink = authenticated
      ? `<a href="#" onclick="openCreate(); return false">Create the first one.</a>`
      : '';
    tbody.innerHTML = `
      <tr>
        <td colspan="8" class="text-center py-5 text-muted">
          <i class="bi bi-inbox fs-3 d-block mb-2"></i>
          No runner scale sets found. ${createLink}
        </td>
      </tr>`;
    return;
  }

  tbody.innerHTML = runners.map(r => {
    const status  = r.status || {};
    const badge   = statusBadge(status.helmStatus);
    const chartOutdated = serverDefaults.arcChartVersion &&
      status.chartVersion &&
      status.chartVersion !== serverDefaults.arcChartVersion;
    const chartBadge = chartOutdated
      ? `<span class="badge bg-warning-subtle text-warning-emphasis ms-1"
               title="Installed: ${e(status.chartVersion)} → Target: ${e(serverDefaults.arcChartVersion)}">Update available</span>`
      : '';
    const running = status.currentRunners ?? 0;
    const pending = status.pendingRunners  ?? 0;
    const secret  = status.secretExists
      ? '<i class="bi bi-key-fill text-success" title="Secret present"></i>'
      : '<i class="bi bi-key text-warning" title="Secret missing"></i>';

    const actions = authenticated ? `
          <div class="btn-group btn-group-sm">
            <button class="btn btn-outline-secondary" title="Edit"
                    onclick="openEdit('${e(r.name)}')">
              <i class="bi bi-pencil"></i>
            </button>
            <button class="btn btn-outline-danger" title="Delete"
                    onclick="confirmDelete('${e(r.name)}')">
              <i class="bi bi-trash3"></i>
            </button>
          </div>` : '';

    return `
      <tr>
        <td>
          <a href="#" class="fw-semibold text-decoration-none"
             onclick="openDetail('${e(r.name)}'); return false">${e(r.name)}</a>
          <div class="text-muted" style="font-size:0.75rem">${e(status.namespace || 'arc-' + r.name)}</div>
        </td>
        <td class="text-truncate" style="max-width:220px">
          <span title="${e(r.githubConfigUrl)}">${e(r.githubConfigUrl || '—')}</span>
        </td>
        <td><code>${e(r.runnerScaleSetName || r.name)}</code></td>
        <td>${r.minRunners ?? 0} / ${r.maxRunners ?? 10}</td>
        <td class="text-truncate" style="max-width:180px">
          <code style="font-size:0.7rem" title="${e(r.runnerImage || '')}">${e(shortImage(r.runnerImage) || '—')}</code>
        </td>
        <td>${badge}${chartBadge} ${secret}</td>
        <td>
          <span class="badge bg-success-subtle text-success-emphasis">${running} running</span>
          ${pending > 0 ? `<span class="badge bg-warning-subtle text-warning-emphasis">${pending} pending</span>` : ''}
        </td>
        <td class="text-end">${actions}</td>
      </tr>`;
  }).join('');
}

// ── create / edit modal ───────────────────────────────────────────────────────

function openCreate() {
  editingName = null;
  document.getElementById('runnerModalLabel').textContent = 'New Runner Scale Set';
  document.getElementById('cred-note').style.display = 'none';
  document.getElementById('f-name').disabled = false;
  setCredentialsRequired(true);
  clearForm();
  runnerModal.show();
}

async function openEdit(name) {
  editingName = name;
  document.getElementById('runnerModalLabel').textContent = `Edit — ${name}`;
  document.getElementById('cred-note').style.display = '';
  document.getElementById('f-name').disabled = true;
  setCredentialsRequired(false);

  showSpinner(true);
  try {
    const r = await apiFetch(`/v1/runners/${encodeURIComponent(name)}`);
    populateForm(r);
    runnerModal.show();
  } catch (err) {
    showToast('danger', `Failed to load runner: ${err.message}`);
  } finally {
    showSpinner(false);
  }
}

function setCredentialsRequired(required) {
  ['cred-required-marker','cred-required-marker2','cred-required-marker3'].forEach(id => {
    document.getElementById(id).style.display = required ? '' : 'none';
  });
}

function clearForm() {
  ['name','runnerScaleSetName','githubConfigUrl','githubAppId','githubAppInstallationId',
   'githubAppPrivateKey'].forEach(f => {
    const el = document.getElementById('f-' + f);
    if (el) el.value = '';
  });
  const d = serverDefaults;
  document.getElementById('f-minRunners').value   = d.minRunners   ?? 0;
  document.getElementById('f-maxRunners').value   = d.maxRunners   ?? 10;
  document.getElementById('f-runnerImage').value  = d.runnerImage  ?? '';
  document.getElementById('f-cpuRequest').value   = d.cpuRequest   ?? '';
  document.getElementById('f-cpuLimit').value     = d.cpuLimit     ?? '';
  document.getElementById('f-memoryRequest').value = d.memoryRequest ?? '';
  document.getElementById('f-memoryLimit').value  = d.memoryLimit  ?? '';
  document.getElementById('f-storageClass').value = d.storageClass ?? '';
  document.getElementById('f-storageSize').value  = d.storageSize  ?? '';
}

function populateForm(r) {
  clearForm();
  const set = (id, val) => { const el = document.getElementById('f-' + id); if (el && val != null) el.value = val; };
  set('name', r.name);
  set('runnerScaleSetName', r.runnerScaleSetName);
  set('githubConfigUrl', r.githubConfigUrl);
  set('minRunners', r.minRunners);
  set('maxRunners', r.maxRunners);
  set('runnerImage', r.runnerImage);
  set('cpuRequest', r.resources?.cpuRequest);
  set('cpuLimit', r.resources?.cpuLimit);
  set('memoryRequest', r.resources?.memoryRequest);
  set('memoryLimit', r.resources?.memoryLimit);
  set('storageClass', r.storageClass);
  set('storageSize', r.storageSize);
  // Credentials are never returned from the API — fields stay blank.
}

function buildPayload() {
  const val  = id => document.getElementById('f-' + id)?.value.trim() || '';
  const num  = id => parseInt(document.getElementById('f-' + id)?.value, 10) || 0;

  const payload = {
    name:               val('name'),
    githubConfigUrl:    val('githubConfigUrl'),
    runnerScaleSetName: val('runnerScaleSetName') || val('name'),
    minRunners:         num('minRunners'),
    maxRunners:         num('maxRunners'),
  };

  const optStr = (key, v) => { if (v) payload[key] = v; };
  optStr('runnerImage',             val('runnerImage'));
  optStr('githubAppId',             val('githubAppId'));
  optStr('githubAppInstallationId', val('githubAppInstallationId'));
  optStr('githubAppPrivateKey',     val('githubAppPrivateKey'));
  optStr('storageClass',            val('storageClass'));
  optStr('storageSize',             val('storageSize'));

  const cpuReq = val('cpuRequest'), cpuLim = val('cpuLimit');
  const memReq = val('memoryRequest'), memLim = val('memoryLimit');
  if (cpuReq || cpuLim || memReq || memLim) {
    payload.resources = {};
    if (cpuReq) payload.resources.cpuRequest    = cpuReq;
    if (cpuLim) payload.resources.cpuLimit      = cpuLim;
    if (memReq) payload.resources.memoryRequest = memReq;
    if (memLim) payload.resources.memoryLimit   = memLim;
  }

  return payload;
}

async function saveRunner() {
  const payload = buildPayload();

  if (!payload.name || !payload.githubConfigUrl) {
    showToast('warning', 'Name and GitHub Config URL are required.');
    return;
  }
  if (!editingName && (!payload.githubAppId || !payload.githubAppInstallationId || !payload.githubAppPrivateKey)) {
    showToast('warning', 'All three GitHub App credential fields are required when creating a runner.');
    return;
  }

  showSpinner(true);
  try {
    if (editingName) {
      await apiFetch(`/v1/runners/${encodeURIComponent(editingName)}`, {
        method: 'PUT',
        body: JSON.stringify(payload),
      });
      showToast('success', `Runner "${editingName}" updated.`);
    } else {
      await apiFetch('/v1/runners', {
        method: 'POST',
        body: JSON.stringify(payload),
      });
      showToast('success', `Runner "${payload.name}" created.`);
    }
    runnerModal.hide();
    await loadRunners();
  } catch (err) {
    showToast('danger', `Save failed: ${err.message}`);
  } finally {
    showSpinner(false);
  }
}

// ── delete ────────────────────────────────────────────────────────────────────

function confirmDelete(name) {
  pendingDelete = name;
  document.getElementById('delete-name').textContent = name;
  document.getElementById('confirm-delete-btn').onclick = executeDelete;
  deleteModal.show();
}

async function executeDelete() {
  if (!pendingDelete) return;
  const name = pendingDelete;
  deleteModal.hide();
  showSpinner(true);
  try {
    await apiFetch(`/v1/runners/${encodeURIComponent(name)}`, { method: 'DELETE' });
    showToast('success', `Runner "${name}" deleted.`);
    await loadRunners();
  } catch (err) {
    showToast('danger', `Delete failed: ${err.message}`);
  } finally {
    pendingDelete = null;
    showSpinner(false);
  }
}

// ── detail drawer ─────────────────────────────────────────────────────────────

async function openDetail(name) {
  document.getElementById('detailCanvasLabel').textContent = name;
  document.getElementById('detail-body').innerHTML = `
    <div class="d-flex justify-content-center py-5">
      <div class="spinner-border text-primary" role="status"></div>
    </div>`;
  detailCanvas.show();

  try {
    const r = await apiFetch(`/v1/runners/${encodeURIComponent(name)}`);
    document.getElementById('detail-body').innerHTML = renderDetail(r);
  } catch (err) {
    document.getElementById('detail-body').innerHTML = `
      <div class="alert alert-danger">Failed to load detail: ${e(err.message)}</div>`;
  }
}

function renderDetail(r) {
  const s = r.status || {};
  const res = r.resources || {};

  const detailActions = authenticated ? `
      <button class="btn btn-sm btn-outline-primary" onclick="openEdit('${e(r.name)}'); bootstrap.Offcanvas.getInstance(document.getElementById('detailCanvas')).hide()">
        <i class="bi bi-pencil me-1"></i>Edit
      </button>
      <button class="btn btn-sm btn-outline-danger" onclick="confirmDelete('${e(r.name)}'); bootstrap.Offcanvas.getInstance(document.getElementById('detailCanvas')).hide()">
        <i class="bi bi-trash3 me-1"></i>Delete
      </button>` : '';

  return `
    <dl class="row">
      <dt class="col-5">Namespace</dt>       <dd class="col-7"><code>${e(s.namespace || '')}</code></dd>
      <dt class="col-5">Helm Status</dt>     <dd class="col-7">${statusBadge(s.helmStatus)}</dd>
      <dt class="col-5">Chart Version</dt>   <dd class="col-7">${e(s.chartVersion || '—')}</dd>
      <dt class="col-5">Secret</dt>          <dd class="col-7">${s.secretExists ? '<span class="text-success">Present</span>' : '<span class="text-warning">Missing</span>'}</dd>
      <dt class="col-5">Running Pods</dt>    <dd class="col-7">${s.currentRunners ?? 0}</dd>
      <dt class="col-5">Pending Pods</dt>    <dd class="col-7">${s.pendingRunners ?? 0}</dd>
    </dl>
    <hr>
    <dl class="row">
      <dt class="col-5">GitHub URL</dt>      <dd class="col-7 text-break">${e(r.githubConfigUrl || '—')}</dd>
      <dt class="col-5">Scale Set Label</dt> <dd class="col-7"><code>${e(r.runnerScaleSetName || r.name)}</code></dd>
      <dt class="col-5">Min Runners</dt>     <dd class="col-7">${r.minRunners ?? 0}</dd>
      <dt class="col-5">Max Runners</dt>     <dd class="col-7">${r.maxRunners ?? 10}</dd>
    </dl>
    <hr>
    <dl class="row">
      <dt class="col-5">Image</dt>           <dd class="col-7 text-break"><code style="font-size:0.75rem">${e(r.runnerImage || '—')}</code></dd>
      <dt class="col-5">CPU Req/Lim</dt>     <dd class="col-7">${e(res.cpuRequest || '—')} / ${e(res.cpuLimit || '—')}</dd>
      <dt class="col-5">Mem Req/Lim</dt>     <dd class="col-7">${e(res.memoryRequest || '—')} / ${e(res.memoryLimit || '—')}</dd>
      <dt class="col-5">Storage Class</dt>   <dd class="col-7">${e(r.storageClass || '—')}</dd>
      <dt class="col-5">Storage Size</dt>    <dd class="col-7">${e(r.storageSize || '—')}</dd>
    </dl>
    <hr>
    <div class="d-flex gap-2 mt-3">${detailActions}</div>`;
}

// ── UI helpers ────────────────────────────────────────────────────────────────

function statusBadge(status) {
  const map = {
    deployed:        ['success', 'Deployed'],
    failed:          ['danger',  'Failed'],
    'pending-install': ['warning', 'Pending Install'],
    'pending-upgrade': ['warning', 'Pending Upgrade'],
    uninstalling:    ['secondary', 'Uninstalling'],
    superseded:      ['secondary', 'Superseded'],
  };
  const [cls, label] = map[status] || ['secondary', status || 'Unknown'];
  return `<span class="badge bg-${cls}-subtle text-${cls}-emphasis status-badge">${label}</span>`;
}

function shortImage(image) {
  if (!image) return '';
  // Strip registry host for display brevity.
  const parts = image.split('/');
  return parts.length > 2 ? parts.slice(1).join('/') : image;
}

/** HTML-escape a string for safe interpolation into innerHTML. */
function e(str) {
  return String(str ?? '')
    .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
    .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}

function showSpinner(show) {
  document.getElementById('spinner').classList.toggle('active', show);
}

function showAlert(type, message) {
  const area = document.getElementById('alert-area');
  area.innerHTML = `
    <div class="alert alert-${type} alert-dismissible fade show" role="alert">
      ${e(message)}
      <button type="button" class="btn-close" data-bs-dismiss="alert"></button>
    </div>`;
}

function showToast(type, message) {
  const container = document.getElementById('toast-container');
  const id = 'toast-' + Date.now();
  const bgMap = { success: 'bg-success', danger: 'bg-danger', warning: 'bg-warning text-dark' };
  const el = document.createElement('div');
  el.innerHTML = `
    <div id="${id}" class="toast align-items-center text-white ${bgMap[type] || 'bg-secondary'} border-0"
         role="alert" aria-live="assertive" aria-atomic="true">
      <div class="d-flex">
        <div class="toast-body">${e(message)}</div>
        <button type="button" class="btn-close btn-close-white me-2 m-auto" data-bs-dismiss="toast"></button>
      </div>
    </div>`;
  container.appendChild(el.firstElementChild);
  const toast = new bootstrap.Toast(document.getElementById(id), { delay: 5000 });
  toast.show();
  document.getElementById(id).addEventListener('hidden.bs.toast', () => {
    document.getElementById(id)?.remove();
  });
}
