// Simple SPA without frameworks: works via fetch to the backend REST API.
const state = {
  projects: [],
  currentProjectId: null,
  currentTab: 'contract',
  providers: [],
};

const $ = sel => document.querySelector(sel);
const el = (tag, attrs = {}, children = []) => {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'html') e.innerHTML = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else e.setAttribute(k, v);
  }
  for (const c of [].concat(children)) {
    if (c == null) continue;
    e.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
  }
  return e;
};

async function api(path, opts = {}) {
  const resp = await fetch(path, {
    headers: { 'Content-Type': 'application/json', ...(opts.headers || {}) },
    ...opts,
  });
  const text = await resp.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch (_) { data = text; }
  if (!resp.ok) {
    const msg = (data && data.error) ? data.error : `Error ${resp.status}`;
    throw new Error(msg);
  }
  return data;
}

function toast(msg, type = 'success') {
  const t = el('div', { class: `toast ${type}` }, msg);
  document.body.appendChild(t);
  setTimeout(() => t.remove(), 3500);
}

// ---------------- Projects ----------------

async function loadProjects() {
  state.projects = await api('/api/projects');
  renderProjectList();
}

function renderProjectList() {
  const list = $('#projectList');
  list.innerHTML = '';
  for (const p of state.projects) {
    const item = el('div', {
      class: 'project-item' + (p.id === state.currentProjectId ? ' active' : ''),
      onclick: () => selectProject(p.id),
    }, [
      el('div', {}, p.name),
      el('div', { class: 'desc' }, p.description || '—'),
    ]);
    list.appendChild(item);
  }
}

function selectProject(id) {
  state.currentProjectId = id;
  state.currentTab = 'contract';
  renderProjectList();
  renderContent();
}

$('#btnNewProject').addEventListener('click', () => {
  openModal('New Project', [
    field('name', 'Name', 'text', 'my-service'),
    field('description', 'Description', 'text', 'optional'),
  ], async (values) => {
    const p = await api('/api/projects', { method: 'POST', body: JSON.stringify(values) });
    await loadProjects();
    selectProject(p.id);
    closeModal();
  });
});

// ---------------- Modal helpers ----------------

function field(name, label, type = 'text', placeholder = '', extra = {}) {
  return { name, label, type, placeholder, ...extra };
}

function openModal(title, fields, onSubmit, initial = {}) {
  const root = $('#modalRoot');
  const inputs = {};
  const body = fields.map(f => {
    let inputEl;
    if (f.type === 'textarea') {
      inputEl = el('textarea', { rows: f.rows || 6, placeholder: f.placeholder || '' }, initial[f.name] || '');
    } else if (f.type === 'select') {
      inputEl = el('select', {}, (f.options || []).map(o => el('option', { value: o.value, ...(o.value === initial[f.name] ? {selected:'selected'} : {}) }, o.label)));
    } else {
      inputEl = el('input', { type: f.type, placeholder: f.placeholder || '', value: initial[f.name] || '' });
    }
    inputs[f.name] = inputEl;
    return el('div', {}, [el('label', {}, f.label), inputEl]);
  });
  const modal = el('div', { class: 'modal' }, [
    el('h3', {}, title),
    ...body,
    el('div', { class: 'row', style: 'margin-top:16px;justify-content:flex-end;' }, [
      el('button', { class: 'btn btn-secondary', onclick: closeModal }, 'Cancel'),
      el('button', { class: 'btn btn-primary', onclick: async () => {
        const values = {};
        for (const f of fields) values[f.name] = inputs[f.name].value;
        try { await onSubmit(values); } catch (e) { toast(e.message, 'error'); }
      }}, 'Save'),
    ]),
  ]);
  root.innerHTML = '';
  root.appendChild(el('div', { class: 'modal-backdrop', onclick: (e) => { if (e.target.classList.contains('modal-backdrop')) closeModal(); } }, modal));
}

function closeModal() { $('#modalRoot').innerHTML = ''; }

// ---------------- Content / Tabs ----------------

