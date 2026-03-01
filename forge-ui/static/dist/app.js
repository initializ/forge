// Forge Dashboard — Preact + HTM (no build step)
// Vendor imports via ESM CDN (pinned versions for reproducibility)
import { h, render } from 'https://esm.sh/preact@10.25.4';
import { useState, useEffect, useCallback, useRef, useMemo } from 'https://esm.sh/preact@10.25.4/hooks';
import htm from 'https://esm.sh/htm@3.1.1';

const html = htm.bind(h);

// ── API helpers ──────────────────────────────────────────────

async function fetchAgents() {
  const res = await fetch('/api/agents');
  if (!res.ok) throw new Error(`Failed to fetch agents: ${res.status}`);
  return res.json();
}

async function startAgent(id, passphrase) {
  const opts = { method: 'POST' };
  if (passphrase) {
    opts.headers = { 'Content-Type': 'application/json' };
    opts.body = JSON.stringify({ passphrase });
  }
  const res = await fetch(`/api/agents/${id}/start`, opts);
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `Failed to start agent: ${res.status}`);
  }
  return res.json();
}

// Cached passphrase (in-memory only, cleared on page reload)
let cachedPassphrase = null;

async function stopAgent(id) {
  const res = await fetch(`/api/agents/${id}/stop`, { method: 'POST' });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `Failed to stop agent: ${res.status}`);
  }
  return res.json();
}

async function rescanAgents() {
  const res = await fetch('/api/agents/rescan', { method: 'POST' });
  if (!res.ok) throw new Error(`Rescan failed: ${res.status}`);
  return res.json();
}

async function fetchSessions(agentId) {
  const res = await fetch(`/api/agents/${agentId}/sessions`);
  if (!res.ok) throw new Error(`Failed to fetch sessions: ${res.status}`);
  return res.json();
}

async function fetchSession(agentId, sessionId) {
  const res = await fetch(`/api/agents/${agentId}/sessions/${sessionId}`);
  if (!res.ok) throw new Error(`Failed to fetch session: ${res.status}`);
  return res.json();
}

// ── Phase 3 API Helpers ──────────────────────────────────────

async function fetchWizardMeta() {
  const res = await fetch('/api/wizard/meta');
  if (!res.ok) throw new Error(`Failed to fetch wizard metadata: ${res.status}`);
  return res.json();
}

async function createAgent(opts) {
  const res = await fetch('/api/agents', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(opts),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `Failed to create agent: ${res.status}`);
  }
  return res.json();
}

async function startOAuth(provider) {
  const res = await fetch('/api/oauth/start', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ provider }),
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `OAuth failed: ${res.status}`);
  }
  return res.json();
}

async function fetchConfig(agentId) {
  const res = await fetch(`/api/agents/${agentId}/config`);
  if (!res.ok) throw new Error(`Failed to fetch config: ${res.status}`);
  return res.text();
}

async function saveConfig(agentId, content) {
  const res = await fetch(`/api/agents/${agentId}/config`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
  });
  const data = await res.json();
  if (!res.ok && !data.errors) throw new Error(`Failed to save config: ${res.status}`);
  return data;
}