function renderContent() {
  const content = $('#content');
  const project = state.projects.find(p => p.id === state.currentProjectId);
  if (!project) { content.innerHTML = ''; return; }

  const tabs = [
    ['contract', 'Contract'],
    ['mocks', 'Mock Data'],
    ['llm', 'LLM Providers'],
    ['codegen', 'Codegen'],
    ['logs', 'Try It / Logs'],
  ];

  content.innerHTML = '';
  content.appendChild(el('div', { class: 'flex-between' }, [
    el('h1', {}, project.name),
    el('button', { class: 'btn btn-danger btn-sm', onclick: () => deleteProject(project.id) }, 'Delete Project'),
  ]));
  content.appendChild(el('p', { class: 'muted' }, `Mock base URL: `));
  content.lastChild.appendChild(el('code', {}, `${location.origin}/mock/${project.id}/...`));

  const tabBar = el('div', { class: 'tabs' }, tabs.map(([key, label]) =>
    el('div', { class: 'tab' + (state.currentTab === key ? ' active' : ''), onclick: () => { state.currentTab = key; renderContent(); } }, label)
  ));
  content.appendChild(tabBar);

  const panel = el('div', { id: 'tabPanel' });
  content.appendChild(panel);

  switch (state.currentTab) {
    case 'contract': renderContractTab(panel, project); break;
    case 'mocks': renderMocksTab(panel, project); break;
    case 'llm': renderLLMTab(panel, project); break;
    case 'codegen': renderCodegenTab(panel, project); break;
    case 'logs': renderLogsTab(panel, project); break;
  }
}

async function deleteProject(id) {
  if (!confirm('Delete the project and all its data?')) return;
  await api(`/api/projects/${id}`, { method: 'DELETE' });
  state.currentProjectId = null;
  await loadProjects();
  renderContent();
}

// ---------------- Contract tab ----------------

async function renderContractTab(panel, project) {
  panel.innerHTML = 'Loading...';
  let contract = null;
  try { contract = await api(`/api/projects/${project.id}/contract`); } catch (_) {}

  panel.innerHTML = '';
  panel.appendChild(el('div', { class: 'card' }, [
    el('h3', {}, 'Upload / Edit Contract'),
    el('div', { class: 'row' }, [
      el('input', { type: 'file', id: 'contractFile', accept: '.json,.yaml,.yml' }),
      el('button', { class: 'btn btn-secondary', onclick: uploadContractFile }, 'Upload File'),
    ]),
    el('label', {}, 'Or edit manually (YAML/JSON):'),
    el('textarea', { id: 'contractRaw', rows: 16 }, contract ? contract.raw : defaultContractTemplate(project.name)),
    el('div', { class: 'row', style: 'margin-top:10px;' }, [
      el('button', { class: 'btn btn-secondary', onclick: () => validateContract() }, 'Validate'),
      el('button', { class: 'btn btn-primary', onclick: () => saveContract() }, 'Save Version'),
    ]),
    el('div', { id: 'validationResult' }),
  ]));

  panel.appendChild(el('div', { class: 'card' }, [
    el('h3', {}, '✨ Generate Contract from Description (LLM)'),
    el('label', {}, 'LLM Provider'),
    el('select', { id: 'genProvider' }, await providerOptions(project.id)),
    el('label', {}, 'Describe the API in your own words'),
    el('textarea', { id: 'genDescription', rows: 4, placeholder: 'REST API for task management: CRUD for tasks, fields title, done, dueDate...' }),
    el('button', { class: 'btn btn-primary', style: 'margin-top:10px;', onclick: generateContract }, 'Generate'),
  ]));

  if (contract) {
    let liveBadge = null;
    try {
      const endpoints = await api(`/api/projects/${project.id}/endpoints`);
      liveBadge = el('div', { class: 'toast success', style: 'position:static;display:inline-block;margin-bottom:10px;' },
        `🟢 Published: version ${contract.version} — ${endpoints.length} endpoint(s) live right now at /mock/${project.id}/...`);
    } catch (_) {
      liveBadge = el('div', { class: 'toast error', style: 'position:static;display:inline-block;margin-bottom:10px;' }, 'Contract saved, but not parseable — endpoints not published');
    }
    if (liveBadge) panel.insertBefore(liveBadge, panel.firstChild);

    const versions = await api(`/api/projects/${project.id}/contract/versions`);
    const activeVersion = contract.version;
    panel.appendChild(el('div', { class: 'card' }, [
      el('h3', {}, `Version History (${versions.length})`),
      el('p', { class: 'muted' }, 'Each save publishes a new version — it immediately becomes active, and all its endpoints are available at /mock/. You can view an old version, compare it with the current one, or roll back (rollback also creates a new version; history is never rewritten).'),
      el('table', {}, [
        el('tr', {}, [el('th', {}, 'Version'), el('th', {}, 'Source'), el('th', {}, 'Date'), el('th', {}, '')]),
        ...versions.slice().reverse().map(v => el('tr', {}, [
          el('td', {}, [String(v.version), v.version === activeVersion ? el('span', { class: 'badge badge-get', style: 'margin-left:6px;' }, 'live') : null]),
          el('td', {}, v.source),
          el('td', {}, new Date(v.createdAt).toLocaleString()),
          el('td', {}, [
            el('button', { class: 'btn btn-secondary btn-sm', onclick: () => viewContractVersion(project.id, v.version) }, 'View'),
            ' ',
            v.version !== activeVersion ? el('button', { class: 'btn btn-secondary btn-sm', onclick: () => diffContractVersions(project.id, v.version, activeVersion) }, 'Compare with current') : null,
            ' ',
            v.version !== activeVersion ? el('button', { class: 'btn btn-danger btn-sm', onclick: () => rollbackContractVersion(project.id, v.version) }, 'Rollback') : null,
          ]),
        ])),
      ]),
    ]));
  }
}

async function viewContractVersion(projectId, version) {
  try {
    const v = await api(`/api/projects/${projectId}/contract/versions/${version}`);
    const modal = el('div', { class: 'modal', style: 'width:720px;' }, [
      el('h3', {}, `Version ${v.version} (${v.source})`),
      el('textarea', { rows: 22, readonly: 'readonly' }, v.raw),
      el('div', { class: 'row', style: 'margin-top:16px;justify-content:flex-end;' }, [
        el('button', { class: 'btn btn-secondary', onclick: closeModal }, 'Close'),
      ]),
    ]);
    const root = $('#modalRoot');
    root.innerHTML = '';
    root.appendChild(el('div', { class: 'modal-backdrop', onclick: (e) => { if (e.target.classList.contains('modal-backdrop')) closeModal(); } }, modal));
  } catch (e) { toast(e.message, 'error'); }
}

async function diffContractVersions(projectId, fromVersion, toVersion) {
  try {
    const res = await api(`/api/projects/${projectId}/contract/diff?from=${fromVersion}&to=${toVersion}`);
    const lines = res.diff.map(l => {
      const prefix = l.type === 'added' ? '+ ' : l.type === 'removed' ? '- ' : '  ';
      const color = l.type === 'added' ? '#2ecc71' : l.type === 'removed' ? '#e74c3c' : '#9099b3';
      return el('div', { style: `color:${color};white-space:pre-wrap;font-family:ui-monospace,monospace;font-size:12px;` }, prefix + l.text);
    });
    const modal = el('div', { class: 'modal', style: 'width:720px;' }, [
      el('h3', {}, `Diff: version ${res.fromVersion} → version ${res.toVersion}`),
      el('div', { style: 'max-height:60vh;overflow-y:auto;background:var(--panel2);border-radius:8px;padding:10px;' }, lines),
      el('div', { class: 'row', style: 'margin-top:16px;justify-content:flex-end;' }, [
        el('button', { class: 'btn btn-secondary', onclick: closeModal }, 'Close'),
      ]),
    ]);
    const root = $('#modalRoot');
    root.innerHTML = '';
    root.appendChild(el('div', { class: 'modal-backdrop', onclick: (e) => { if (e.target.classList.contains('modal-backdrop')) closeModal(); } }, modal));
  } catch (e) { toast(e.message, 'error'); }
}