async function validateConfig(agentId, content) {
  const res = await fetch(`/api/agents/${agentId}/config/validate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
  });
  if (!res.ok) throw new Error(`Failed to validate config: ${res.status}`);
  return res.json();
}

async function fetchSkills(category) {
  const url = category ? `/api/skills?category=${encodeURIComponent(category)}` : '/api/skills';
  const res = await fetch(url);
  if (!res.ok) throw new Error(`Failed to fetch skills: ${res.status}`);
  return res.json();
}

async function fetchSkillContent(name) {
  const res = await fetch(`/api/skills/${encodeURIComponent(name)}/content`);
  if (!res.ok) throw new Error(`Failed to fetch skill content: ${res.status}`);
  return res.text();
}

// ── SSE Hook ─────────────────────────────────────────────────

function useSSE(onEvent) {
  const callbackRef = useRef(onEvent);
  callbackRef.current = onEvent;

  useEffect(() => {
    const es = new EventSource('/api/events');

    es.addEventListener('agent_status', (e) => {
      try {
        const data = JSON.parse(e.data);
        callbackRef.current(data);
      } catch { /* ignore parse errors */ }
    });

    es.onerror = () => {
      // EventSource auto-reconnects
    };

    return () => es.close();
  }, []);
}

// ── Hash Router ──────────────────────────────────────────────

function useHashRoute() {
  const [route, setRoute] = useState(() => parseHash(location.hash));

  useEffect(() => {
    const handler = () => setRoute(parseHash(location.hash));
    window.addEventListener('hashchange', handler);
    return () => window.removeEventListener('hashchange', handler);
  }, []);

  return route;
}

function parseHash(hash) {
  const path = hash.replace(/^#\/?/, '') || '';
  // #/agent/{id}
  const agentMatch = path.match(/^agent\/(.+)$/);
  if (agentMatch) {
    return { page: 'chat', params: { id: agentMatch[1] } };
  }
  // #/create
  if (path === 'create') return { page: 'create', params: {} };
  // #/config/{id}
  const configMatch = path.match(/^config\/(.+)$/);
  if (configMatch) return { page: 'config', params: { id: configMatch[1] } };
  // #/skills
  if (path === 'skills') return { page: 'skills', params: {} };
  return { page: 'dashboard', params: {} };
}

function navigate(path) {
  location.hash = '#/' + path;
}

// ── Markdown Renderer ────────────────────────────────────────

function renderMarkdown(text) {
  if (!text) return '';

  // Extract code blocks first so they aren't processed by other rules.
  const codeBlocks = [];
  let src = text.replace(/```(\w*)\n([\s\S]*?)```/g, (_, lang, code) => {
    const idx = codeBlocks.length;
    const escaped = code.trim().replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    codeBlocks.push(`<pre class="md-code-block"><code>${escaped}</code></pre>`);
    return `\x00CB${idx}\x00`;
  });

  // Escape HTML in remaining text
  src = src.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

  // Process line-by-line for block-level elements
  const lines = src.split('\n');
  const out = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    // Code block placeholder
    const cbMatch = line.match(/^\x00CB(\d+)\x00$/);
    if (cbMatch) {
      out.push(codeBlocks[parseInt(cbMatch[1])]);
      i++;
      continue;
    }

    // Headings
    const headingMatch = line.match(/^(#{1,6})\s+(.+)$/);
    if (headingMatch) {
      const level = headingMatch[1].length;
      out.push(`<h${level} class="md-heading">${inlineFormat(headingMatch[2])}</h${level}>`);
      i++;
      continue;
    }

    // Horizontal rule
    if (/^(---|\*\*\*|___)/.test(line.trim())) {
      out.push('<hr class="md-hr"/>');
      i++;
      continue;
    }

    // Unordered list block
    if (/^[\s]*[-*]\s+/.test(line)) {
      const items = [];
      while (i < lines.length && /^[\s]*[-*]\s+/.test(lines[i])) {
        const content = lines[i].replace(/^[\s]*[-*]\s+/, '');
        // Collect indented continuation lines
        let full = content;
        while (i + 1 < lines.length && /^\s{2,}/.test(lines[i + 1]) && !/^[\s]*[-*]\s+/.test(lines[i + 1]) && !/^\d+\.\s+/.test(lines[i + 1])) {
          i++;
          full += '<br/>' + lines[i].trim();
        }
        items.push(`<li>${inlineFormat(full)}</li>`);
        i++;
      }
      out.push(`<ul>${items.join('')}</ul>`);
      continue;
    }

    // Ordered list block
    if (/^[\s]*\d+\.\s+/.test(line)) {
      const items = [];
      while (i < lines.length && /^[\s]*\d+\.\s+/.test(lines[i])) {
        const content = lines[i].replace(/^[\s]*\d+\.\s+/, '');
        let full = content;
        while (i + 1 < lines.length && /^\s{2,}/.test(lines[i + 1]) && !/^[\s]*[-*]\s+/.test(lines[i + 1]) && !/^\d+\.\s+/.test(lines[i + 1])) {
          i++;
          full += '<br/>' + lines[i].trim();
        }
        items.push(`<li>${inlineFormat(full)}</li>`);
        i++;
      }
      out.push(`<ol>${items.join('')}</ol>`);
      continue;
    }

    // Empty line — paragraph break
    if (line.trim() === '') {
      out.push('');
      i++;
      continue;
    }

    // Indented block (3+ spaces, not a list) — preserve as-is
    if (/^\s{3,}/.test(line) && out.length > 0) {
      out.push(`<div class="md-indent">${inlineFormat(line.trim())}</div>`);
      i++;
      continue;
    }

    // Regular paragraph line
    out.push(`<p>${inlineFormat(line)}</p>`);
    i++;
  }

  // Collapse consecutive <p> tags separated only by empty entries
  return out.filter((l, idx) => !(l === '' && idx > 0 && out[idx - 1] === '')).join('\n');
}

// Apply inline formatting (bold, italic, code, links)
function inlineFormat(text) {
  let s = text;
  // Inline code (before bold/italic to avoid conflicts)
  s = s.replace(/`([^`]+)`/g, '<code class="md-inline-code">$1</code>');
  // Bold
  s = s.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
  // Italic (single *)
  s = s.replace(/(?<!\*)\*(?!\*)(.+?)(?<!\*)\*(?!\*)/g, '<em>$1</em>');
  // Links
  s = s.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
  return s;
}

// ── Helpers ──────────────────────────────────────────────────

// formatToolContent extracts readable text from tool result content.
// Tool results are often JSON like {"stdout":"...","stderr":"...","exit_code":0}.
function formatToolContent(content) {
  if (!content) return '';
  try {
    const obj = JSON.parse(content);
    // Shell/exec tool output format.
    if (typeof obj === 'object' && obj !== null && 'stdout' in obj) {
      let text = (obj.stdout || '').trim();
      if (obj.stderr) text += (text ? '\n' : '') + obj.stderr.trim();
      if (obj.exit_code && obj.exit_code !== 0) text += `\n(exit code: ${obj.exit_code})`;
      return text || content;
    }
    // Other structured results — return pretty-printed.
    return JSON.stringify(obj, null, 2);
  } catch {
    // Not JSON — return as-is.
    return content;
  }
}

// ── Chat Stream Hook ─────────────────────────────────────────

function useChatStream(agentId) {
  const [messages, setMessages] = useState([]);
  const [streaming, setStreaming] = useState(false);
  const [sessionId, setSessionId] = useState(null);
  const abortRef = useRef(null);

  const loadSession = useCallback(async (sid) => {
    try {
      const data = await fetchSession(agentId, sid);
      setSessionId(data.task_id || sid);
      // Convert session messages to display format.
      // Group assistant tool_calls with their tool results, skip system messages.
      const raw = data.messages || [];
      const display = [];
      let i = 0;
      while (i < raw.length) {
        const m = raw[i];
        // Skip system messages.
        if (m.role === 'system') { i++; continue; }
        // User messages.
        if (m.role === 'user') {
          display.push({ role: 'user', content: m.content || '' });
          i++;
          continue;
        }
        // Assistant message with tool calls — group with following tool results.
        if (m.role === 'assistant' && m.tool_calls && m.tool_calls.length > 0) {
          const tools = m.tool_calls.map(tc => ({
            name: tc.function?.name || tc.id || 'tool',
            phase: 'end',
            message: '',
          }));
          // Consume following tool-role messages and attach results.
          let j = i + 1;
          while (j < raw.length && raw[j].role === 'tool') {
            const toolMsg = raw[j];
            const matchIdx = tools.findIndex(t =>
              t.name === toolMsg.name ||
              (m.tool_calls.find(tc => tc.id === toolMsg.tool_call_id)?.function?.name === t.name)
            );
            if (matchIdx >= 0) {
              tools[matchIdx].message = formatToolContent(toolMsg.content);
            }
            j++;
          }
          display.push({ role: 'agent', content: m.content || '', tools });
          i = j;
          continue;
        }
        // Plain assistant message (no tool calls).
        if (m.role === 'assistant') {
          display.push({ role: 'agent', content: m.content || '' });
          i++;
          continue;
        }
        // Skip orphaned tool messages or unknown roles.
        i++;
      }
      setMessages(display);
    } catch (err) {
      console.error('Failed to load session:', err);
    }
  }, [agentId]);

  const newSession = useCallback(() => {
    setMessages([]);
    setSessionId(null);
  }, []);

  const sendMessage = useCallback(async (text) => {
    if (streaming) return;

    // Append user message
    setMessages(prev => [...prev, { role: 'user', content: text }]);
    setStreaming(true);

    const controller = new AbortController();
    abortRef.current = controller;

    try {
      const res = await fetch(`/api/agents/${agentId}/chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          message: text,
          session_id: sessionId || undefined,
        }),
        signal: controller.signal,
      });

      if (!res.ok) {
        const errBody = await res.json().catch(() => ({}));
        setMessages(prev => [...prev, { role: 'error', content: errBody.error || `Error: ${res.status}` }]);
        setStreaming(false);
        return;
      }

      // Parse SSE stream
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buffer = '';
      let currentTools = [];
      let agentText = '';
      let receivedSessionId = sessionId;

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;

        buffer += decoder.decode(value, { stream: true });
        const frames = buffer.split('\n\n');
        buffer = frames.pop(); // Keep incomplete frame

        for (const frame of frames) {
          if (!frame.trim()) continue;

          let eventType = '';
          let eventData = '';

          for (const line of frame.split('\n')) {
            if (line.startsWith('event:')) {
              eventType = line.slice(6).trim();
            } else if (line.startsWith('data:')) {
              eventData = line.slice(5).trim();
            }
          }

          if (!eventType || !eventData) continue;

          try {
            const parsed = JSON.parse(eventData);

            if (eventType === 'status') {
              // Task state change — extract agent message if present
              const status = parsed.status || parsed;
              if (status.message && status.message.parts) {
                for (const part of status.message.parts) {
                  if (part.kind === 'text' && part.text) {
                    agentText = part.text;
                  }
                }
              }
            } else if (eventType === 'progress') {
              // Tool execution progress
              const status = parsed.status || parsed;
              if (status.message && status.message.parts) {
                for (const part of status.message.parts) {
                  if (part.kind === 'data' && part.data) {
                    const toolInfo = part.data;
                    currentTools = [...currentTools.filter(t =>
                      !(t.name === toolInfo.name && t.phase === 'start')
                    ), toolInfo];
                  }
                }
              }
              // Update messages in real-time to show tool progress
              setMessages(prev => {
                const last = prev[prev.length - 1];
                if (last && last.role === 'agent' && last.isStreaming) {
                  const updated = [...prev];
                  updated[updated.length - 1] = { ...last, tools: [...currentTools] };
                  return updated;
                }
                return [...prev, { role: 'agent', content: '', tools: [...currentTools], isStreaming: true }];
              });
            } else if (eventType === 'result') {
              // Final result
              const task = parsed;
              const status = task.status || parsed;
              if (status.message && status.message.parts) {
                for (const part of status.message.parts) {
                  if (part.kind === 'text' && part.text) {
                    agentText = part.text;
                  }
                }
              }
            } else if (eventType === 'done') {
              if (parsed.session_id) {
                receivedSessionId = parsed.session_id;
              }
            }
          } catch { /* skip malformed events */ }
        }
      }

      // Finalize: set the agent response
      if (agentText || currentTools.length > 0) {
        setMessages(prev => {
          // Remove streaming placeholder if present
          const filtered = prev.filter(m => !m.isStreaming);
          return [...filtered, { role: 'agent', content: agentText, tools: currentTools }];
        });
      }

      if (receivedSessionId) {
        setSessionId(receivedSessionId);
      }

    } catch (err) {
      if (err.name !== 'AbortError') {
        setMessages(prev => [...prev, { role: 'error', content: 'Connection error: ' + err.message }]);
      }
    } finally {
      setStreaming(false);
      abortRef.current = null;
    }
  }, [agentId, sessionId, streaming]);

  const cancel = useCallback(() => {
    if (abortRef.current) {
      abortRef.current.abort();
    }
  }, []);

  return { messages, streaming, sessionId, sendMessage, loadSession, newSession, cancel };
}

// ── Components ───────────────────────────────────────────────

function StatusDot({ status }) {
  return html`<span class="status-dot ${status}" />`;
}

function AgentCard({ agent, onStart, onStop }) {
  const isActive = agent.status === 'running' || agent.status === 'starting';
  const isBusy = agent.status === 'starting' || agent.status === 'stopping';

  return html`
    <div class="agent-card" onClick=${() => isActive && navigate('agent/' + agent.id)}>
      <div class="agent-card-header">
        <div>
          <div class="agent-card-name">${agent.id}</div>
          <div class="agent-card-version">v${agent.version || '0.0.0'}</div>
        </div>
        <div class="agent-card-status">
          <${StatusDot} status=${agent.status} />
          ${agent.status}
          ${isBusy && html`<span class="spinner" />`}
        </div>
      </div>

      <div class="agent-card-meta">
        ${agent.model?.provider && html`
          <span class="agent-card-tag">
            <span class="tag-label">model</span>
            ${agent.model.provider}/${agent.model.name || '\u2014'}
          </span>
        `}
        ${agent.framework && html`
          <span class="agent-card-tag">
            <span class="tag-label">fw</span>
            ${agent.framework}
          </span>
        `}
        ${(agent.tools?.length > 0) && html`
          <span class="agent-card-tag">
            <span class="tag-label">tools</span>
            ${agent.tools.length}
          </span>
        `}
        ${(agent.skills > 0) && html`
          <span class="agent-card-tag">
            <span class="tag-label">skills</span>
            ${agent.skills}
          </span>
        `}
        ${(agent.channels?.length > 0) && html`
          <span class="agent-card-tag">
            <span class="tag-label">channels</span>
            ${agent.channels.join(', ')}
          </span>
        `}
        ${agent.port > 0 && html`
          <span class="agent-card-tag">
            <span class="tag-label">port</span>
            ${agent.port}
          </span>
        `}
      </div>

      ${agent.error && html`
        <div class="agent-card-error">${agent.error}</div>
      `}

      <div class="agent-card-actions">
        ${!isActive && html`
          <button class="btn btn-primary btn-sm" onClick=${(e) => { e.stopPropagation(); onStart(agent.id); }} disabled=${isBusy}>
            Start
          </button>
        `}
        ${isActive && html`
          <button class="btn btn-danger btn-sm" onClick=${(e) => { e.stopPropagation(); onStop(agent.id); }} disabled=${isBusy}>
            Stop
          </button>
        `}
        ${isActive && html`
          <button class="btn btn-ghost btn-sm" onClick=${(e) => { e.stopPropagation(); navigate('agent/' + agent.id); }}>
            Chat
          </button>
        `}
        <button class="btn btn-ghost btn-sm" onClick=${(e) => { e.stopPropagation(); navigate('config/' + agent.id); }}>
          Config
        </button>
      </div>
    </div>
  `;
}

function EmptyState() {
  return html`
    <div class="empty-state">
      <div class="empty-state-icon">&#9881;</div>
      <div class="empty-state-title">No agents found</div>
      <div class="empty-state-text">
        Create agent directories with forge.yaml files in your workspace, then click Rescan.
      </div>
    </div>
  `;
}

function Sidebar({ agents, activeAgentId, activePage }) {
  return html`
    <aside class="sidebar">
      <div class="sidebar-header">
        <div class="sidebar-logo" onClick=${() => navigate('')} style="cursor: pointer;">
          <div class="sidebar-logo-icon">F</div>
          <div class="sidebar-logo-text">Forge</div>
        </div>
      </div>
      <div class="sidebar-nav">
        <div class="sidebar-nav-item ${activePage === 'dashboard' ? 'active' : ''}" onClick=${() => navigate('')}>
          <span class="sidebar-nav-icon">\u2302</span>
          Dashboard
        </div>
        <div class="sidebar-nav-item ${activePage === 'create' ? 'active' : ''}" onClick=${() => navigate('create')}>
          <span class="sidebar-nav-icon">+</span>
          New Agent
        </div>
        <div class="sidebar-nav-item ${activePage === 'skills' ? 'active' : ''}" onClick=${() => navigate('skills')}>
          <span class="sidebar-nav-icon">\u2606</span>
          Skills Browser
        </div>
      </div>
      <div class="sidebar-label">Agents</div>
      <div class="sidebar-agents">
        ${agents.map(a => html`
          <div
            class="sidebar-agent ${a.id === activeAgentId ? 'active' : ''}"
            key=${a.id}
            onClick=${() => {
              if (a.status === 'running' || a.status === 'starting') {
                navigate('agent/' + a.id);
              }
            }}
          >
            <${StatusDot} status=${a.status} />
            <span>${a.id}</span>
            <div class="sidebar-agent-actions">
              <button class="sidebar-config-btn" onClick=${(e) => { e.stopPropagation(); navigate('config/' + a.id); }}>cfg</button>
            </div>
            ${(a.status === 'running' || a.status === 'starting') && html`
              <span class="sidebar-chat-label">Chat</span>
            `}
          </div>
        `)}
        ${agents.length === 0 && html`
          <div style="padding: 12px; font-size: 12px; color: var(--text-muted);">
            No agents discovered
          </div>
        `}
      </div>
    </aside>
  `;
}

function Dashboard({ agents, onStart, onStop, onRescan, loading }) {
  return html`
    <main class="main">
      <div class="main-header">
        <div>
          <div class="main-title">Dashboard</div>
          <div class="main-subtitle">${agents.length} agent${agents.length !== 1 ? 's' : ''} discovered</div>
        </div>
        <div style="display: flex; gap: 8px;">
          <button class="btn btn-primary" onClick=${() => navigate('create')}>
            + New Agent
          </button>
          <button class="btn btn-ghost" onClick=${onRescan} disabled=${loading}>
            ${loading ? html`<span class="spinner" />` : 'Rescan'}
          </button>
        </div>
      </div>

      ${agents.length > 0
        ? html`
          <div class="agent-grid">
            ${agents.map(a => html`
              <${AgentCard}
                key=${a.id}
                agent=${a}
                onStart=${onStart}
                onStop=${onStop}
              />
            `)}
          </div>
        `
        : html`<${EmptyState} />`
      }
    </main>
  `;
}

// ── Tool Card Component ──────────────────────────────────────

function ToolCard({ tool }) {
  const [expanded, setExpanded] = useState(false);
  const phase = tool.phase || 'unknown';
  const phaseClass = phase === 'end' ? 'tool-done' : 'tool-running';

  return html`
    <div class="chat-tool-card ${phaseClass}" onClick=${() => setExpanded(!expanded)}>
      <div class="chat-tool-header">
        <span class="chat-tool-icon">${phase === 'end' ? '\u2713' : '\u25B6'}</span>
        <span class="chat-tool-name">${tool.name || 'tool'}</span>
        <span class="chat-tool-phase">${phase === 'end' ? 'completed' : 'running...'}</span>
        <span class="chat-tool-chevron ${expanded ? 'expanded' : ''}">\u25B8</span>
      </div>
      ${expanded && tool.message && html`
        <div class="chat-tool-body">${tool.message}</div>
      `}
    </div>
  `;
}

// ── Message Bubble Component ─────────────────────────────────

function MessageBubble({ message }) {
  if (message.role === 'error') {
    return html`
      <div class="chat-bubble error">
        <div class="chat-bubble-content">${message.content}</div>
      </div>
    `;
  }

  if (message.role === 'user') {
    return html`
      <div class="chat-bubble user">
        <div class="chat-bubble-content">${message.content}</div>
      </div>
    `;
  }

  // Agent message
  return html`
    <div class="chat-bubble agent">
      ${message.tools && message.tools.length > 0 && html`
        <div class="chat-tools">
          ${message.tools.map((t, i) => html`<${ToolCard} key=${i} tool=${t} />`)}
        </div>
      `}
      ${message.content && html`
        <div class="chat-bubble-content" dangerouslySetInnerHTML=${{ __html: renderMarkdown(message.content) }} />
      `}
      ${message.isStreaming && !message.content && html`
        <div class="chat-bubble-content"><span class="typing-indicator" /></div>
      `}
    </div>
  `;
}

// ── Chat Page Component ──────────────────────────────────────

function ChatPage({ agentId, agents }) {
  const agent = useMemo(() => agents.find(a => a.id === agentId), [agents, agentId]);
  const isRunning = agent && (agent.status === 'running' || agent.status === 'starting');

  const { messages, streaming, sessionId, sendMessage, loadSession, newSession, cancel } = useChatStream(agentId);
  const [sessions, setSessions] = useState([]);
  const [inputText, setInputText] = useState('');
  const messagesEndRef = useRef(null);
  const messagesContainerRef = useRef(null);
  const userScrolledUp = useRef(false);
  const textareaRef = useRef(null);

  // Load sessions on mount
  useEffect(() => {
    fetchSessions(agentId).then(s => setSessions(s || [])).catch(() => {});
  }, [agentId, sessionId]);

  // Auto-scroll
  useEffect(() => {
    if (!userScrolledUp.current && messagesEndRef.current) {
      messagesEndRef.current.scrollIntoView({ behavior: 'smooth' });
    }
  }, [messages]);

  const handleScroll = useCallback(() => {
    const el = messagesContainerRef.current;
    if (!el) return;
    userScrolledUp.current = el.scrollTop + el.clientHeight < el.scrollHeight - 50;
  }, []);

  const handleSend = useCallback(() => {
    const text = inputText.trim();
    if (!text || streaming || !isRunning) return;
    setInputText('');
    if (textareaRef.current) {
      textareaRef.current.style.height = 'auto';
    }
    sendMessage(text);
  }, [inputText, streaming, isRunning, sendMessage]);

  const handleKeyDown = useCallback((e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  }, [handleSend]);

  const handleInput = useCallback((e) => {
    setInputText(e.target.value);
    // Auto-grow textarea
    e.target.style.height = 'auto';
    e.target.style.height = Math.min(e.target.scrollHeight, 200) + 'px';
  }, []);

  return html`
    <main class="main chat-layout">
      <div class="chat-sessions">
        <div class="chat-sessions-header">
          <span>Sessions</span>
          <button class="btn btn-ghost btn-sm" onClick=${newSession}>New</button>
        </div>
        <div class="chat-sessions-list">
          ${sessions.map(s => html`
            <div
              key=${s.id}
              class="chat-session-item ${s.id === sessionId ? 'active' : ''}"
              onClick=${() => loadSession(s.id)}
            >
              <div class="chat-session-preview">${s.preview || 'Empty session'}</div>
              <div class="chat-session-time">${formatTime(s.updated_at)}</div>
            </div>
          `)}
          ${sessions.length === 0 && html`
            <div class="chat-session-empty">No previous sessions</div>
          `}
        </div>
      </div>

      <div class="chat-main">
        <div class="chat-main-header">
          <button class="btn btn-ghost btn-sm" onClick=${() => navigate('')}>\u2190 Back</button>
          <div class="chat-agent-info">
            <${StatusDot} status=${agent?.status || 'stopped'} />
            <span class="chat-agent-name">${agentId}</span>
            ${agent?.port > 0 && html`<span class="chat-agent-port">:${agent.port}</span>`}
          </div>
        </div>

        <div class="chat-messages" ref=${messagesContainerRef} onScroll=${handleScroll}>
          ${messages.length === 0 && html`
            <div class="chat-empty">
              <div class="chat-empty-icon">\u{1F4AC}</div>
              <div class="chat-empty-title">Start a conversation</div>
              <div class="chat-empty-text">
                ${isRunning
                  ? 'Send a message to begin chatting with this agent.'
                  : 'Start the agent first to enable chat.'}
              </div>
            </div>
          `}
          ${messages.map((m, i) => html`<${MessageBubble} key=${i} message=${m} />`)}
          <div ref=${messagesEndRef} />
        </div>

        <div class="chat-input">
          <textarea
            ref=${textareaRef}
            class="chat-textarea"
            placeholder=${isRunning ? 'Type a message... (Enter to send, Shift+Enter for newline)' : 'Agent is not running'}
            value=${inputText}
            onInput=${handleInput}
            onKeyDown=${handleKeyDown}
            disabled=${!isRunning}
            rows="1"
          />
          <div class="chat-input-actions">
            ${streaming
              ? html`<button class="btn btn-danger btn-sm" onClick=${cancel}>Stop</button>`
              : html`<button class="btn btn-primary btn-sm" onClick=${handleSend} disabled=${!inputText.trim() || !isRunning}>Send</button>`
            }
          </div>
        </div>
      </div>
    </main>
  `;
}

function formatTime(isoString) {
  if (!isoString) return '';
  const d = new Date(isoString);
  const now = new Date();
  const diffMs = now - d;
  const diffMins = Math.floor(diffMs / 60000);
  if (diffMins < 1) return 'just now';
  if (diffMins < 60) return `${diffMins}m ago`;
  const diffHours = Math.floor(diffMins / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  return d.toLocaleDateString();
}

// ── Passphrase Modal ─────────────────────────────────────────

function PassphraseModal({ agentId, onSubmit, onCancel, error }) {
  const [value, setValue] = useState('');
  const inputRef = useRef(null);

  useEffect(() => {
    if (inputRef.current) inputRef.current.focus();
  }, []);

  const handleSubmit = useCallback((e) => {
    e.preventDefault();
    if (value.trim()) onSubmit(value.trim());
  }, [value, onSubmit]);

  return html`
    <div class="modal-overlay" onClick=${onCancel}>
      <div class="modal" onClick=${(e) => e.stopPropagation()}>
        <div class="modal-header">
          <div class="modal-title">Passphrase Required</div>
          <div class="modal-subtitle">Agent <strong>${agentId}</strong> uses encrypted secrets</div>
        </div>
        <form onSubmit=${handleSubmit}>
          <input
            ref=${inputRef}
            type="password"
            class="modal-input"
            placeholder="Enter passphrase for secrets.enc"
            value=${value}
            onInput=${(e) => setValue(e.target.value)}
            autocomplete="off"
          />
          ${error && html`<div class="modal-error">${error}</div>`}
          <div class="modal-actions">
            <button type="button" class="btn btn-ghost" onClick=${onCancel}>Cancel</button>
            <button type="submit" class="btn btn-primary" disabled=${!value.trim()}>Unlock & Start</button>
          </div>
        </form>
      </div>
    </div>
  `;
}

// ── Monaco Loader ────────────────────────────────────────────

function loadMonaco() {
  return new Promise((resolve) => {
    if (window.monaco) { resolve(window.monaco); return; }
    const link = document.createElement('link');
    link.rel = 'stylesheet';
    link.href = '/monaco/editor.css';
    document.head.appendChild(link);
    window.MonacoEnvironment = {
      getWorkerUrl: () => '/monaco/editor.worker.js'
    };
    const script = document.createElement('script');
    script.src = '/monaco/editor.js';
    script.onload = () => resolve(window.monaco);
    script.onerror = () => resolve(null);
    document.head.appendChild(script);
  });
}

// ── Create Agent Wizard ──────────────────────────────────────

const WIZARD_STEPS = ['Name', 'Provider', 'Model & Key', 'Channels', 'Tools', 'Skills', 'Fallback', 'Env Vars', 'Review'];

// Provider-specific API key labels, placeholders, and env var names
const PROVIDER_KEY_INFO = {
  openai:    { label: 'OpenAI API Key',    placeholder: 'sk-...', envVar: 'OPENAI_API_KEY' },
  anthropic: { label: 'Anthropic API Key', placeholder: 'sk-ant-...', envVar: 'ANTHROPIC_API_KEY' },
  gemini:    { label: 'Gemini API Key',    placeholder: 'AI...', envVar: 'GEMINI_API_KEY' },
  custom:    { label: 'API Key / Auth Token', placeholder: 'your-api-key', envVar: 'MODEL_API_KEY' },
};

// Channel-specific token fields
const CHANNEL_TOKEN_FIELDS = {
  telegram: [
    { key: 'TELEGRAM_BOT_TOKEN', label: 'Telegram Bot Token', placeholder: '123456:ABC-DEF...', hint: 'Get from @BotFather on Telegram' },
  ],
  slack: [
    { key: 'SLACK_APP_TOKEN', label: 'Slack App Token', placeholder: 'xapp-...', hint: 'App-level token from api.slack.com' },
    { key: 'SLACK_BOT_TOKEN', label: 'Slack Bot Token', placeholder: 'xoxb-...', hint: 'Bot user OAuth token' },
  ],
};

// Fallback provider env var keys
const FALLBACK_KEY_MAP = {
  openai: 'OPENAI_API_KEY', anthropic: 'ANTHROPIC_API_KEY', gemini: 'GEMINI_API_KEY',
};

function slugify(name) {
  return name.toLowerCase().trim().replace(/\s+/g, '-').replace(/[^a-z0-9-]/g, '').replace(/-{2,}/g, '-').replace(/^-|-$/g, '');
}

// Helper: check if an env var key looks like a secret
function isSecretKey(key) {
  return /_API_KEY$|_TOKEN$|_SECRET$|_PASSWORD$/.test(key);
}

function CreatePage() {
  const [step, setStep] = useState(0);
  const [meta, setMeta] = useState(null);
  const [form, setForm] = useState({
    name: '', framework: 'forge', model_provider: '', model_name: '', api_key: '',
    auth_method: 'apikey', // "apikey" or "oauth"
    web_search_provider: '', // "tavily" or "perplexity"
    channels: [], builtin_tools: [], skills: [],
    fallbacks: [], // [{provider, api_key}]
    passphrase: '',
    env_vars: {},
  });
  const [creating, setCreating] = useState(false);
  const [oauthLoading, setOauthLoading] = useState(false);
  const [oauthDone, setOauthDone] = useState(false);
  const [error, setError] = useState(null);
  const [success, setSuccess] = useState(null);

  useEffect(() => {
    fetchWizardMeta().then(setMeta).catch(err => setError(err.message));
  }, []);

  const updateForm = useCallback((key, val) => {
    setForm(prev => ({ ...prev, [key]: val }));
  }, []);

  const updateEnvVar = useCallback((envKey, val) => {
    setForm(prev => ({ ...prev, env_vars: { ...prev.env_vars, [envKey]: val } }));
  }, []);

  const toggleList = useCallback((key, item) => {
    setForm(prev => {
      const list = prev[key] || [];
      return { ...prev, [key]: list.includes(item) ? list.filter(x => x !== item) : [...list, item] };
    });
  }, []);

  // Collect required env vars from selected skills
  const skillEnvVars = useMemo(() => {
    if (!meta?.skills) return { required: [], oneOf: [], optional: [] };
    const required = [], oneOf = [], optional = [];
    const seen = new Set();
    for (const skillName of form.skills) {
      const skill = meta.skills.find(s => s.name === skillName);
      if (!skill) continue;
      for (const env of (skill.required_env || [])) {
        if (!seen.has(env)) { seen.add(env); required.push({ key: env, skill: skill.display_name || skillName }); }
      }
      for (const env of (skill.one_of_env || [])) {
        if (!seen.has(env)) { seen.add(env); oneOf.push({ key: env, skill: skill.display_name || skillName }); }
      }
      for (const env of (skill.optional_env || [])) {
        if (!seen.has(env)) { seen.add(env); optional.push({ key: env, skill: skill.display_name || skillName }); }
      }
    }
    return { required, oneOf, optional };
  }, [meta, form.skills]);

  // Get model list for current provider
  const providerMeta = useMemo(() => {
    if (!meta?.provider_models || !form.model_provider) return null;
    return meta.provider_models[form.model_provider] || null;
  }, [meta, form.model_provider]);

  const modelList = useMemo(() => {
    if (!providerMeta) return [];
    if (form.model_provider === 'openai' && form.auth_method === 'oauth') {
      return providerMeta.oauth || [];
    }
    return providerMeta.api_key || [];
  }, [providerMeta, form.model_provider, form.auth_method]);

  // Check if any env vars look like secrets (need passphrase)
  const hasSecrets = useMemo(() => {
    if (form.api_key) return true;
    for (const [k, v] of Object.entries(form.env_vars)) {
      if (v && isSecretKey(k)) return true;
    }
    for (const fb of form.fallbacks) {
      if (fb.api_key) return true;
    }
    return false;
  }, [form]);

  const canNext = useMemo(() => {
    if (step === 0) return form.name.trim().length > 0;
    if (step === 1) return form.model_provider.length > 0;
    if (step === 2) {
      if (form.model_provider === 'openai' && form.auth_method === 'oauth') {
        return oauthDone;
      }
      return form.model_name.trim().length > 0;
    }
    return true;
  }, [step, form, oauthDone]);

  const handleCreate = useCallback(async () => {
    setCreating(true);
    setError(null);
    try {
      const result = await createAgent(form);
      setSuccess(result);
      setTimeout(() => rescanAgents().catch(() => {}), 500);
    } catch (err) {
      setError(err.message);
    } finally {
      setCreating(false);
    }
  }, [form]);

  const handleOAuth = useCallback(async () => {
    setOauthLoading(true);
    setError(null);
    try {
      await startOAuth(form.model_provider);
      setOauthDone(true);
      updateForm('api_key', '__oauth__');
    } catch (err) {
      setError('OAuth failed: ' + err.message);
    } finally {
      setOauthLoading(false);
    }
  }, [form.model_provider]);

  if (!meta && !error) {
    return html`<main class="main"><div class="config-loading"><span class="spinner" /> Loading wizard...</div></main>`;
  }

  if (success) {
    return html`
      <main class="main">
        <div class="wizard-layout">
          <div class="wizard-success">
            <div class="wizard-success-icon">\u2713</div>
            <div class="wizard-success-title">Agent Created</div>
            <div class="wizard-success-text">
              <strong>${success.agent_id}</strong> has been scaffolded in<br/>
              <code>${success.directory}</code>
            </div>
            <div style="display: flex; gap: 8px; justify-content: center;">
              <button class="btn btn-primary" onClick=${() => navigate('')}>Go to Dashboard</button>
              <button class="btn btn-ghost" onClick=${() => navigate('config/' + success.agent_id)}>Edit Config</button>
            </div>
          </div>
        </div>
      </main>
    `;
  }

  // Helper to render an env var input field
  const envField = (key, label, placeholder, hint, required) => html`
    <div style="margin-top: 12px;">
      <label style="font-size: 12px; color: var(--text-muted); display: block; margin-bottom: 4px;">
        ${label}${required ? html` <span style="color: var(--accent);">*</span>` : ''}
      </label>
      <input class="wizard-input" type=${isSecretKey(key) ? 'password' : 'text'}
        placeholder=${placeholder || key} value=${form.env_vars[key] || ''}
        onInput=${(e) => updateEnvVar(key, e.target.value)} autocomplete="off" />
      ${hint && html`<div style="font-size: 11px; color: var(--text-muted); margin-top: 3px;">${hint}</div>`}
    </div>
  `;

  const renderStep = () => {
    switch (step) {
      case 0: // Name
        return html`
          <div class="wizard-step">
            <div class="wizard-step-title">Agent Name</div>
            <div class="wizard-step-desc">Choose a name for your new agent. This will become the directory name.</div>
            <input class="wizard-input" placeholder="My Agent" value=${form.name}
              onInput=${(e) => updateForm('name', e.target.value)} autofocus />
            ${form.name && html`<div class="wizard-slug-preview">ID: ${slugify(form.name)}</div>`}
          </div>
        `;
      case 1: { // Provider
        const handleProviderSelect = (p) => {
          updateForm('model_provider', p);
          // Auto-fill default model name when switching providers
          const pm = meta?.provider_models?.[p];
          if (pm) {
            updateForm('model_name', pm.default || '');
          }
          // Reset auth method and OAuth state when switching providers
          updateForm('auth_method', 'apikey');
          setOauthDone(false);
        };
        const descriptions = {
          openai: 'GPT 5.3 Codex, GPT 5.2, GPT 5 Mini',
          anthropic: 'Claude Sonnet, Haiku, Opus',
          gemini: 'Gemini 2.5 Flash, Pro',
          ollama: 'Run models locally, no API key needed',
          custom: 'Any OpenAI-compatible endpoint',
        };
        return html`
          <div class="wizard-step">
            <div class="wizard-step-title">Model Provider</div>
            <div class="wizard-step-desc">Select the LLM provider for your agent.</div>
            <div class="wizard-radio-group">
              ${(meta?.providers || []).map(p => html`
                <div class="wizard-radio ${form.model_provider === p ? 'selected' : ''}"
                  onClick=${() => handleProviderSelect(p)}>
                  <div class="wizard-radio-dot" />
                  <div>
                    <span style="font-weight: 500;">${p}</span>
                    ${descriptions[p] ? html`<div style="font-size: 11px; color: var(--text-muted);">${descriptions[p]}</div>` : ''}
                  </div>
                </div>
              `)}
            </div>
          </div>
        `;
      }
      case 2: { // Model & Key
        const keyInfo = PROVIDER_KEY_INFO[form.model_provider];
        const needsKey = providerMeta?.needs_key !== false;
        const isCustom = providerMeta?.is_custom === true;
        const hasOAuth = providerMeta?.has_oauth === true;

        return html`
          <div class="wizard-step">
            <div class="wizard-step-title">Model & Authentication</div>
            <div class="wizard-step-desc">Configure the model${needsKey ? ' and credentials' : ''} for ${form.model_provider}.</div>

            ${hasOAuth && html`
              <div style="margin-bottom: 16px;">
                <label style="font-size: 12px; color: var(--text-muted); display: block; margin-bottom: 6px;">Authentication Method</label>
                <div class="wizard-radio-group" style="margin-bottom: 0;">
                  <div class="wizard-radio ${form.auth_method === 'apikey' ? 'selected' : ''}"
                    onClick=${() => { updateForm('auth_method', 'apikey'); setOauthDone(false); updateForm('api_key', ''); }}>
                    <div class="wizard-radio-dot" />
                    <div>
                      <span>Enter API Key</span>
                      <div style="font-size: 11px; color: var(--text-muted);">Paste your API key</div>
                    </div>
                  </div>
                  <div class="wizard-radio ${form.auth_method === 'oauth' ? 'selected' : ''}"
                    onClick=${() => { updateForm('auth_method', 'oauth'); updateForm('api_key', ''); }}>
                    <div class="wizard-radio-dot" />
                    <div>
                      <span>Login with ${form.model_provider}</span>
                      <div style="font-size: 11px; color: var(--text-muted);">Browser-based OAuth login</div>
                    </div>
                  </div>
                </div>
              </div>
            `}

            ${form.auth_method === 'oauth' ? html`
              <div style="margin-bottom: 16px;">
                ${oauthDone ? html`
                  <div style="display: flex; align-items: center; gap: 8px; padding: 12px; background: rgba(46, 160, 67, 0.1); border: 1px solid rgba(46, 160, 67, 0.3); border-radius: 6px;">
                    <span style="color: #2ea043; font-size: 18px;">\u2713</span>
                    <span style="color: #2ea043;">Authenticated successfully</span>
                  </div>
                ` : html`
                  <button class="btn btn-primary" onClick=${handleOAuth} disabled=${oauthLoading} style="width: 100%;">
                    ${oauthLoading ? html`<span class="spinner" /> Opening browser...` : `Login with ${form.model_provider}`}
                  </button>
                  <div style="font-size: 11px; color: var(--text-muted); margin-top: 6px; text-align: center;">
                    A browser window will open for authentication
                  </div>
                `}
              </div>
            ` : ''}

            ${isCustom && html`
              <div style="margin-bottom: 12px;">
                <label style="font-size: 12px; color: var(--text-muted); display: block; margin-bottom: 4px;">
                  Base URL <span style="color: var(--accent);">*</span>
                </label>
                <input class="wizard-input" placeholder="https://api.example.com/v1"
                  value=${form.env_vars['MODEL_BASE_URL'] || ''}
                  onInput=${(e) => updateEnvVar('MODEL_BASE_URL', e.target.value)} />
                <div style="font-size: 11px; color: var(--text-muted); margin-top: 3px;">OpenAI-compatible API endpoint</div>
              </div>
            `}

            <div style="margin-bottom: 12px;">
              <label style="font-size: 12px; color: var(--text-muted); display: block; margin-bottom: 4px;">Model</label>
              ${modelList.length > 0 ? html`
                <div class="wizard-radio-group">
                  ${modelList.map(m => html`
                    <div class="wizard-radio ${form.model_name === m.model_id ? 'selected' : ''}"
                      onClick=${() => updateForm('model_name', m.model_id)}>
                      <div class="wizard-radio-dot" />
                      <div>
                        <span>${m.display_name}</span>
                        <div style="font-size: 11px; color: var(--text-muted); font-family: monospace;">${m.model_id}</div>
                      </div>
                    </div>
                  `)}
                </div>
              ` : html`
                <input class="wizard-input" placeholder=${providerMeta?.default || 'model name'}
                  value=${form.model_name} onInput=${(e) => updateForm('model_name', e.target.value)} />
              `}
            </div>

            ${needsKey && form.auth_method === 'apikey' && keyInfo && html`
              <div>
                <label style="font-size: 12px; color: var(--text-muted); display: block; margin-bottom: 4px;">
                  ${keyInfo.label}
                </label>
                <input class="wizard-input" type="password" placeholder=${keyInfo.placeholder} value=${form.api_key}
                  onInput=${(e) => updateForm('api_key', e.target.value)} autocomplete="off" />
                <div style="font-size: 11px; color: var(--text-muted); margin-top: 3px;">
                  Stored in .env as ${keyInfo.envVar}. Leave empty to set later.
                </div>
              </div>
            `}
          </div>
        `;
      }
      case 3: { // Channels + Token Inputs
        const selectedTokenFields = form.channels.flatMap(ch => CHANNEL_TOKEN_FIELDS[ch] || []);
        return html`
          <div class="wizard-step">
            <div class="wizard-step-title">Channels</div>
            <div class="wizard-step-desc">Select communication channels and provide their credentials (optional).</div>
            <div class="wizard-checkbox-list">
              ${(meta?.channels || []).map(ch => html`
                <div class="wizard-checkbox-item ${form.channels.includes(ch) ? 'checked' : ''}"
                  onClick=${() => toggleList('channels', ch)}>
                  <div class="wizard-checkbox-box">${form.channels.includes(ch) ? '\u2713' : ''}</div>
                  <div>
                    <div class="wizard-checkbox-label">${ch}</div>
                  </div>
                </div>
              `)}
            </div>
            ${selectedTokenFields.length > 0 && html`
              <div style="margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--border-color);">
                <div style="font-size: 12px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 4px;">
                  Channel Credentials
                </div>
                ${selectedTokenFields.map(f => envField(f.key, f.label, f.placeholder, f.hint, true))}
              </div>
            `}
          </div>
        `;
      }
      case 4: { // Tools + Web Search Provider
        const hasWebSearch = form.builtin_tools.includes('web_search');
        const wsProviders = meta?.web_search_providers || [];
        const selectedWsProvider = wsProviders.find(p => p.name === form.web_search_provider);
        return html`
          <div class="wizard-step">
            <div class="wizard-step-title">Builtin Tools</div>
            <div class="wizard-step-desc">Select builtin tools for your agent (optional).</div>
            <div class="wizard-checkbox-list">
              ${(meta?.builtin_tools || []).map(t => html`
                <div class="wizard-checkbox-item ${form.builtin_tools.includes(t.name) ? 'checked' : ''}"
                  onClick=${() => {
                    toggleList('builtin_tools', t.name);
                    // Reset web search provider when deselecting web_search
                    if (t.name === 'web_search' && form.builtin_tools.includes('web_search')) {
                      updateForm('web_search_provider', '');
                    }
                  }}>
                  <div class="wizard-checkbox-box">${form.builtin_tools.includes(t.name) ? '\u2713' : ''}</div>
                  <div>
                    <div class="wizard-checkbox-label">${t.name}</div>
                    <div class="wizard-checkbox-desc">${t.description}</div>
                  </div>
                </div>
              `)}
            </div>
            ${hasWebSearch && html`
              <div style="margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--border-color);">
                <div style="font-size: 12px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 8px;">
                  Web Search Provider
                </div>
                <div class="wizard-radio-group">
                  ${wsProviders.map(p => html`
                    <div class="wizard-radio ${form.web_search_provider === p.name ? 'selected' : ''}"
                      onClick=${() => updateForm('web_search_provider', p.name)}>
                      <div class="wizard-radio-dot" />
                      <div>
                        <span>${p.label}</span>
                        <div style="font-size: 11px; color: var(--text-muted);">${p.description}</div>
                      </div>
                    </div>
                  `)}
                </div>
                ${selectedWsProvider && html`
                  ${envField(selectedWsProvider.env_var, selectedWsProvider.label + ' API Key', selectedWsProvider.placeholder,
                    'Required for web search', false)}
                `}
              </div>
            `}
          </div>
        `;
      }
      case 5: { // Skills + Required Env Vars
        const skills = meta?.skills || [];
        const categories = [...new Set(skills.map(s => s.category).filter(Boolean))].sort();
        const hasEnvReqs = skillEnvVars.required.length > 0 || skillEnvVars.oneOf.length > 0;
        return html`
          <div class="wizard-step">
            <div class="wizard-step-title">Skills</div>
            <div class="wizard-step-desc">Select registry skills for your agent (optional).</div>
            <div class="wizard-checkbox-list">
              ${categories.map(cat => html`
                <div class="wizard-checkbox-category">${cat}</div>
                ${skills.filter(s => s.category === cat).map(s => html`
                  <div class="wizard-checkbox-item ${form.skills.includes(s.name) ? 'checked' : ''}"
                    onClick=${() => toggleList('skills', s.name)}>
                    <div class="wizard-checkbox-box">${form.skills.includes(s.name) ? '\u2713' : ''}</div>
                    <div>
                      <div class="wizard-checkbox-label">${s.display_name || s.name}</div>
                      <div class="wizard-checkbox-desc">${s.description}</div>
                    </div>
                  </div>
                `)}
              `)}
              ${skills.filter(s => !s.category).map(s => html`
                <div class="wizard-checkbox-item ${form.skills.includes(s.name) ? 'checked' : ''}"
                  onClick=${() => toggleList('skills', s.name)}>
                  <div class="wizard-checkbox-box">${form.skills.includes(s.name) ? '\u2713' : ''}</div>
                  <div>
                    <div class="wizard-checkbox-label">${s.display_name || s.name}</div>
                    <div class="wizard-checkbox-desc">${s.description}</div>
                  </div>
                </div>
              `)}
            </div>
            ${hasEnvReqs && html`
              <div style="margin-top: 16px; padding-top: 16px; border-top: 1px solid var(--border-color);">
                <div style="font-size: 12px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 4px;">
                  Skill Credentials
                </div>
                ${skillEnvVars.required.map(({ key, skill }) =>
                  envField(key, key, '', 'Required by ' + skill, true)
                )}
                ${skillEnvVars.oneOf.map(({ key, skill }) =>
                  envField(key, key, '', 'One of required by ' + skill, false)
                )}
              </div>
            `}
            ${skillEnvVars.optional.length > 0 && html`
              <div style="margin-top: 12px;">
                <div style="font-size: 12px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 4px;">
                  Optional Skill Config
                </div>
                ${skillEnvVars.optional.map(({ key, skill }) =>
                  envField(key, key, '', 'Optional for ' + skill, false)
                )}
              </div>
            `}
          </div>
        `;
      }
      case 6: { // Fallback Providers
        const availableFallbacks = (meta?.providers || []).filter(p =>
          p !== form.model_provider && p !== 'custom'
        );
        const toggleFallback = (provider) => {
          setForm(prev => {
            const existing = prev.fallbacks.find(f => f.provider === provider);
            if (existing) {
              return { ...prev, fallbacks: prev.fallbacks.filter(f => f.provider !== provider) };
            }
            return { ...prev, fallbacks: [...prev.fallbacks, { provider, api_key: '' }] };
          });
        };
        const updateFallbackKey = (provider, key) => {
          setForm(prev => ({
            ...prev,
            fallbacks: prev.fallbacks.map(f => f.provider === provider ? { ...f, api_key: key } : f),
          }));
        };
        const fbDescriptions = {
          openai: 'GPT models', anthropic: 'Claude models', gemini: 'Gemini models', ollama: 'Local models (no key needed)',
        };
        return html`
          <div class="wizard-step">
            <div class="wizard-step-title">Fallback Providers</div>
            <div class="wizard-step-desc">Select backup providers for reliability. If the primary provider fails, the agent will automatically try fallbacks in order (optional).</div>
            <div class="wizard-checkbox-list">
              ${availableFallbacks.map(p => {
                const isSelected = form.fallbacks.some(f => f.provider === p);
                const fb = form.fallbacks.find(f => f.provider === p);
                const needsKey = p !== 'ollama';
                const fbKeyInfo = FALLBACK_KEY_MAP[p];
                return html`
                  <div class="wizard-checkbox-item ${isSelected ? 'checked' : ''}" onClick=${() => toggleFallback(p)}>
                    <div class="wizard-checkbox-box">${isSelected ? '\u2713' : ''}</div>
                    <div style="flex: 1;">
                      <div class="wizard-checkbox-label">${p}</div>
                      <div class="wizard-checkbox-desc">${fbDescriptions[p] || ''}</div>
                    </div>
                  </div>
                  ${isSelected && needsKey && fbKeyInfo ? html`
                    <div style="margin: -4px 0 8px 36px;">
                      <input class="wizard-input" type="password"
                        placeholder=${PROVIDER_KEY_INFO[p]?.placeholder || 'API key'}
                        value=${fb?.api_key || ''}
                        onInput=${(e) => updateFallbackKey(p, e.target.value)}
                        onClick=${(e) => e.stopPropagation()} autocomplete="off" />
                      <div style="font-size: 11px; color: var(--text-muted); margin-top: 3px;">
                        ${fbKeyInfo} \u2014 Leave empty to set later
                      </div>
                    </div>
                  ` : ''}
                `;
              })}
            </div>
          </div>
        `;
      }
      case 7: { // Additional Env Vars + Passphrase
        // Filter out vars already collected by previous steps
        const autoKeys = new Set([
          ...Object.keys(PROVIDER_KEY_INFO).map(p => PROVIDER_KEY_INFO[p].envVar),
          'MODEL_BASE_URL', 'WEB_SEARCH_PROVIDER',
          ...form.channels.flatMap(ch => (CHANNEL_TOKEN_FIELDS[ch] || []).map(f => f.key)),
          ...(meta?.web_search_providers || []).map(p => p.env_var),
          ...skillEnvVars.required.map(e => e.key),
          ...skillEnvVars.oneOf.map(e => e.key),
          ...skillEnvVars.optional.map(e => e.key),
        ]);
        const manualEntries = Object.entries(form.env_vars).filter(([k]) => !autoKeys.has(k));
        return html`
          <div class="wizard-step">
            <div class="wizard-step-title">Environment & Security</div>
            <div class="wizard-step-desc">Add extra environment variables and configure secret encryption (optional).</div>

            ${hasSecrets && html`
              <div style="margin-bottom: 20px; padding: 16px; background: rgba(139, 148, 158, 0.06); border: 1px solid var(--border-color); border-radius: 8px;">
                <div style="font-size: 12px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 8px;">
                  Secret Encryption
                </div>
                <div style="font-size: 12px; color: var(--text-muted); margin-bottom: 8px;">
                  Set a passphrase to encrypt API keys and tokens. Without this, secrets are stored in plaintext in .env.
                </div>
                <input class="wizard-input" type="password" placeholder="Enter passphrase for secret encryption"
                  value=${form.passphrase} onInput=${(e) => updateForm('passphrase', e.target.value)} autocomplete="off" />
                ${form.passphrase && html`
                  <input class="wizard-input" type="password" placeholder="Confirm passphrase"
                    style="margin-top: 8px;"
                    onInput=${(e) => {
                      if (e.target.value !== form.passphrase) {
                        e.target.style.borderColor = 'var(--error-color, #f85149)';
                      } else {
                        e.target.style.borderColor = 'var(--success-color, #2ea043)';
                      }
                    }} autocomplete="off" />
                `}
              </div>
            `}

            <div style="font-size: 12px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 8px;">
              Additional Environment Variables
            </div>
            <div class="wizard-env-list">
              ${manualEntries.map(([k, v], i) => html`
                <div class="wizard-env-row" key=${i}>
                  <input class="wizard-env-key" placeholder="KEY" value=${k}
                    onInput=${(e) => {
                      const newVars = { ...form.env_vars };
                      delete newVars[k];
                      newVars[e.target.value] = v;
                      updateForm('env_vars', newVars);
                    }} />
                  <input class="wizard-env-value" placeholder="value" value=${v}
                    onInput=${(e) => updateForm('env_vars', { ...form.env_vars, [k]: e.target.value })} />
                  <button class="wizard-env-remove" onClick=${() => {
                    const newVars = { ...form.env_vars };
                    delete newVars[k];
                    updateForm('env_vars', newVars);
                  }}>\u00D7</button>
                </div>
              `)}
              <button class="btn btn-ghost btn-sm" onClick=${() => updateForm('env_vars', { ...form.env_vars, '': '' })}>
                + Add Variable
              </button>
            </div>
          </div>
        `;
      }
      case 8: { // Review
        const configuredEnvCount = Object.values(form.env_vars).filter(v => v).length;
        const authLabel = form.auth_method === 'oauth' ? 'OAuth' : (form.api_key ? '\u2713 API Key provided' : 'Not set');
        return html`
          <div class="wizard-step">
            <div class="wizard-step-title">Review</div>
            <div class="wizard-step-desc">Review your selections before creating the agent.</div>
            <div class="wizard-review-section">
              <div class="wizard-review-label">Name</div>
              <div class="wizard-review-value">${form.name} (${slugify(form.name)})</div>
            </div>
            <div class="wizard-review-section">
              <div class="wizard-review-label">Provider / Model</div>
              <div class="wizard-review-value">${form.model_provider} / ${form.model_name || 'default'}</div>
            </div>
            <div class="wizard-review-section">
              <div class="wizard-review-label">Authentication</div>
              <div class="wizard-review-value">${authLabel}</div>
            </div>
            ${form.fallbacks.length > 0 && html`
              <div class="wizard-review-section">
                <div class="wizard-review-label">Fallbacks</div>
                <div class="wizard-review-list">
                  ${form.fallbacks.map(f => html`<span class="wizard-review-tag">${f.provider}${f.api_key ? ' \u2713' : ''}</span>`)}
                </div>
              </div>
            `}
            ${form.channels.length > 0 && html`
              <div class="wizard-review-section">
                <div class="wizard-review-label">Channels</div>
                <div class="wizard-review-list">
                  ${form.channels.map(c => html`<span class="wizard-review-tag">${c}</span>`)}
                </div>
              </div>
            `}
            ${form.builtin_tools.length > 0 && html`
              <div class="wizard-review-section">
                <div class="wizard-review-label">Builtin Tools</div>
                <div class="wizard-review-list">
                  ${form.builtin_tools.map(t => html`<span class="wizard-review-tag">${t}</span>`)}
                </div>
              </div>
            `}
            ${form.web_search_provider && html`
              <div class="wizard-review-section">
                <div class="wizard-review-label">Web Search</div>
                <div class="wizard-review-value">${form.web_search_provider}</div>
              </div>
            `}
            ${form.skills.length > 0 && html`
              <div class="wizard-review-section">
                <div class="wizard-review-label">Skills</div>
                <div class="wizard-review-list">
                  ${form.skills.map(s => html`<span class="wizard-review-tag">${s}</span>`)}
                </div>
              </div>
            `}
            ${configuredEnvCount > 0 && html`
              <div class="wizard-review-section">
                <div class="wizard-review-label">Environment Variables</div>
                <div class="wizard-review-value">${configuredEnvCount} variable${configuredEnvCount !== 1 ? 's' : ''} configured</div>
              </div>
            `}
            <div class="wizard-review-section">
              <div class="wizard-review-label">Secret Encryption</div>
              <div class="wizard-review-value">${form.passphrase ? '\u2713 Passphrase set' : 'None (plaintext .env)'}</div>
            </div>
          </div>
        `;
      }
      default:
        return null;
    }
  };

  return html`
    <main class="main">
      <div class="wizard-layout">
        <div class="wizard-header">
          <div class="wizard-title">Create New Agent</div>
          <div class="wizard-subtitle">Step ${step + 1} of ${WIZARD_STEPS.length}: ${WIZARD_STEPS[step]}</div>
        </div>
        <div class="wizard-progress">
          ${WIZARD_STEPS.map((_, i) => html`
            <div class="wizard-progress-dot ${i < step ? 'done' : ''} ${i === step ? 'active' : ''}" />
          `)}
        </div>
        ${renderStep()}
        ${error && html`<div class="wizard-error">${error}</div>`}
        <div class="wizard-nav">
          <button class="btn btn-ghost" onClick=${() => step > 0 ? setStep(step - 1) : navigate('')}>
            ${step > 0 ? 'Back' : 'Cancel'}
          </button>
          ${step < WIZARD_STEPS.length - 1
            ? html`<button class="btn btn-primary" onClick=${() => { setError(null); setStep(step + 1); }} disabled=${!canNext}>Next</button>`
            : html`<button class="btn btn-primary" onClick=${handleCreate} disabled=${creating}>
                ${creating ? html`<span class="spinner" /> Creating...` : 'Create Agent'}
              </button>`
          }
        </div>
      </div>
    </main>
  `;
}

// ── Config Editor Page ───────────────────────────────────────

function ConfigPage({ agentId }) {
  const [content, setContent] = useState(null);
  const [originalContent, setOriginalContent] = useState(null);
  const [monacoLoaded, setMonacoLoaded] = useState(false);
  const [validation, setValidation] = useState(null);
  const [saving, setSaving] = useState(false);
  const editorRef = useRef(null);
  const containerRef = useRef(null);

  // Load config
  useEffect(() => {
    fetchConfig(agentId).then(text => {
      setContent(text);
      setOriginalContent(text);
    }).catch(err => setValidation({ errors: [err.message] }));
  }, [agentId]);

  // Load Monaco
  useEffect(() => {
    loadMonaco().then(m => {
      if (m) setMonacoLoaded(true);
    });
  }, []);

  // Create editor
  useEffect(() => {
    if (!monacoLoaded || content === null || !containerRef.current || editorRef.current) return;
    const editor = window.monaco.editor.create(containerRef.current, {
      value: content,
      language: 'yaml',
      theme: 'vs-dark',
      minimap: { enabled: false },
      tabSize: 2,
      automaticLayout: true,
      scrollBeyondLastLine: false,
      fontSize: 13,
      fontFamily: "'SF Mono', 'Fira Code', 'Cascadia Code', monospace",
    });
    editor.onDidChangeModelContent(() => {
      setContent(editor.getValue());
    });
    // Cmd/Ctrl+S to save
    editor.addCommand(window.monaco.KeyMod.CtrlCmd | window.monaco.KeyCode.KeyS, () => {
      handleSave();
    });
    editorRef.current = editor;
    return () => { editor.dispose(); editorRef.current = null; };
  }, [monacoLoaded, content === null]);

  const isDirty = content !== null && content !== originalContent;

  const handleSave = useCallback(async () => {
    if (!content) return;
    setSaving(true);
    try {
      const result = await saveConfig(agentId, content);
      setValidation(result);
      if (result.valid) {
        setOriginalContent(content);
      }
    } catch (err) {
      setValidation({ errors: [err.message] });
    } finally {
      setSaving(false);
    }
  }, [agentId, content]);

  const handleValidate = useCallback(async () => {
    if (!content) return;
    try {
      const result = await validateConfig(agentId, content);
      setValidation(result);
    } catch (err) {
      setValidation({ errors: [err.message] });
    }
  }, [agentId, content]);

  const handleRestart = useCallback(async () => {
    try {
      await stopAgent(agentId);
      setTimeout(async () => {
        try { await startAgent(agentId); } catch { /* may not be startable */ }
      }, 1500);
    } catch { /* ignore stop errors */ }
  }, [agentId]);

  // Fallback textarea when Monaco is not available
  const renderEditor = () => {
    if (content === null) {
      return html`<div class="config-loading"><span class="spinner" /> Loading config...</div>`;
    }
    if (!monacoLoaded) {
      return html`
        <textarea class="chat-textarea" style="flex: 1; font-family: var(--font-mono); font-size: 13px; padding: 16px; resize: none; border: none; border-radius: 0;"
          value=${content} onInput=${(e) => setContent(e.target.value)} />
      `;
    }
    return html`<div ref=${containerRef} class="config-editor" />`;
  };

  return html`
    <main class="main config-layout">
      <div class="config-header">
        <div class="config-header-left">
          <button class="btn btn-ghost btn-sm" onClick=${() => navigate('')}>\u2190 Back</button>
          <div class="config-title">forge.yaml \u2014 ${agentId}</div>
          ${isDirty && html`<span class="config-dirty">unsaved</span>`}
        </div>
        <div class="config-actions">
          <button class="btn btn-ghost btn-sm" onClick=${handleValidate}>Validate</button>
          <button class="btn btn-primary btn-sm" onClick=${handleSave} disabled=${!isDirty || saving}>
            ${saving ? html`<span class="spinner" />` : 'Save'}
          </button>
          <button class="btn btn-ghost btn-sm" onClick=${handleRestart}>Restart Agent</button>
        </div>
      </div>
      ${renderEditor()}
      ${validation && html`
        <div class="config-validation">
          ${(validation.errors || []).map(e => html`<div class="validation-error">\u2717 ${e}</div>`)}
          ${(validation.warnings || []).map(w => html`<div class="validation-warning">\u26A0 ${w}</div>`)}
          ${validation.valid && html`<div class="validation-ok">\u2713 Configuration is valid</div>`}
        </div>
      `}
    </main>
  `;
}

// ── Skills Browser Page ──────────────────────────────────────

function SkillsPage() {
  const [skills, setSkills] = useState([]);
  const [loading, setLoading] = useState(true);
  const [categoryFilter, setCategoryFilter] = useState('');
  const [selectedSkill, setSelectedSkill] = useState(null);
  const [skillContent, setSkillContent] = useState('');

  useEffect(() => {
    setLoading(true);
    fetchSkills(categoryFilter || undefined).then(data => {
      setSkills(data || []);
    }).catch(() => {}).finally(() => setLoading(false));
  }, [categoryFilter]);

  const categories = useMemo(() => {
    const cats = [...new Set(skills.map(s => s.category).filter(Boolean))].sort();
    return cats;
  }, [skills]);

  const handleSelectSkill = useCallback(async (skill) => {
    if (selectedSkill?.name === skill.name) {
      setSelectedSkill(null);
      setSkillContent('');
      return;
    }
    setSelectedSkill(skill);
    try {
      const content = await fetchSkillContent(skill.name);
      setSkillContent(content);
    } catch {
      setSkillContent('Failed to load skill content.');
    }
  }, [selectedSkill]);

  return html`
    <main class="main skills-layout">
      <div class="skills-header">
        <div>
          <div class="skills-title">Skills Browser</div>
          <div class="skills-subtitle">${skills.length} skill${skills.length !== 1 ? 's' : ''} available</div>
        </div>
        <select class="skills-category-select" value=${categoryFilter}
          onChange=${(e) => setCategoryFilter(e.target.value)}>
          <option value="">All Categories</option>
          ${categories.map(c => html`<option value=${c}>${c}</option>`)}
        </select>
      </div>

      ${loading
        ? html`<div class="config-loading"><span class="spinner" /> Loading skills...</div>`
        : html`
          <div class="skills-content">
            <div class="skills-list-panel">
              <div class="skills-grid">
                ${skills.map(s => html`
                  <div class="skill-card ${selectedSkill?.name === s.name ? 'active' : ''}"
                    key=${s.name} onClick=${() => handleSelectSkill(s)}>
                    <div class="skill-card-name">${s.display_name || s.name}</div>
                    <div class="skill-card-desc">${s.description}</div>
                    <div class="skill-card-meta">
                      ${s.category && html`<span class="skill-tag category">${s.category}</span>`}
                      ${(s.tags || []).map(t => html`<span class="skill-tag">${t}</span>`)}
                    </div>
                  </div>
                `)}
              </div>
            </div>
            ${selectedSkill && html`
              <div class="skill-detail">
                <div class="skill-detail-header">
                  <div class="skill-detail-name">${selectedSkill.display_name || selectedSkill.name}</div>
                  <button class="skill-detail-close" onClick=${() => setSelectedSkill(null)}>\u00D7</button>
                </div>
                <div class="skill-detail-body" dangerouslySetInnerHTML=${{ __html: renderMarkdown(skillContent) }} />
                ${(selectedSkill.required_env?.length > 0 || selectedSkill.one_of_env?.length > 0) && html`
                  <div class="skill-detail-section">
                    <div class="skill-detail-section-title">Environment Variables</div>
                    <div class="skill-detail-env-list">
                      ${(selectedSkill.required_env || []).map(e => html`<div class="skill-detail-env">${e} (required)</div>`)}
                      ${(selectedSkill.one_of_env || []).map(e => html`<div class="skill-detail-env">${e} (one of)</div>`)}
                      ${(selectedSkill.optional_env || []).map(e => html`<div class="skill-detail-env">${e} (optional)</div>`)}
                    </div>
                  </div>
                `}
                ${selectedSkill.required_bins?.length > 0 && html`
                  <div class="skill-detail-section">
                    <div class="skill-detail-section-title">Required Binaries</div>
                    <div class="skill-detail-env-list">
                      ${selectedSkill.required_bins.map(b => html`<div class="skill-detail-env">${b}</div>`)}
                    </div>
                  </div>
                `}
              </div>
            `}
          </div>
        `
      }
    </main>
  `;
}

// ── App ──────────────────────────────────────────────────────

function App() {
  const [agents, setAgents] = useState([]);
  const [loading, setLoading] = useState(true);
  const [passphrasePrompt, setPassphrasePrompt] = useState(null); // { agentId, error }
  const route = useHashRoute();

  const loadAgents = useCallback(async () => {
    try {
      const data = await fetchAgents();
      setAgents(data || []);
    } catch (err) {
      console.error('Failed to load agents:', err);
    } finally {
      setLoading(false);
    }
  }, []);

  // Initial load
  useEffect(() => { loadAgents(); }, [loadAgents]);

  // Polling fallback (every 3s)
  useEffect(() => {
    const interval = setInterval(loadAgents, 3000);
    return () => clearInterval(interval);
  }, [loadAgents]);

  // SSE real-time updates
  useSSE((agentData) => {
    setAgents(prev => {
      const idx = prev.findIndex(a => a.id === agentData.id);
      if (idx === -1) return prev;
      const updated = [...prev];
      updated[idx] = { ...updated[idx], ...agentData };
      return updated;
    });
  });

  const handleStart = useCallback(async (id) => {
    // Check if agent needs passphrase
    const agent = agents.find(a => a.id === id);
    if (agent && agent.needs_passphrase && !cachedPassphrase) {
      setPassphrasePrompt({ agentId: id, error: null });
      return;
    }

    try {
      await startAgent(id, cachedPassphrase || undefined);
    } catch (err) {
      // If passphrase was wrong, prompt again
      if (err.message.includes('passphrase') || err.message.includes('decryption')) {
        cachedPassphrase = null;
        setPassphrasePrompt({ agentId: id, error: err.message });
        return;
      }
      console.error('Failed to start agent:', err);
    }
  }, [agents]);

  const handlePassphraseSubmit = useCallback(async (passphrase) => {
    const agentId = passphrasePrompt?.agentId;
    if (!agentId) return;

    try {
      await startAgent(agentId, passphrase);
      cachedPassphrase = passphrase;
      setPassphrasePrompt(null);
    } catch (err) {
      if (err.message.includes('passphrase') || err.message.includes('decryption')) {
        setPassphrasePrompt({ agentId, error: 'Wrong passphrase. Please try again.' });
      } else {
        setPassphrasePrompt({ agentId, error: err.message });
      }
    }
  }, [passphrasePrompt]);

  const handleStop = useCallback(async (id) => {
    try {
      await stopAgent(id);
    } catch (err) {
      console.error('Failed to stop agent:', err);
    }
  }, []);

  const handleRescan = useCallback(async () => {
    setLoading(true);
    try {
      const data = await rescanAgents();
      setAgents(data || []);
    } catch (err) {
      console.error('Failed to rescan:', err);
    } finally {
      setLoading(false);
    }
  }, []);

  const activeAgentId = route.page === 'chat' ? route.params.id : (route.page === 'config' ? route.params.id : null);

  const renderPage = () => {
    switch (route.page) {
      case 'chat':
        return html`<${ChatPage} agentId=${route.params.id} agents=${agents} />`;
      case 'create':
        return html`<${CreatePage} />`;
      case 'config':
        return html`<${ConfigPage} agentId=${route.params.id} />`;
      case 'skills':
        return html`<${SkillsPage} />`;
      default:
        return html`<${Dashboard}
          agents=${agents}
          onStart=${handleStart}
          onStop=${handleStop}
          onRescan=${handleRescan}
          loading=${loading}
        />`;
    }
  };

  return html`
    <div class="layout">
      <${Sidebar} agents=${agents} activeAgentId=${activeAgentId} activePage=${route.page} />
      ${renderPage()}
      ${passphrasePrompt && html`
        <${PassphraseModal}
          agentId=${passphrasePrompt.agentId}
          error=${passphrasePrompt.error}
          onSubmit=${handlePassphraseSubmit}
          onCancel=${() => setPassphrasePrompt(null)}
        />
      `}
    </div>
  `;
}

// ── Mount ────────────────────────────────────────────────────

render(html`<${App} />`, document.getElementById('app'));