async function rollbackContractVersion(projectId, version) {
  if (!confirm(`Roll back the contract to version ${version}? This will re-publish it as a new active version — all its endpoints will go live immediately.`)) return;
  try {
    await api(`/api/projects/${projectId}/contract/versions/${version}/rollback`, { method: 'POST' });
    toast(`Version ${version} re-published and live`);
    renderContent();
  } catch (e) { toast(e.message, 'error'); }
}

function defaultContractTemplate(name) {
  return `openapi: "3.0.3"
info:
  title: ${name}
  version: "1.0.0"
paths:
  /health:
    get:
      operationId: getHealth
      summary: Service health check
      responses:
        "200":
          description: OK
          content:
            application/json:
              schema:
                type: object
                properties:
                  status: { type: string, example: ok }
`;
}

async function uploadContractFile() {
  const fileInput = $('#contractFile');
  if (!fileInput.files.length) { toast('Select a file', 'error'); return; }
  const text = await fileInput.files[0].text();
  $('#contractRaw').value = text;
  toast('File loaded into editor, click "Save Version"');
}

async function validateContract() {
  try {
    const raw = $('#contractRaw').value;
    const result = await api(`/api/projects/${state.currentProjectId}/contract/validate`, { method: 'POST', body: JSON.stringify({ raw }) });
    const box = $('#validationResult');
    box.innerHTML = '';
    if (result.valid) {
      box.appendChild(el('div', { class: 'toast success', style: 'position:static;display:inline-block;' }, `Valid: ${result.pathCount} paths, ${result.operationCount} operations`));
    } else {
      box.appendChild(el('pre', {}, result.errors.join('\n')));
    }
  } catch (e) { toast(e.message, 'error'); }
}

async function saveContract() {
  try {
    const raw = $('#contractRaw').value;
    await api(`/api/projects/${state.currentProjectId}/contract`, { method: 'POST', body: JSON.stringify({ raw, source: 'manual' }) });
    toast('Contract saved');
    renderContent();
  } catch (e) { toast(e.message, 'error'); }
}

async function generateContract() {
  const providerId = $('#genProvider').value;
  const description = $('#genDescription').value;
  if (!providerId) { toast('First configure an LLM provider in the "LLM Providers" tab', 'error'); return; }
  try {
    toast('Generating... this may take up to a minute');
    const result = await api(`/api/projects/${state.currentProjectId}/contract/generate`, {
      method: 'POST', body: JSON.stringify({ providerId, description }),
    });
    $('#contractRaw').value = result.raw;
    toast('Contract generated — review and save');
  } catch (e) { toast(e.message, 'error'); }
}

// ---------------- Mocks tab ----------------

async function renderMocksTab(panel, project) {
  panel.innerHTML = 'Loading...';
  let endpoints = [];
  try { endpoints = await api(`/api/projects/${project.id}/endpoints`); } catch (e) { toast(e.message, 'error'); }
  const mocks = await api(`/api/projects/${project.id}/mocks`);

  panel.innerHTML = '';
  if (!endpoints.length) {
    panel.appendChild(el('p', { class: 'muted' }, 'First, load a contract in the "Contract" tab.'));
    return;
  }

  for (const ep of endpoints) {
    const epMocks = mocks.filter(m => m.path === ep.path && m.method === ep.method);
    const card = el('div', { class: 'card' });
    card.appendChild(el('div', { class: 'flex-between' }, [
      el('div', {}, [
        el('span', { class: `badge badge-${ep.method.toLowerCase()}` }, ep.method),
        ' ',
        el('code', {}, ep.path),
      ]),
      el('button', { class: 'btn btn-secondary btn-sm', onclick: () => openMockEditor(project.id, ep, null) }, '+ Add Mock'),
    ]));
    if (ep.summary) card.appendChild(el('p', { class: 'muted' }, ep.summary));

    if (epMocks.length) {
      card.appendChild(el('table', {}, [
        el('tr', {}, [el('th', {}, 'Scenario'), el('th', {}, 'Status'), el('th', {}, 'Source'), el('th', {}, '')]),
        ...epMocks.map(m => el('tr', {}, [
          el('td', {}, m.scenario || 'default'),
          el('td', {}, String(m.statusCode)),
          el('td', {}, m.isLlmGenerated ? '🤖 LLM' : '✍️ manually'),
          el('td', {}, [
            el('button', { class: 'btn btn-secondary btn-sm', onclick: () => openMockEditor(project.id, ep, m) }, 'Edit'),
            ' ',
            el('button', { class: 'btn btn-danger btn-sm', onclick: async () => { await api(`/api/mocks/${m.id}`, { method: 'DELETE' }); renderContent(); } }, 'Delete'),
          ]),
        ])),
      ]));
    } else {
      card.appendChild(el('p', { class: 'muted' }, 'No mock configured — a schema example from the contract will be used.'));
    }
    panel.appendChild(card);
  }
}

async function openMockEditor(projectId, endpoint, mock) {
  const providers = await providerOptions(projectId);
  const isEdit = !!mock;
  const modal = el('div', { class: 'modal' });
  const bodyText = mock ? mock.body : '';
  const scenario = el('input', { value: mock ? mock.scenario : 'default' });
  const status = el('input', { type: 'number', value: mock ? mock.statusCode : 200 });
  const delay = el('input', { type: 'number', value: mock ? mock.delayMs : 0 });
  const failRate = el('input', { type: 'number', value: mock ? mock.failRatePct : 0 });
  const body = el('textarea', { rows: 10 }, bodyText);
  const providerSelect = el('select', {}, providers);
  const hints = el('input', { placeholder: 'e.g. Russian names, realistic email' });

  modal.appendChild(el('h3', {}, `${isEdit ? 'Edit' : 'New'} Mock: ${endpoint.method} ${endpoint.path}`));
  modal.appendChild(el('label', {}, 'Scenario (X-Mock-Scenario)'));
  modal.appendChild(scenario);
  modal.appendChild(el('div', { class: 'row' }, [
    el('div', {}, [el('label', {}, 'HTTP Status'), status]),
    el('div', {}, [el('label', {}, 'Delay, ms'), delay]),
    el('div', {}, [el('label', {}, '% Random Errors'), failRate]),
  ]));
  modal.appendChild(el('label', {}, 'Response Body (JSON)'));
  modal.appendChild(body);
  modal.appendChild(el('div', { class: 'card', style: 'margin-top:10px;' }, [
    el('label', {}, '✨ Generate Body via LLM'),
    el('div', { class: 'row' }, [providerSelect, hints]),
    el('button', { class: 'btn btn-secondary btn-sm', style: 'margin-top:8px;', onclick: async () => {
      try {
        const res = await api(`/api/projects/${projectId}/mocks/generate`, {
          method: 'POST',
          body: JSON.stringify({ path: endpoint.path, method: endpoint.method, statusCode: Number(status.value) || 200, providerId: providerSelect.value, hints: hints.value }),
        });
        body.value = res.body;
        toast('Generated — review and save');
      } catch (e) { toast(e.message, 'error'); }
    }}, 'Generate Data'),
  ]));
  modal.appendChild(el('div', { class: 'row', style: 'margin-top:16px;justify-content:flex-end;' }, [
    el('button', { class: 'btn btn-secondary', onclick: closeModal }, 'Cancel'),
    el('button', { class: 'btn btn-primary', onclick: async () => {
      try {
        const payload = {
          projectId, path: endpoint.path, method: endpoint.method,
          scenario: scenario.value || 'default',
          statusCode: Number(status.value) || 200,
          contentType: 'application/json',
          body: body.value,
          delayMs: Number(delay.value) || 0,
          failRatePct: Number(failRate.value) || 0,
        };
        if (isEdit) {
          payload.id = mock.id;
          await api(`/api/mocks/${mock.id}`, { method: 'PUT', body: JSON.stringify(payload) });
        } else {
          await api(`/api/projects/${projectId}/mocks`, { method: 'POST', body: JSON.stringify(payload) });
        }
        closeModal();
        renderContent();
      } catch (e) { toast(e.message, 'error'); }
    }}, 'Save'),
  ]));

  const root = $('#modalRoot');
  root.innerHTML = '';
  root.appendChild(el('div', { class: 'modal-backdrop', onclick: (e) => { if (e.target.classList.contains('modal-backdrop')) closeModal(); } }, modal));
}

// ---------------- LLM tab ----------------

async function providerOptions(projectId) {
  const providers = await api(`/api/projects/${projectId}/llm-providers`);
  state.providers = providers;
  const opts = providers.map(p => el('option', { value: p.id }, `${p.name} (${p.type})`));
  return opts.length ? opts : [el('option', { value: '' }, 'no providers — add one in the LLM tab')];
}

async function renderLLMTab(panel, project) {
  panel.innerHTML = 'Loading...';
  const providers = await api(`/api/projects/${project.id}/llm-providers`);
  panel.innerHTML = '';

  panel.appendChild(el('div', { class: 'flex-between' }, [
    el('h3', {}, 'Connected LLM Providers'),
    el('button', { class: 'btn btn-primary btn-sm', onclick: () => openProviderEditor(project.id) }, '+ Add Provider'),
  ]));

  for (const p of providers) {
    panel.appendChild(el('div', { class: 'card flex-between' }, [
      el('div', {}, [
        el('div', {}, [el('b', {}, p.name), ' ', el('span', { class: 'muted' }, `(${p.type}${p.model ? ', ' + p.model : ''})`)]),
        el('div', { class: 'muted' }, p.baseUrl || 'default address'),
      ]),
      el('div', {}, [
        el('button', { class: 'btn btn-secondary btn-sm', onclick: () => testProvider(p.id) }, 'Test'),
        ' ',
        el('button', { class: 'btn btn-danger btn-sm', onclick: async () => { await api(`/api/llm-providers/${p.id}`, { method: 'DELETE' }); renderContent(); } }, 'Delete'),
      ]),
    ]));
  }

  panel.appendChild(el('div', { class: 'card' }, [
    el('h3', {}, 'Supported Types'),
    el('ul', {}, [
      el('li', {}, 'openai — OpenAI (gpt-4o, gpt-4.1, ...)'),
      el('li', {}, 'azure_openai — Azure OpenAI (specify BaseURL of deployment)'),
      el('li', {}, 'anthropic — Claude'),
      el('li', {}, 'google — Gemini'),
      el('li', {}, 'ollama — local model via Ollama (BaseURL e.g. http://localhost:11434/v1 or http://host.docker.internal:11434/v1 in Docker/K8s)'),
      el('li', {}, 'vllm / lmstudio / groq / openrouter / together / deepseek — any OpenAI-compatible endpoint'),
      el('li', {}, 'custom — arbitrary HTTP LLM API (request template + response extraction path)'),
    ]),
  ]));
}

async function testProvider(id) {
  try {
    await api(`/api/llm-providers/${id}/test`, { method: 'POST' });
    toast('Connection works');
  } catch (e) { toast(e.message, 'error'); }
}

function openProviderEditor(projectId) {
  const modal = el('div', { class: 'modal' });
  const name = el('input', { placeholder: 'My OpenAI' });
  const type = el('select', {}, [
    'openai', 'azure_openai', 'anthropic', 'google', 'ollama', 'vllm', 'lmstudio', 'groq', 'openrouter', 'together', 'deepseek', 'custom',
  ].map(t => el('option', { value: t }, t)));
  const baseUrl = el('input', { placeholder: 'https://api.openai.com/v1 (empty = provider default)' });
  const apiKey = el('input', { type: 'password', placeholder: 'API key (if needed)' });
  const model = el('input', { placeholder: 'gpt-4o-mini / claude-sonnet-4-6 / llama3.1 ...' });
  const isGlobal = el('input', { type: 'checkbox', checked: 'checked', style: 'width:auto;' });
  const customBody = el('textarea', { rows: 3, placeholder: '{"model":"{{MODEL}}","input":{{USER_JSON}}} — for type=custom only' });
  const customPath = el('input', { placeholder: 'e.g. choices.0.message.content — for type=custom only' });

  modal.appendChild(el('h3', {}, 'New LLM Provider'));
  modal.appendChild(el('label', {}, 'Name')); modal.appendChild(name);
  modal.appendChild(el('label', {}, 'Type')); modal.appendChild(type);
  modal.appendChild(el('label', {}, 'Base URL')); modal.appendChild(baseUrl);
  modal.appendChild(el('label', {}, 'API Key')); modal.appendChild(apiKey);
  modal.appendChild(el('label', {}, 'Model')); modal.appendChild(model);
  modal.appendChild(el('label', {}, 'Body Template (custom only)')); modal.appendChild(customBody);
  modal.appendChild(el('label', {}, 'Response Extraction Path (custom only)')); modal.appendChild(customPath);
  modal.appendChild(el('div', { class: 'row', style: 'margin-top:8px;align-items:center;' }, [isGlobal, el('label', { style: 'margin:0;' }, 'Available to all projects')]));

  modal.appendChild(el('div', { class: 'row', style: 'margin-top:16px;justify-content:flex-end;' }, [
    el('button', { class: 'btn btn-secondary', onclick: closeModal }, 'Cancel'),
    el('button', { class: 'btn btn-primary', onclick: async () => {
      try {
        await api('/api/llm-providers', {
          method: 'POST',
          body: JSON.stringify({
            name: name.value, type: type.value, baseUrl: baseUrl.value, apiKey: apiKey.value, model: model.value,
            customBody: customBody.value, customPath: customPath.value,
            projectId: isGlobal.checked ? '' : projectId,
          }),
        });
        closeModal();
        renderContent();
      } catch (e) { toast(e.message, 'error'); }
    }}, 'Save'),
  ]));

  const root = $('#modalRoot');
  root.innerHTML = '';
  root.appendChild(el('div', { class: 'modal-backdrop', onclick: (e) => { if (e.target.classList.contains('modal-backdrop')) closeModal(); } }, modal));
}

// ---------------- Codegen tab ----------------

const CODEGEN_LANGUAGES = [
  { id: 'go', label: 'Go' },
  { id: 'typescript', label: 'TypeScript' },
  { id: 'python', label: 'Python' },
  { id: 'java', label: 'Java' },
  { id: 'rust', label: 'Rust' },
  { id: 'csharp', label: 'C#' },
];

function renderCodegenTab(panel, project) {
  panel.innerHTML = '';

  // Multi-language agent generation.
  const langChecks = CODEGEN_LANGUAGES.map(l => {
    const cb = el('input', { type: 'checkbox', value: l.id, checked: 'checked', style: 'width:auto;' });
    return el('label', { style: 'margin-right:12px;' }, [cb, ' ' + l.label]);
  });

  panel.appendChild(el('div', { class: 'card' }, [
    el('h3', {}, '🛠 Multi-Language Code Generation'),
    el('p', { class: 'muted' }, 'Generate server/client code for multiple languages from the active contract and download a zip archive.'),
    el('div', { class: 'row', style: 'flex-wrap:wrap;' }, langChecks),
    el('div', { class: 'row', style: 'margin-top:8px;' }, [
      el('button', { class: 'btn btn-primary', onclick: async () => {
        const languages = CODEGEN_LANGUAGES.filter(l => {
          const cb = langChecks.find(c => c.querySelector('input').value === l.id);
          return cb.querySelector('input').checked;
        }).map(l => l.id);
        if (languages.length === 0) { toast('Select at least one language', 'error'); return; }
        try {
          const resp = await fetch(`/api/projects/${project.id}/codegen/agent`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ languages, project_name: project.name }),
          });
          if (!resp.ok) {
            const text = await resp.text();
            let msg = `Error ${resp.status}`;
            try { msg = JSON.parse(text).error || msg; } catch (_) {}
            throw new Error(msg);
          }
          const blob = await resp.blob();
          const url = URL.createObjectURL(blob);
          const a = el('a', { href: url, download: 'codegen.zip' });
          document.body.appendChild(a);
          a.click();
          a.remove();
          URL.revokeObjectURL(url);
          toast('codegen.zip downloaded');
        } catch (e) { toast(e.message, 'error'); }
      } }, 'Generate & Download ZIP'),
    ]),
  ]));

  // Legacy Go server/client downloads.
  panel.appendChild(el('div', { class: 'card' }, [
    el('h3', {}, '🛠 Generate Go Server'),
    el('p', { class: 'muted' }, 'HTTP server skeleton (net/http) with an interface for each contract operation and a main.go stub.'),
    el('button', { class: 'btn btn-primary', onclick: () => downloadCodegen(project.id, 'server') }, 'Download server.zip'),
  ]));
  panel.appendChild(el('div', { class: 'card' }, [
    el('h3', {}, '🛠 Generate Go Client'),
    el('p', { class: 'muted' }, 'Type-safe client with a method for each contract operation.'),
    el('button', { class: 'btn btn-primary', onclick: () => downloadCodegen(project.id, 'client') }, 'Download client.zip'),
  ]));
}

function downloadCodegen(projectId, kind) {
  window.open(`/api/projects/${projectId}/codegen/${kind}?lang=go`, '_blank');
}

// ---------------- Logs / Try it tab ----------------

async function renderLogsTab(panel, project) {
  panel.innerHTML = '';
  panel.appendChild(el('div', { class: 'card' }, [
    el('h3', {}, 'Try the Mock'),
    el('p', { class: 'muted' }, 'Send a request directly from the browser. The X-Mock-Scenario header selects the response scenario.'),
    tryItForm(project.id),
  ]));

  const logs = await api(`/api/projects/${project.id}/logs`);
  panel.appendChild(el('div', { class: 'card' }, [
    el('div', { class: 'flex-between' }, [el('h3', {}, 'Recent Requests'), el('button', { class: 'btn btn-secondary btn-sm', onclick: () => renderContent() }, 'Refresh')]),
    el('table', {}, [
      el('tr', {}, [el('th', {}, 'Time'), el('th', {}, 'Method'), el('th', {}, 'Path'), el('th', {}, 'Status'), el('th', {}, 'ms')]),
      ...logs.map(l => el('tr', {}, [
        el('td', {}, new Date(l.timestamp).toLocaleTimeString()),
        el('td', {}, l.method),
        el('td', {}, l.path),
        el('td', {}, String(l.statusCode)),
        el('td', {}, String(l.durationMs)),
      ])),
    ]),
  ]));
}

function tryItForm(projectId) {
  const method = el('select', {}, ['GET', 'POST', 'PUT', 'PATCH', 'DELETE'].map(m => el('option', { value: m }, m)));
  const path = el('input', { placeholder: '/users/1', value: '/' });
  const scenario = el('input', { placeholder: 'scenario (optional)' });
  const out = el('pre', {}, 'Response will appear here');
  const wrap = el('div', {}, [
    el('div', { class: 'row' }, [method, path, scenario, el('button', { class: 'btn btn-secondary', onclick: async () => {
      const url = `/mock/${projectId}${path.value}`;
      const headers = {};
      if (scenario.value) headers['X-Mock-Scenario'] = scenario.value;
      const started = performance.now();
      const resp = await fetch(url, { method: method.value, headers });
      const text = await resp.text();
      const ms = Math.round(performance.now() - started);
      out.textContent = `HTTP ${resp.status} (${ms} ms)\n\n${text}`;
    }}, 'Send') ]),
    out,
  ]);
  return wrap;
}

// ---------------- init ----------------

loadProjects();
