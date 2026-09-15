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

async function fetchOptimizerStats() {
  const res = await fetch('/api/optimizer/stats');
  if (!res.ok) throw new Error(`Failed to fetch optimizer stats: ${res.status}`);
  return res.json();
}

async function fetchOptimizerSavings() {
  const res = await fetch('/api/optimizer/savings');
  if (!res.ok) throw new Error(`Failed to fetch optimizer savings: ${res.status}`);
  return res.json();
}

async function fetchOptimizerMemory() {
  const res = await fetch('/api/optimizer/memory');
  if (!res.ok) throw new Error(`Failed to fetch optimizer memory: ${res.status}`);
  return res.json();
}

async function fetchOptimizerDaemon() {
  const res = await fetch('/api/optimizer/daemon');
  if (!res.ok) throw new Error(`Failed to fetch optimizer daemon: ${res.status}`);
  return res.json();
}

async function controlOptimizerDaemon(action) {
  const res = await fetch('/api/optimizer/daemon/' + action, { method: 'POST' });
  if (!res.ok) throw new Error(`optimizer ${action} failed: ${res.status}`);
  return res.json();
}

// List input prices (USD per 1M tokens) for a rough dollar estimate in the UI.
// The `forge optimizer savings` CLI supports negotiated-rate overrides; this
// dashboard estimate uses list prices only.
const OPTIMIZER_LIST_INPUT_PRICE = {
  'claude-fable-5': 10, 'claude-opus-4-8': 5, 'claude-opus-4-7': 5, 'claude-opus-4-6': 5,
  'claude-opus-4-5': 5, 'claude-sonnet-5': 3, 'claude-sonnet-4-6': 3, 'claude-haiku-4-5': 1,
};
function optimizerInputPrice(model) {
  if (!model) return 3;
  for (const k of Object.keys(OPTIMIZER_LIST_INPUT_PRICE)) {
    if (model.startsWith(k)) return OPTIMIZER_LIST_INPUT_PRICE[k];
  }
  return 3; // unknown-model fallback
}
function optimizerCommas(n) {
  return (n || 0).toLocaleString('en-US');
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

// ── Skill Builder API Helpers ─────────────────────────────────

async function fetchSkillBuilderProvider(agentId) {
  const res = await fetch(`/api/agents/${agentId}/skill-builder/provider`);
  if (!res.ok) throw new Error(`Failed to fetch provider: ${res.status}`);
  return res.json();
}

async function streamSkillBuilderChat(agentId, messages, { onProgress, onMessage, onSkillDraft, onError, onDone, signal, mode, editingName }) {
  const body = { messages };
  if (mode === 'edit' && editingName) {
    body.mode = 'edit';
    body.editing_name = editingName;
  }
  const res = await fetch(`/api/agents/${agentId}/skill-builder/chat`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal,
  });

  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `Chat failed: ${res.status}`);
  }

  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });

    // Parse SSE frames
    const lines = buffer.split('\n');
    buffer = lines.pop() || '';

    let eventType = '';
    for (const line of lines) {
      if (line.startsWith('event: ')) {
        eventType = line.slice(7).trim();
      } else if (line.startsWith('data: ')) {
        const data = line.slice(6);
        try {
          const parsed = JSON.parse(data);
          // #252 part 2: the builder now returns a structured {message, skill}
          // envelope. `progress` is a content-free keepalive during
          // generation; `message` carries the assistant's chat reply
          // (delivered once, at completion); `skill_draft` carries the draft.
          if (eventType === 'progress' && onProgress) onProgress();
          else if (eventType === 'message' && onMessage) onMessage(parsed.content || '');
          else if (eventType === 'skill_draft' && onSkillDraft) onSkillDraft(parsed);
          else if (eventType === 'error' && onError) onError(parsed.error || 'Unknown error');
          else if (eventType === 'done' && onDone) onDone();
        } catch { /* ignore parse errors */ }
        eventType = '';
      }
    }
  }
}

async function validateSkillBuilderMD(agentId, skillMD, scripts, { mode, editingName } = {}) {
  const body = { skill_md: skillMD, scripts };
  if (mode === 'edit' && editingName) {
    body.mode = 'edit';
    body.editing_name = editingName;
  }
  const res = await fetch(`/api/agents/${agentId}/skill-builder/validate`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`Validation failed: ${res.status}`);
  return res.json();
}

async function saveSkillBuilder(agentId, skillName, skillMD, scripts, envVars, { overwrite, editingName } = {}) {
  const body = { skill_name: skillName, skill_md: skillMD, scripts };
  if (envVars && Object.keys(envVars).length > 0) body.env_vars = envVars;
  if (overwrite) {
    body.overwrite = true;
    body.editing_name = editingName;
  }
  const res = await fetch(`/api/agents/${agentId}/skill-builder/save`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const data = await res.json();
  if (!res.ok) throw new Error(data.error || `Save failed: ${res.status}`);
  return data;
}

// Lists custom skills attached to the agent on disk (skills/<name>/SKILL.md
// or skills/<name>.md). Distinct from /api/skills which returns registry/
// embedded skills available to ADD. Issue #193.
async function fetchCustomSkills(agentId) {
  const res = await fetch(`/api/agents/${agentId}/skill-builder/skills`);
  if (!res.ok) throw new Error(`Failed to list custom skills: ${res.status}`);
  return res.json();
}

// Loads a single custom skill's SKILL.md + helper scripts so the Skill
// Builder can populate the editor for the edit flow. Issue #193.
async function loadCustomSkill(agentId, name) {
  const res = await fetch(`/api/agents/${agentId}/skill-builder/skills/${encodeURIComponent(name)}`);
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `Failed to load skill: ${res.status}`);
  }
  return res.json();
}

// ── SSE Hook ─────────────────────────────────────────────────

function useSSE(onEvent) {
  const callbackRef = useRef(onEvent);
  callbackRef.current = onEvent;

  useEffect(() => {
    const es = new EventSource('/api/events');

    // EventSource dispatches by `event:` name, so every type the server
    // broadcasts needs an explicit listener — an unlisted one is silently
    // dropped. agent_created (handlers_create.go) was being dropped, which
    // is why a newly created agent only appeared after a refresh or the
    // 60s poll. The type is forwarded so the caller can tell them apart.
    const forward = (type) => (e) => {
      try {
        callbackRef.current(type, JSON.parse(e.data));
      } catch { /* ignore parse errors */ }
    };

    es.addEventListener('agent_status', forward('agent_status'));
    es.addEventListener('agent_created', forward('agent_created'));

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
  if (path === 'optimizer') return { page: 'optimizer', params: {} };
  // #/skill-builder/{id}
  const sbMatch = path.match(/^skill-builder\/(.+)$/);
  if (sbMatch) return { page: 'skill-builder', params: { id: sbMatch[1] } };
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

  // Abort any in-flight stream on unmount. Switching agents remounts this
  // hook (ChatPage is keyed by agent id), so without this the previous
  // agent's request keeps streaming into a dead component.
  useEffect(() => () => {
    if (abortRef.current) abortRef.current.abort();
  }, []);

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
              // Update messages in real-time to show text as it arrives
              setMessages(prev => {
                const last = prev[prev.length - 1];
                if (last && last.role === 'agent' && last.isStreaming) {
                  const updated = [...prev];
                  updated[updated.length - 1] = { ...last, content: agentText, tools: [...currentTools] };
                  return updated;
                }
                return [...prev, { role: 'agent', content: agentText, tools: [...currentTools], isStreaming: true }];
              });
            } else if (eventType === 'progress') {
              // Tool execution progress. The agent carries the tool name and
              // phase in task metadata (progress_tool / progress_phase) and
              // the human-readable line as a text part — NOT as a data part.
              const meta = parsed.metadata || {};
              const toolName = meta.progress_tool || '';
              // Backend phases are "tool_start" / "tool_end"; ToolCard wants
              // the bare "start" / "end".
              const toolPhase = meta.progress_phase === 'tool_end' ? 'end'
                : meta.progress_phase === 'tool_start' ? 'start'
                : '';
              let toolMessage = '';
              const status = parsed.status || parsed;
              if (status.message && status.message.parts) {
                for (const part of status.message.parts) {
                  if (part.kind === 'text' && part.text) {
                    toolMessage = part.text;
                  }
                }
              }
              if (toolName) {
                // Replace this tool's pending start entry with the end entry;
                // tools execute sequentially so start/end never interleave.
                currentTools = [...currentTools.filter(t =>
                  !(t.name === toolName && t.phase === 'start')
                ), { name: toolName, phase: toolPhase, message: toolMessage }];
              }
              // Update messages in real-time to show tool progress
              setMessages(prev => {
                const last = prev[prev.length - 1];
                if (last && last.role === 'agent' && last.isStreaming) {
                  const updated = [...prev];
                  updated[updated.length - 1] = { ...last, content: agentText, tools: [...currentTools] };
                  return updated;
                }
                return [...prev, { role: 'agent', content: agentText, tools: [...currentTools], isStreaming: true }];
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
              // Show final text immediately
              setMessages(prev => {
                const last = prev[prev.length - 1];
                if (last && last.role === 'agent' && last.isStreaming) {
                  const updated = [...prev];
                  updated[updated.length - 1] = { ...last, content: agentText, tools: [...currentTools] };
                  return updated;
                }
                return [...prev, { role: 'agent', content: agentText, tools: [...currentTools], isStreaming: true }];
              });
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

function AgentCard({ agent, onStart, onStop, onChannelsChanged }) {
  const isActive = agent.status === 'running' || agent.status === 'starting';
  const isBusy = agent.status === 'starting' || agent.status === 'stopping';
  // Optimistic chip state — flipped immediately on click so the UI
  // reflects intent before /api/user-policy responds. Cleared back to
  // the authoritative agent.denied_channels on every render.
  const [pendingChannel, setPendingChannel] = useState(null);

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
            <span class="channel-chip-group">
              ${agent.channels.map(ch => {
                // denied_channels is the EFFECTIVE deny set across
                // system + user + workspace layers (server computes
                // it). Click toggles the user-layer entry in
                // ~/.forge/policy.yaml.
                const denied = new Set(agent.denied_channels || []);
                const lockedBy = agent.denied_channels_locked?.[ch];
                const isDenied = pendingChannel === ch ? !denied.has(ch) : denied.has(ch);
                const isSaving = pendingChannel === ch;
                const isLocked = !!lockedBy && lockedBy !== 'user';
                const title = isLocked
                  ? `Denied by ${lockedBy} policy (read-only — edit the policy file directly)`
                  : `Click to ${isDenied ? 'enable' : 'disable'} ${ch} (edits ~/.forge/policy.yaml)`;
                const flip = async (e) => {
                  e.stopPropagation();
                  if (isLocked || isSaving) return;
                  setPendingChannel(ch);
                  try {
                    const cur = await fetch('/api/user-policy').then(r => r.json());
                    const user = cur.user || {};
                    const userDenies = user.denied_channels || [];
                    const userDenied = userDenies.includes(ch);
                    const next = userDenied
                      ? userDenies.filter(c => c !== ch)
                      : [...userDenies, ch];
                    const updatedUser = { ...user, denied_channels: next };
                    const res = await fetch('/api/user-policy', {
                      method: 'PUT',
                      headers: { 'Content-Type': 'application/json' },
                      body: JSON.stringify({ user: updatedUser }),
                    });
                    if (!res.ok) throw new Error('PUT /api/user-policy failed');
                    if (onChannelsChanged) await onChannelsChanged();
                  } catch (err) {
                    console.error('channel toggle failed', err);
                  } finally {
                    setPendingChannel(null);
                  }
                };
                const klass = ['channel-chip',
                  isDenied ? 'disabled' : '',
                  isSaving ? 'saving' : '',
                  isLocked ? 'locked' : ''].filter(Boolean).join(' ');
                return html`
                  <span class=${klass} title=${title} onClick=${flip}>
                    ${ch}
                  </span>
                `;
              })}
            </span>
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

      ${!isActive && agent.channels?.length > 0 && (() => {
        // Pre-launch hint: what `forge serve start --with …` will
        // actually bring up (declared channels minus policy denies).
        // Single-line, ellipsis on overflow, full list in tooltip.
        const denied = new Set(agent.denied_channels || []);
        const effective = agent.channels.filter(c => !denied.has(c));
        const text = effective.length === 0
          ? 'no channels (all denied by policy)'
          : effective.join(', ');
        return html`
          <div class="agent-card-start-hint" title=${'starts with: ' + text}>
            <span class="start-hint-label">starts with</span>
            <span class="start-hint-list">${text}</span>
          </div>
        `;
      })()}

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
        <button class="btn btn-ghost btn-sm" onClick=${(e) => { e.stopPropagation(); navigate('skill-builder/' + agent.id); }}>
          Build Skill
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

function Sidebar({ agents, activeAgentId, activePage, version }) {
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
        <div class="sidebar-nav-item ${activePage === 'optimizer' ? 'active' : ''}" onClick=${() => navigate('optimizer')}>
          <span class="sidebar-nav-icon">\u26a1</span>
          Optimizer
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
      <div class="sidebar-footer">
        <span class="sidebar-footer-version">${version ? 'v' + version : ''}</span>
        <a class="sidebar-footer-link" href="https://useforge.ai" target="_blank" rel="noopener noreferrer">useforge.ai</a>
      </div>
    </aside>
  `;
}

function Dashboard({ agents, onStart, onStop, onRescan, onChannelsChanged, loading }) {
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
                onChannelsChanged=${onChannelsChanged}
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

const WIZARD_STEPS = ['Name', 'Provider', 'Model & Key', 'Channels', 'Tools', 'Skills', 'Fallback', 'Env Vars', 'Authentication', 'Review'];

// buildAuthPayload reshapes the wizard's form.auth into the JSON shape
// the backend (cmd/ui.go → scaffold) expects. The wizard collects
// `groups_claim` as a flat field for usability; we translate it into the
// nested `claim_map: {groups: <name>}` shape here. Empty optional fields
// are stripped so the generated forge.yaml stays clean.
// authReviewLabel renders the one-line summary shown on the Review step.
function authReviewLabel(auth) {
  const mode = auth?.mode || 'none';
  const s = auth?.settings || {};
  if (mode === 'none') return 'Anonymous (no auth)';
  if (mode === 'custom') return 'Custom (edit forge.yaml)';
  if (mode === 'oidc') {
    try {
      return s.issuer ? 'OIDC · ' + new URL(s.issuer).hostname : 'OIDC';
    } catch (e) {
      return 'OIDC';
    }
  }
  if (mode === 'http_verifier') {
    try {
      return s.url ? 'HTTP Verifier · ' + new URL(s.url).hostname : 'HTTP Verifier';
    } catch (e) {
      return 'HTTP Verifier';
    }
  }
  return mode;
}

function buildAuthPayload(auth) {
  const mode = auth?.mode || 'none';
  const src = auth?.settings || {};
  if (mode === 'none' || mode === 'custom') {
    return { mode, settings: {} };
  }
  const settings = {};
  if (mode === 'oidc') {
    const issuer = (src.issuer || '').replace(/\/+$/, '');
    if (issuer) settings.issuer = issuer;
    if (src.audience) settings.audience = src.audience;
    if (src.groups_claim && src.groups_claim !== 'groups') {
      settings.claim_map = { groups: src.groups_claim };
    }
  } else if (mode === 'http_verifier') {
    if (src.url) settings.url = src.url;
    if (src.default_org) settings.default_org = src.default_org;
  }
  return { mode, settings };
}

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
    organization_id: '', // OpenAI enterprise org ID
    web_search_provider: '', // "tavily" or "perplexity"
    channels: [], builtin_tools: [], skills: [],
    fallbacks: [], // [{provider, api_key}]
    passphrase: '',
    env_vars: {},
    // A2A server auth chain (PR6). The wizard's Authentication step
    // sets `mode` to one of {none, oidc, http_verifier, custom} and
    // populates `settings` for non-trivial modes.
    auth: { mode: 'none', settings: {} },
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
    // Authentication step: required fields must be filled when a
    // non-trivial mode is chosen. None / Custom always advance.
    if (step === 8) {
      const mode = form.auth?.mode || 'none';
      const s = form.auth?.settings || {};
      if (mode === 'oidc') return !!(s.issuer && s.audience);
      if (mode === 'http_verifier') return !!s.url;
      return true; // none / custom
    }
    return true;
  }, [step, form, oauthDone]);

  const handleCreate = useCallback(async () => {
    setCreating(true);
    setError(null);
    try {
      // Reshape the auth payload before submission so the backend gets
      // the typed settings the scaffold expects (e.g., claim_map nesting).
      const payload = { ...form, auth: buildAuthPayload(form.auth) };
      const result = await createAgent(payload);
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
                  value=${form.env_vars['OPENAI_BASE_URL'] || ''}
                  onInput=${(e) => updateEnvVar('OPENAI_BASE_URL', e.target.value)} />
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

            ${providerMeta?.supports_org_id && form.auth_method === 'apikey' && html`
              <div>
                <label style="font-size: 12px; color: var(--text-muted); display: block; margin-bottom: 4px;">
                  Organization ID (optional — enterprise only)
                </label>
                <input class="wizard-input" placeholder="org-xxxxxxxxxxxxxxxxxxxxxxxx" value=${form.organization_id}
                  onInput=${(e) => updateForm('organization_id', e.target.value)} />
                <div style="font-size: 11px; color: var(--text-muted); margin-top: 3px;">
                  Stored in .env as OPENAI_ORG_ID. Leave empty if not using an organization.
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
          'OPENAI_BASE_URL', 'WEB_SEARCH_PROVIDER',
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
      case 8: { // Authentication (A2A server auth chain — PR6)
        const authTypes = meta?.auth_provider_types || [];
        const mode = form.auth?.mode || 'none';
        const settings = form.auth?.settings || {};

        const setAuthMode = (newMode) => {
          // Switching modes always resets settings so stale fields
          // (e.g., OIDC issuer when user switches to HTTP Verifier)
          // don't leak into the submitted payload.
          updateForm('auth', { mode: newMode, settings: {} });
        };
        const setAuthSetting = (key, val) => {
          updateForm('auth', { mode, settings: { ...settings, [key]: val } });
        };

        return html`
          <div class="wizard-step">
            <div class="wizard-step-title">Authentication</div>
            <div class="wizard-step-desc">
              How should the agent's A2A server authenticate incoming requests? Default: anonymous.
            </div>

            <div class="wizard-radio-group" style="display: flex; flex-direction: column; gap: 8px;">
              ${authTypes.map(opt => html`
                <label class="wizard-radio" style="display: flex; align-items: flex-start; gap: 10px; padding: 10px; border: 1px solid var(--border-color); border-radius: 8px; cursor: pointer; ${mode === opt.type ? 'background: rgba(255,154,0,0.08); border-color: var(--accent);' : ''}">
                  <input type="radio" name="auth_mode" value=${opt.type}
                    checked=${mode === opt.type}
                    onChange=${() => setAuthMode(opt.type)}
                    style="margin-top: 3px;" />
                  <div style="flex: 1;">
                    <div style="font-weight: 600; font-size: 14px;">${opt.label}</div>
                    <div style="font-size: 12px; color: var(--text-muted); margin-top: 2px;">${opt.description}</div>
                  </div>
                </label>
              `)}
            </div>

            ${mode === 'oidc' && html`
              <div style="margin-top: 20px; padding: 16px; background: rgba(139, 148, 158, 0.06); border: 1px solid var(--border-color); border-radius: 8px;">
                <div style="font-size: 12px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 10px;">
                  OIDC Configuration
                </div>
                <label style="font-size: 12px; color: var(--text-muted); display: block; margin-bottom: 4px;">
                  Issuer URL <span style="color: var(--accent);">*</span>
                </label>
                <input class="wizard-input" type="text" placeholder="https://login.example.com"
                  value=${settings.issuer || ''}
                  onInput=${(e) => setAuthSetting('issuer', e.target.value)} autocomplete="off" />

                <label style="font-size: 12px; color: var(--text-muted); display: block; margin: 12px 0 4px;">
                  Audience <span style="color: var(--accent);">*</span>
                </label>
                <input class="wizard-input" type="text" placeholder="api://forge"
                  value=${settings.audience || ''}
                  onInput=${(e) => setAuthSetting('audience', e.target.value)} autocomplete="off" />

                <label style="font-size: 12px; color: var(--text-muted); display: block; margin: 12px 0 4px;">
                  Groups claim (optional)
                </label>
                <input class="wizard-input" type="text" placeholder="groups"
                  value=${settings.groups_claim || ''}
                  onInput=${(e) => setAuthSetting('groups_claim', e.target.value)} autocomplete="off" />
                <div style="font-size: 11px; color: var(--text-muted); margin-top: 3px;">
                  Defaults to "groups". Set to e.g. "roles" if your IdP uses a different claim name.
                </div>
              </div>
            `}

            ${mode === 'http_verifier' && html`
              <div style="margin-top: 20px; padding: 16px; background: rgba(139, 148, 158, 0.06); border: 1px solid var(--border-color); border-radius: 8px;">
                <div style="font-size: 12px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 10px;">
                  HTTP Verifier Configuration
                </div>
                <label style="font-size: 12px; color: var(--text-muted); display: block; margin-bottom: 4px;">
                  Verifier URL <span style="color: var(--accent);">*</span>
                </label>
                <input class="wizard-input" type="text" placeholder="https://verify.example.com/verify"
                  value=${settings.url || ''}
                  onInput=${(e) => setAuthSetting('url', e.target.value)} autocomplete="off" />

                <label style="font-size: 12px; color: var(--text-muted); display: block; margin: 12px 0 4px;">
                  Default org_id (optional)
                </label>
                <input class="wizard-input" type="text" placeholder=""
                  value=${settings.default_org || ''}
                  onInput=${(e) => setAuthSetting('default_org', e.target.value)} autocomplete="off" />
              </div>
            `}

            ${mode === 'custom' && html`
              <div style="margin-top: 20px; padding: 16px; background: rgba(139, 148, 158, 0.06); border: 1px solid var(--border-color); border-radius: 8px; font-size: 12px; color: var(--text-muted);">
                A commented stub will be written to forge.yaml. Edit it after creation to plug in providers we don't expose in the wizard (e.g., static_token).
              </div>
            `}
          </div>
        `;
      }
      case 9: { // Review
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
            <div class="wizard-review-section">
              <div class="wizard-review-label">A2A Auth</div>
              <div class="wizard-review-value">${authReviewLabel(form.auth)}</div>
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

function OptimizerStatTile({ label, value, sub }) {
  return html`
    <div style="min-width:130px">
      <div style="font-size:22px;font-weight:600">${value}</div>
      <div style="opacity:.7;font-size:12px">${label}${sub ? ' · ' + sub : ''}</div>
    </div>`;
}

function optimizerBar(ratio) {
  let r = ratio || 0;
  if (r < 0) r = 0; if (r > 1) r = 1;
  const width = 15, filled = Math.round(r * width);
  return '█'.repeat(filled) + '░'.repeat(width - filled);
}

function OptimizerSavingsRow({ label, w }) {
  const saved = w ? w.saved_tokens : 0;
  const cache = w ? w.cache_tokens : 0;
  // Ratio = saved / cache tokens (billed cache read + write). Compression shrinks
  // the conversation history Claude Code caches, so this is the share of cached
  // token volume removed — an honest denominator from real billed counts. $
  // credits the cache-WRITE the dropped content avoided.
  const ratio = cache > 0 ? Math.min(saved / cache, 1) : 0;
  return html`
    <div style="font-family:monospace;font-size:13px;line-height:1.9">
      <span style="display:inline-block;width:110px">${label}</span>
      <span>${optimizerBar(ratio)}</span>
      <span style="display:inline-block;width:64px;text-align:right">${(ratio * 100).toFixed(1)}%</span>
      <span style="opacity:.8">  saved ${optimizerCommas(saved)} / ${optimizerCommas(cache)}</span>
      <span style="opacity:.55"> cache read+write</span>
      <span style="float:right">$${(w ? w.cost_avoided_usd : 0).toFixed(4)}</span>
    </div>`;
}

const OPTIMIZER_OUTCOME_MARK = { success: '✓', failure: '✗', abandoned: '∅' };
const OPTIMIZER_OUTCOME_COLOR = { success: '#3fb950', failure: '#f85149', abandoned: '#d29922' };

async function deleteOptimizerMemory(id) {
  const res = await fetch('/api/optimizer/memory?id=' + encodeURIComponent(id), { method: 'DELETE' });
  if (!res.ok) throw new Error(`delete failed: ${res.status}`);
  return res.json();
}

async function feedbackOptimizerMemory(id, signal) {
  const res = await fetch(`/api/optimizer/memory/feedback?id=${encodeURIComponent(id)}&signal=${signal}`, { method: 'POST' });
  if (!res.ok) throw new Error(`feedback failed: ${res.status}`);
  return res.json();
}

// Confidence bar: red below the 0.5 inject gate, amber mid, green high.
function OptimizerConfidenceBar({ value }) {
  const v = Math.max(0, Math.min(1, value || 0));
  const color = v < 0.5 ? '#f85149' : v < 0.7 ? '#d29922' : '#3fb950';
  const gated = v < 0.5;
  return html`<span title=${gated ? 'below 0.5 — not injected into new sessions' : 'injected into new sessions'}
    style="display:inline-flex;align-items:center;gap:6px">
    <span style="display:inline-block;width:46px;height:6px;border-radius:3px;background:rgba(128,128,128,.25);overflow:hidden">
      <span style="display:block;height:100%;width:${(v * 100).toFixed(0)}%;background:${color}"></span></span>
    <span style="font-size:12px;color:${color};font-weight:600">${(v * 100).toFixed(0)}%</span>
    ${gated ? html`<span style="font-size:11px;opacity:.6">gated</span>` : ''}
  </span>`;
}

function optimizerFmtTime(t) {
  return (t || '').replace('T', ' ').replace('Z', '');
}

function OptimizerOutcomeBadge({ outcome }) {
  const mark = OPTIMIZER_OUTCOME_MARK[outcome] || '·';
  const color = OPTIMIZER_OUTCOME_COLOR[outcome] || 'rgba(128,128,128,.8)';
  return html`<span style="display:inline-flex;align-items:center;gap:5px;font-size:12px;color:${color}">
    <span style="font-weight:700">${mark}</span>${outcome || 'unknown'}</span>`;
}

// Recall badge: how many later sessions this memory was injected into.
function OptimizerRecallBadge({ count }) {
  const n = count || 0;
  if (n === 0) return html`<span style="opacity:.4">—</span>`;
  return html`<span title="Injected into ${n} later session${n === 1 ? '' : 's'}"
    style="display:inline-flex;align-items:center;gap:4px;font-size:12px;color:var(--accent,#4a9eff);font-weight:600">
    ↩ ${n}</span>`;
}

// Entity chips: show a few, collapse the rest into a +N pill.
function OptimizerEntityChips({ items, max = 3 }) {
  const list = items || [];
  const shown = list.slice(0, max);
  const extra = list.length - shown.length;
  if (list.length === 0) return html`<span style="opacity:.4">—</span>`;
  return html`<span style="display:inline-flex;gap:4px;flex-wrap:wrap">
    ${shown.map(f => html`<code style="background:rgba(128,128,128,.15);padding:1px 6px;border-radius:4px;font-size:11px">${f}</code>`)}
    ${extra > 0 && html`<span style="opacity:.6;font-size:11px">+${extra}</span>`}
  </span>`;
}

function OptimizerDrawerRow({ label, children }) {
  return html`<div style="margin-bottom:14px">
    <div style="font-size:11px;text-transform:uppercase;letter-spacing:.05em;opacity:.55;margin-bottom:3px">${label}</div>
    <div style="line-height:1.5">${children}</div>
  </div>`;
}

// Right-side detail drawer for one episode.
function OptimizerMemoryDrawer({ e, onClose, onDelete, onFeedback }) {
  const [flash, setFlash] = useState(null);
  const [confirmDel, setConfirmDel] = useState(false);
  if (!e) return null;
  const isProc = e.kind === 'procedural';
  const conf = e.effective_confidence != null ? e.effective_confidence : e.confidence;
  const rate = (signal) => { setFlash(signal); onFeedback(e.id, signal); setTimeout(() => setFlash(null), 1200); };
  return html`
    <div onClick=${onClose} style="position:fixed;inset:0;background:rgba(0,0,0,.4);z-index:40"></div>
    <div style="position:fixed;top:0;right:0;bottom:0;width:min(560px,92vw);z-index:41;overflow-y:auto;
                background:var(--bg,#1b1b1d);border-left:1px solid rgba(128,128,128,.3);box-shadow:-8px 0 24px rgba(0,0,0,.3);padding:22px 24px">
      <div style="display:flex;align-items:flex-start;justify-content:space-between;gap:12px;margin-bottom:16px">
        <div>
          <div style="font-size:17px;font-weight:600;line-height:1.3">${e.task_signature || '(untitled)'}</div>
          <div style="margin-top:5px">${isProc
            ? html`<span style="font-size:12px;color:#a371f7;font-weight:600">⚙ procedure${e.episode_ids ? ' · from ' + e.episode_ids.length + ' episodes' : ''}</span>`
            : html`<${OptimizerOutcomeBadge} outcome=${e.outcome} />`}</div>
        </div>
        <button onClick=${onClose} title="Close"
          style="background:none;border:none;font-size:22px;cursor:pointer;opacity:.6;line-height:1">×</button>
      </div>

      <${OptimizerDrawerRow} label="Confidence">
        <div style="display:flex;align-items:center;gap:14px;flex-wrap:wrap">
          <${OptimizerConfidenceBar} value=${conf} />
          <span style="display:inline-flex;gap:6px;align-items:center">
            <button onClick=${() => rate('up')}
              style="background:${flash === 'up' ? 'rgba(63,185,80,.35)' : 'rgba(63,185,80,.12)'};color:#3fb950;border:1px solid rgba(63,185,80,.4);border-radius:6px;padding:4px 10px;cursor:pointer;font-size:12px;transition:background .15s">👍 boost</button>
            <button onClick=${() => rate('down')}
              style="background:${flash === 'down' ? 'rgba(248,81,73,.3)' : 'rgba(248,81,73,.1)'};color:#f85149;border:1px solid rgba(248,81,73,.35);border-radius:6px;padding:4px 10px;cursor:pointer;font-size:12px;transition:background .15s">👎 lower</button>
            ${flash && html`<span style="font-size:12px;color:${flash === 'up' ? '#3fb950' : '#f85149'}">${flash === 'up' ? 'boosted ↑' : 'lowered ↓'}</span>`}
          </span>
        </div>
        <div style="opacity:.55;font-size:11px;margin-top:5px">base ${((e.confidence || 0) * 100).toFixed(0)}% · adjusted by corroboration, post-recall outcomes & your feedback</div>
      <//>
      ${e.summary && html`<${OptimizerDrawerRow} label=${isProc ? 'When to use' : 'Summary'}>${e.summary}<//>`}
      ${e.lesson && html`<${OptimizerDrawerRow} label="Lesson">
        <span style="font-style:italic">💡 ${e.lesson}</span><//>`}
      ${e.actions && e.actions.length > 0 && html`<${OptimizerDrawerRow} label=${isProc ? 'Steps' : 'Actions'}>
        <ul style="margin:0;padding-left:18px">${e.actions.map(a => html`<li>${a}</li>`)}</ul><//>`}
      ${e.errors && e.errors.length > 0 && html`<${OptimizerDrawerRow} label=${isProc ? 'Pitfalls' : 'Errors'}>
        ${e.errors.map(x => html`<div style="color:${isProc ? '#d29922' : '#f85149'};font-size:12px">${x}</div>`)}<//>`}
      <${OptimizerDrawerRow} label="Entities (files)">
        <${OptimizerEntityChips} items=${e.entities && e.entities.length ? e.entities : e.files} max=${20} /><//>

      <div style="display:grid;grid-template-columns:1fr 1fr;gap:12px 20px;margin-top:6px;font-size:13px">
        <${OptimizerDrawerRow} label="Kind">${e.kind || 'episodic'}<//>
        <${OptimizerDrawerRow} label="Scope">${e.about || ('resource:repo:' + (e.repo || ''))}<//>
        <${OptimizerDrawerRow} label="Repo @ commit">
          <code>${e.repo || '—'}${e.code_state && e.code_state.commit ? '@' + e.code_state.commit : ''}</code><//>
        <${OptimizerDrawerRow} label="Model">${(e.model || '—').replace('claude-', '')}<//>
        <${OptimizerDrawerRow} label="Confidence">${e.confidence != null ? e.confidence.toFixed(2) : '—'}<//>
        <${OptimizerDrawerRow} label="Source">${e.source_kind || '—'}${e.binding ? ' · ' + e.binding : ''}<//>
        <${OptimizerDrawerRow} label="Session">${e.session_id || '—'}<//>
        <${OptimizerDrawerRow} label="Created">${optimizerFmtTime(e.created_at)}<//>
        <${OptimizerDrawerRow} label="Recalled">
          ${(e.recall_count || 0) === 0
            ? html`<span style="opacity:.6">not yet injected into a later session</span>`
            : html`injected into <b>${e.recall_count}</b> later session${e.recall_count === 1 ? '' : 's'}${e.last_recalled ? ' · last ' + optimizerFmtTime(e.last_recalled) : ''}`}<//>
      </div>

      <div style="margin-top:18px;border-top:1px solid rgba(128,128,128,.2);padding-top:16px">
        ${confirmDel
          ? html`<span style="display:inline-flex;align-items:center;gap:10px">
              <span style="font-size:13px;opacity:.8">Delete this memory permanently?</span>
              <button onClick=${() => onDelete(e.id)}
                style="background:#f85149;color:#fff;border:none;border-radius:6px;padding:7px 14px;cursor:pointer;font-size:13px">Delete</button>
              <button onClick=${() => setConfirmDel(false)}
                style="background:none;border:1px solid rgba(128,128,128,.4);border-radius:6px;padding:7px 14px;cursor:pointer;font-size:13px">Cancel</button>
            </span>`
          : html`<button onClick=${() => setConfirmDel(true)}
              style="background:rgba(248,81,73,.12);color:#f85149;border:1px solid rgba(248,81,73,.4);
                     border-radius:6px;padding:7px 14px;cursor:pointer;font-size:13px">🗑 Delete this memory</button>`}
      </div>
    </div>`;
}

// Per-row actions: spaced 👍/👎 with click acknowledgment (a brief colored
// flash) and a two-step inline delete confirm (no blocking browser dialog).
function OptimizerRowActions({ id, onFeedback, onDelete }) {
  const [flash, setFlash] = useState(null);     // 'up' | 'down' | null
  const [confirming, setConfirming] = useState(false);

  const act = (ev, signal) => {
    ev.stopPropagation();
    setFlash(signal);
    onFeedback(id, signal);
    setTimeout(() => setFlash(null), 1000);
  };
  const askDelete = (ev) => { ev.stopPropagation(); setConfirming(true); setTimeout(() => setConfirming(false), 4000); };

  const iconBtn = (label, title, onClick, bg) => html`<button title=${title} onClick=${onClick}
    style="background:${bg || 'transparent'};border:none;cursor:pointer;font-size:14px;line-height:1;
           padding:4px 6px;border-radius:6px;transition:background .15s">${label}</button>`;

  if (confirming) {
    return html`<span onClick=${ev => ev.stopPropagation()}
      style="display:inline-flex;align-items:center;gap:6px;white-space:nowrap">
      <span style="font-size:11px;opacity:.75">Delete?</span>
      ${iconBtn('✓', 'Confirm delete', ev => { ev.stopPropagation(); onDelete(id); }, 'rgba(248,81,73,.18)')}
      ${iconBtn('✕', 'Cancel', ev => { ev.stopPropagation(); setConfirming(false); }, 'rgba(128,128,128,.15)')}
    </span>`;
  }
  return html`<span style="display:inline-flex;align-items:center;gap:8px;white-space:nowrap">
    ${iconBtn('👍', 'Boost confidence', ev => act(ev, 'up'), flash === 'up' ? 'rgba(63,185,80,.3)' : 'transparent')}
    ${iconBtn('👎', 'Lower confidence', ev => act(ev, 'down'), flash === 'down' ? 'rgba(248,81,73,.3)' : 'transparent')}
    <span style="width:1px;height:16px;background:rgba(128,128,128,.3)"></span>
    ${iconBtn('🗑', 'Delete', askDelete, 'transparent')}
  </span>`;
}

// Memory sub-tab: a scannable table; click a row for the drawer.
function OptimizerMemoryTab({ memory, onSelect, onDelete, onFeedback }) {
  const all = (memory && memory.episodes) || [];
  const [kindFilter, setKindFilter] = useState('all');
  const procCount = all.filter(e => e.kind === 'procedural').length;
  const epiCount = all.length - procCount;
  const eps = all.filter(e =>
    kindFilter === 'all' ? true :
    kindFilter === 'procedures' ? e.kind === 'procedural' :
    e.kind !== 'procedural');

  const filterBtn = (id, label, n) => {
    const on = kindFilter === id;
    return html`<button onClick=${() => setKindFilter(id)}
      style="background:${on ? 'var(--accent,#4a9eff)' : 'transparent'};
             color:${on ? '#fff' : 'inherit'};
             border:1px solid ${on ? 'var(--accent,#4a9eff)' : 'rgba(128,128,128,.5)'};
             border-radius:6px;padding:5px 12px;margin-right:8px;cursor:pointer;font-size:12px;
             font-weight:${on ? 600 : 500}">
      ${label} <span style="opacity:${on ? 0.85 : 0.55}">${n}</span></button>`;
  };

  return html`
    <div style="padding:0 24px 32px">
      ${all.length === 0 && html`
        <div style="padding:24px 0;opacity:.65;line-height:1.7">
          No memory yet — run <code>forge optimizer claude</code> (memory is on by default) and complete a few tasks.
          Episodes are distilled at task boundaries; procedures are consolidated once a repo has enough related episodes.
        </div>`}
      ${all.length > 0 && html`
        <div style="display:flex;align-items:center;justify-content:space-between;margin:6px 0 12px;flex-wrap:wrap;gap:8px">
          <div>
            ${filterBtn('all', 'All', all.length)}
            ${filterBtn('episodes', 'Episodes', epiCount)}
            ${filterBtn('procedures', '⚙ Procedures', procCount)}
          </div>
          <div class="skills-subtitle" style="margin:0">${memory.recalled_count || 0} recalled into later sessions</div>
        </div>
        <table style="width:100%;border-collapse:collapse;font-size:13px">
          <thead><tr style="text-align:left;opacity:.7">
            <th style="padding:8px 8px">Title</th><th style="width:110px">Outcome</th>
            <th style="width:130px">Confidence</th><th>Entities</th><th style="width:90px">Recalled</th>
            <th style="width:120px">When</th><th style="width:130px;text-align:right">Actions</th>
          </tr></thead>
          <tbody>
            ${eps.map(e => html`
              <tr style="border-top:1px solid rgba(128,128,128,.2);cursor:pointer"
                  onClick=${() => onSelect(e)}
                  onMouseOver=${ev => ev.currentTarget.style.background = 'rgba(128,128,128,.08)'}
                  onMouseOut=${ev => ev.currentTarget.style.background = 'transparent'}>
                <td style="padding:9px 8px">
                  <div style="font-weight:500">
                    ${e.kind === 'procedural' ? html`<span style="color:#a371f7">⚙ </span>` : ''}${e.task_signature || '(untitled)'}
                  </div>
                  ${e.lesson && html`<div style="opacity:.6;font-size:12px;margin-top:2px">💡 ${e.lesson}</div>`}
                </td>
                <td>${e.kind === 'procedural'
                  ? html`<span style="font-size:12px;color:#a371f7;font-weight:600">procedure</span>`
                  : html`<${OptimizerOutcomeBadge} outcome=${e.outcome} />`}</td>
                <td><${OptimizerConfidenceBar} value=${e.effective_confidence != null ? e.effective_confidence : e.confidence} /></td>
                <td><${OptimizerEntityChips} items=${e.entities && e.entities.length ? e.entities : e.files} /></td>
                <td><${OptimizerRecallBadge} count=${e.recall_count} /></td>
                <td style="opacity:.7">${optimizerFmtTime(e.created_at)}</td>
                <td style="text-align:right">
                  <${OptimizerRowActions} id=${e.id} onFeedback=${onFeedback} onDelete=${onDelete} />
                </td>
              </tr>`)}
          </tbody>
        </table>
        <div style="opacity:.55;font-size:12px;margin-top:14px">
          Stored locally at <code>${memory.store_path || '.forge/optimizer-memory.jsonl'}</code> ·
          record shape aligns with the platform memory schema (kind / about / entities).
          When a control-plane URL is configured, memory also flows there for cross-session and org learning.
        </div>`}
    </div>`;
}

// Savings sub-tab: the live proxy stats + durable usage-log rollups.
function OptimizerSavingsTab({ data, loading, stats, totals, dollars, sessionRows, savings }) {
  return html`
    <div>
      ${loading && !data && html`<div style="padding:24px;opacity:.7">Loading…</div>`}
      ${data && !data.available && html`
        <div style="padding:24px 0;line-height:1.7">
          <p>No optimizer running at <code>${data.base_url || 'http://127.0.0.1:8787'}</code>.</p>
          <p>Start it and route a coding agent through it:</p>
          <pre style="background:rgba(0,0,0,.25);padding:12px;border-radius:8px;overflow:auto">forge optimizer claude      # launches Claude Code through it
# or: forge optimizer --compress   # standalone proxy</pre>
          <p style="opacity:.7">If it listens on another address, set <code>FORGE_OPTIMIZER_URL</code>.</p>
        </div>`}
      ${stats && html`
        <div style="padding-bottom:24px">
          <div style="display:flex;gap:28px;flex-wrap:wrap;margin:8px 0 22px">
            <${OptimizerStatTile} label="Requests" value=${optimizerCommas(totals.requests)} />
            <${OptimizerStatTile} label="Input tokens" value=${optimizerCommas(totals.input_tokens)} />
            <${OptimizerStatTile} label="Cache read" value=${optimizerCommas(totals.cache_read_input_tokens)} sub="billed ~0.1×" />
            <${OptimizerStatTile} label="Output tokens" value=${optimizerCommas(totals.output_tokens)} />
            <${OptimizerStatTile} label="Tokens saved" value=${optimizerCommas(totals.compression_saved_tokens)} sub="vs. uncompressed" />
            <${OptimizerStatTile} label="Cost avoided" value=${'$' + dollars.toFixed(4)} sub="list price" />
            <${OptimizerStatTile} label="Expansions" value=${optimizerCommas(totals.expansions)} />
          </div>
          <div class="skills-subtitle" style="margin-bottom:8px">Live sessions (${sessionRows.length})</div>
          <table style="width:100%;border-collapse:collapse;font-size:13px">
            <thead><tr style="text-align:left;opacity:.7">
              <th style="padding:6px 8px">Session</th><th>Reqs</th><th>Input</th><th>Output</th><th>Cache read</th><th>Saved</th><th>Expand</th><th>Last seen</th>
            </tr></thead>
            <tbody>
              ${sessionRows.map(([id, sd]) => html`
                <tr style="border-top:1px solid rgba(128,128,128,.2)">
                  <td style="padding:6px 8px;font-family:monospace">${id}</td>
                  <td>${optimizerCommas(sd.requests)}</td>
                  <td>${optimizerCommas(sd.input_tokens)}</td>
                  <td>${optimizerCommas(sd.output_tokens)}</td>
                  <td>${optimizerCommas(sd.cache_read_input_tokens)}</td>
                  <td>${optimizerCommas(sd.compression_saved_tokens)}</td>
                  <td>${optimizerCommas(sd.expansions)}</td>
                  <td style="opacity:.7">${optimizerFmtTime(sd.last_seen)}</td>
                </tr>`)}
              ${sessionRows.length === 0 && html`<tr><td colspan="8" style="padding:12px 8px;opacity:.6">No sessions yet — use a coding agent through the optimizer.</td></tr>`}
            </tbody>
          </table>
          <div style="opacity:.55;font-size:12px;margin-top:14px">Live totals reset when the optimizer restarts. Durable history is below.</div>
        </div>`}

      ${savings && savings.report && savings.report.records > 0 && html`
        <div style="padding-bottom:28px">
          <div class="skills-subtitle" style="margin:6px 0 10px">Savings over time · from usage log</div>
          <${OptimizerSavingsRow} label="Today" w=${savings.report.today} />
          <${OptimizerSavingsRow} label="Last 7 days" w=${savings.report.last_7_days} />
          <${OptimizerSavingsRow} label="Last 30 days" w=${savings.report.last_30_days} />

          <div class="skills-subtitle" style="margin:20px 0 8px">Previous sessions (${savings.report.sessions.length})</div>
          <table style="width:100%;border-collapse:collapse;font-size:13px">
            <thead><tr style="text-align:left;opacity:.7">
              <th style="padding:6px 8px">Session</th><th>Client</th><th>Model</th><th>Reqs</th><th>Input</th><th>Output</th><th>Saved</th><th>$ avoided</th><th>Last seen</th>
            </tr></thead>
            <tbody>
              ${savings.report.sessions.map(s => html`
                <tr style="border-top:1px solid rgba(128,128,128,.2)">
                  <td style="padding:6px 8px;font-family:monospace">${s.session_id}</td>
                  <td>${s.client || '—'}</td>
                  <td style="opacity:.8">${(s.model || '—').replace('claude-', '')}</td>
                  <td>${optimizerCommas(s.requests)}</td>
                  <td>${optimizerCommas(s.input_tokens)}</td>
                  <td>${optimizerCommas(s.output_tokens)}</td>
                  <td>${optimizerCommas(s.saved_tokens)}</td>
                  <td>$${(s.cost_avoided_usd || 0).toFixed(4)}</td>
                  <td style="opacity:.7">${optimizerFmtTime(s.last_seen)}</td>
                </tr>`)}
            </tbody>
          </table>
          <div style="opacity:.55;font-size:12px;margin-top:12px">Reading <code>${savings.log_path || '.forge/optimizer-usage.jsonl'}</code>. Dollars use ${' '}
            <code>.forge/optimizer-pricing.json</code> if present, else list prices.</div>
        </div>`}
      ${data && data.available && savings && savings.report && savings.report.records === 0 && html`
        <div style="padding:0 0 24px;opacity:.6">No previous sessions in the usage log yet.</div>`}
    </div>`;
}

function OptimizerTab({ id, active, label, count, onClick }) {
  return html`<button onClick=${() => onClick(id)}
    style="background:none;border:none;cursor:pointer;padding:10px 4px;margin-right:22px;font-size:14px;
           color:${active ? 'inherit' : 'rgba(128,128,128,.85)'};font-weight:${active ? 600 : 400};
           border-bottom:2px solid ${active ? 'var(--accent,#4a9eff)' : 'transparent'}">
    ${label}${count != null ? html` <span style="opacity:.6">(${count})</span>` : ''}</button>`;
}

// Daemon status + Start/Stop control for the background optimizer.
function OptimizerDaemonBanner({ daemon, busy, onStart, onStop }) {
  const running = !!(daemon && daemon.running);
  const notWired = running && daemon.wired_base_url === '';
  const btn = (label, onClick, bg, border) => html`<button disabled=${busy} onClick=${onClick}
    style="background:${bg};color:#fff;border:1px solid ${border};border-radius:6px;padding:6px 14px;
           cursor:${busy ? 'default' : 'pointer'};font-size:13px;opacity:${busy ? 0.6 : 1}">
    ${busy ? '…' : label}</button>`;
  return html`
    <div style="display:flex;align-items:center;justify-content:space-between;gap:12px;padding:10px 24px 12px">
      <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
        <span style="width:9px;height:9px;border-radius:50%;background:${running ? '#3fb950' : '#8b949e'}"></span>
        <span style="font-weight:500">${running ? 'Optimizer running' : 'Optimizer not running'}</span>
        ${running && daemon.base_url && html`<code style="opacity:.6;font-size:12px">${daemon.base_url}</code>`}
        ${notWired && html`<span style="font-size:12px;color:#d29922">· Claude Code not wired (start to route it through)</span>`}
        ${daemon && daemon.managed && running && html`<span style="font-size:12px;opacity:.5">· managed</span>`}
      </div>
      ${running
        ? btn('Stop optimizer', onStop, 'rgba(248,81,73,.85)', 'rgba(248,81,73,.6)')
        : btn('Start optimizer', onStart, 'rgba(63,185,80,.85)', 'rgba(63,185,80,.6)')}
    </div>`;
}

function OptimizerPage() {
  const [data, setData] = useState(null);
  const [savings, setSavings] = useState(null);
  const [memory, setMemory] = useState(null);
  const [loading, setLoading] = useState(true);
  const [tab, setTab] = useState('savings');
  const [selected, setSelected] = useState(null);
  const [daemon, setDaemon] = useState(null);
  const [daemonBusy, setDaemonBusy] = useState(false);

  const loadMemory = useCallback(() => {
    return fetchOptimizerMemory().then(m => setMemory(m)).catch(() => setMemory({ available: false }));
  }, []);

  useEffect(() => {
    let alive = true;
    const load = () => {
      fetchOptimizerStats()
        .then(d => { if (alive) setData(d); })
        .catch(() => { if (alive) setData({ available: false }); })
        .finally(() => { if (alive) setLoading(false); });
      fetchOptimizerSavings()
        .then(s => { if (alive) setSavings(s); })
        .catch(() => { if (alive) setSavings({ available: false }); });
      fetchOptimizerMemory()
        .then(m => { if (alive) setMemory(m); })
        .catch(() => { if (alive) setMemory({ available: false }); });
      fetchOptimizerDaemon()
        .then(d => { if (alive) setDaemon(d); })
        .catch(() => { if (alive) setDaemon(null); });
    };
    load();
    const t = setInterval(load, 5000);
    return () => { alive = false; clearInterval(t); };
  }, []);

  const handleDaemon = useCallback(async (action) => {
    setDaemonBusy(true);
    try { await controlOptimizerDaemon(action); } catch (e) { /* best-effort */ }
    // start/stop take a moment (settings merge/revert, process up/down).
    await new Promise(r => setTimeout(r, 700));
    fetchOptimizerDaemon().then(setDaemon).catch(() => {});
    fetchOptimizerStats().then(setData).catch(() => {});
    setDaemonBusy(false);
  }, []);

  const handleDelete = useCallback(async (id) => {
    try { await deleteOptimizerMemory(id); } catch (e) { /* best-effort */ }
    await loadMemory();
    setSelected(s => (s && s.id === id ? null : s));
  }, [loadMemory]);

  const handleFeedback = useCallback(async (id, signal) => {
    try { await feedbackOptimizerMemory(id, signal); } catch (e) { /* best-effort */ }
    const m = await fetchOptimizerMemory().catch(() => null);
    if (m) {
      setMemory(m);
      setSelected(s => (s ? (m.episodes || []).find(e => e.id === s.id) || s : s));
    }
  }, []);

  const stats = data && data.stats ? data.stats : null;
  const totals = stats ? stats.totals : null;
  const sessions = stats && stats.sessions ? stats.sessions : {};
  const perModel = stats && stats.per_model ? stats.per_model : {};

  let dollars = 0;
  for (const [m, t] of Object.entries(perModel)) {
    dollars += (t.compression_saved_tokens || 0) * optimizerInputPrice(m) / 1e6;
  }
  const sessionRows = Object.entries(sessions)
    .sort((a, b) => (b[1].last_seen || '').localeCompare(a[1].last_seen || ''));
  const memCount = (memory && memory.count) || 0;

  return html`
    <main class="main skills-layout">
      <div class="skills-header">
        <div>
          <div class="skills-title">Coding Agent Sessions</div>
          <div class="skills-subtitle">Token usage, compression savings & learned memory via the forge optimizer</div>
        </div>
      </div>

      <${OptimizerDaemonBanner} daemon=${daemon} busy=${daemonBusy}
        onStart=${() => handleDaemon('start')} onStop=${() => handleDaemon('stop')} />

      <div style="padding:0 24px;border-bottom:1px solid rgba(128,128,128,.2);margin-bottom:18px">
        <${OptimizerTab} id="savings" label="Savings" active=${tab === 'savings'} onClick=${setTab} />
        <${OptimizerTab} id="memory" label="Memory" count=${memCount} active=${tab === 'memory'} onClick=${setTab} />
      </div>

      <div style="padding:0 24px">
        ${tab === 'savings' && html`<${OptimizerSavingsTab}
          data=${data} loading=${loading} stats=${stats} totals=${totals}
          dollars=${dollars} sessionRows=${sessionRows} savings=${savings} />`}
        ${tab === 'memory' && html`<${OptimizerMemoryTab}
          memory=${memory} onSelect=${setSelected} onDelete=${handleDelete} onFeedback=${handleFeedback} />`}
      </div>

      ${selected && html`<${OptimizerMemoryDrawer} e=${selected} onClose=${() => setSelected(null)} onDelete=${handleDelete} onFeedback=${handleFeedback} />`}
    </main>`;
}


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

// ── Skill Builder Settings (issue #92) ───────────────────────

async function fetchSkillBuilderSettings() {
  const res = await fetch('/api/settings/skill-builder');
  if (!res.ok) throw new Error(`Failed to fetch settings: ${res.status}`);
  return res.json();
}

async function saveSkillBuilderSettings(body) {
  const res = await fetch('/api/settings/skill-builder', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    throw new Error(err.error || `Save failed: ${res.status}`);
  }
  return res.json();
}

// SkillBuilderSettingsModal lets the operator pick the workspace-
// level LLM the skill builder uses, decoupled from any agent's
// runtime LLM. Persists to <workspace>/.forge/ui.yaml via the
// PUT /api/settings/skill-builder endpoint.
function SkillBuilderSettingsModal({ initial, onClose, onSaved }) {
  const [form, setForm] = useState({
    provider: (initial && initial.provider) || 'openai',
    model: (initial && initial.model) || '',
    base_url: (initial && initial.base_url) || '',
    api_key_env: (initial && initial.api_key_env) || '',
  });
  // API key is intentionally a separate piece of state — never persisted
  // to ui.yaml, never echoed back from the server. Left blank on every
  // open of the modal so an existing key isn't shown (or shadowed by an
  // empty input). Submitting an empty api_key leaves the saved value
  // untouched; submit a new value to rotate.
  const [apiKey, setApiKey] = useState('');
  const [showKey, setShowKey] = useState(false);
  const [hasKey, setHasKey] = useState((initial && initial.has_key) || false);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState(null);

  // Reload from server when modal opens so we see the persisted state,
  // not the partially-fallback shape that fetchSkillBuilderProvider
  // returned (which folds in agent-fallback fields).
  useEffect(() => {
    fetchSkillBuilderSettings()
      .then((s) => {
        setForm({
          provider: s.provider || 'openai',
          model: s.model || '',
          base_url: s.base_url || '',
          api_key_env: s.api_key_env || '',
        });
        setHasKey(!!s.has_key);
      })
      .catch((e) => setErr(e.message));
  }, []);

  async function submit() {
    setSaving(true);
    setErr(null);
    try {
      // Build the request body. Include api_key only when the operator
      // typed one — empty string means "leave the existing key alone."
      const body = { ...form };
      if (apiKey) body.api_key = apiKey;
      const saved = await saveSkillBuilderSettings(body);
      onSaved && onSaved(saved);
    } catch (e) {
      setErr(e.message);
    } finally {
      setSaving(false);
    }
  }

  const update = (k, v) => setForm({ ...form, [k]: v });

  return html`
    <div class="modal-overlay" onClick=${onClose}>
      <div class="modal" onClick=${(e) => e.stopPropagation()} style="max-width: 540px;">
        <div class="modal-header">
          <h3>Skill Builder LLM</h3>
          <button class="btn btn-ghost btn-sm" onClick=${onClose}>✕</button>
        </div>
        <div class="modal-body">
          <p style="font-size: 12px; color: var(--text-muted); margin-bottom: 16px;">
            Workspace-level LLM used to generate skills. Independent of any
            specific agent's runtime LLM. Persisted to
            <code>&lt;workspace&gt;/.forge/ui.yaml</code>.
          </p>

          <label class="modal-label">Provider</label>
          <select class="wizard-input" value=${form.provider} onChange=${(e) => update('provider', e.target.value)}>
            <option value="openai">openai</option>
            <option value="anthropic">anthropic</option>
            <option value="gemini">gemini</option>
            <option value="ollama">ollama</option>
          </select>

          <label class="modal-label" style="margin-top: 12px;">Model</label>
          <input class="wizard-input" placeholder="gpt-4.1 / claude-opus-4 / etc."
            value=${form.model} onInput=${(e) => update('model', e.target.value)} />

          ${form.provider === 'openai' && html`
            <label class="modal-label" style="margin-top: 12px;">Base URL <span style="color: var(--text-muted);">(optional)</span></label>
            <input class="wizard-input" placeholder="https://api.openai.com/v1 (leave blank for default)"
              value=${form.base_url} onInput=${(e) => update('base_url', e.target.value)} />
            <div style="font-size: 11px; color: var(--text-muted); margin-top: 3px;">
              Set this for OpenAI-compatible endpoints (OpenRouter, vLLM, litellm).
            </div>
          `}

          <label class="modal-label" style="margin-top: 12px;">API key env var</label>
          <input class="wizard-input" placeholder=${`${form.provider.toUpperCase()}_API_KEY (default)`}
            value=${form.api_key_env} onInput=${(e) => update('api_key_env', e.target.value)} />
          <div style="font-size: 11px; color: var(--text-muted); margin-top: 3px;">
            Name of the environment variable the forge ui process reads for the API key.
            Leave blank to use the provider default.
          </div>

          ${form.provider !== 'ollama' && html`
            <label class="modal-label" style="margin-top: 12px;">
              API key ${hasKey ? html`<span style="color: var(--text-muted); font-weight: normal;">(saved; leave blank to keep)</span>` : ''}
            </label>
            <div style="display: flex; gap: 6px;">
              <input class="wizard-input" type=${showKey ? 'text' : 'password'} autocomplete="off"
                placeholder=${hasKey ? '••••••••' : 'paste API key value'}
                value=${apiKey} onInput=${(e) => setApiKey(e.target.value)} style="flex: 1;" />
              <button type="button" class="btn btn-ghost btn-sm" onClick=${() => setShowKey(!showKey)}>
                ${showKey ? 'Hide' : 'Show'}
              </button>
            </div>
            <div style="font-size: 11px; color: var(--text-muted); margin-top: 3px;">
              Stored at <code>&lt;workspace&gt;/.forge/.env</code> (mode 0600).
              An auto-generated <code>.forge/.gitignore</code> protects it from being committed.
            </div>
          `}

          ${err && html`<div class="modal-error">${err}</div>`}
        </div>
        <div class="modal-footer">
          <button class="btn btn-ghost" onClick=${onClose}>Cancel</button>
          <button class="btn btn-primary" onClick=${submit} disabled=${saving}>
            ${saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  `;
}

// ── Skill Builder Page ───────────────────────────────────────

function SkillBuilderPage({ agentId }) {
  const [provider, setProvider] = useState(null);
  const [messages, setMessages] = useState([]);
  const [input, setInput] = useState('');
  const [streaming, setStreaming] = useState(false);
  const [skillMD, setSkillMD] = useState('');
  const [scripts, setScripts] = useState({});
  const [activeTab, setActiveTab] = useState('skill.md');
  const [validation, setValidation] = useState(null);
  const [saveStatus, setSaveStatus] = useState(null);
  const [envInputs, setEnvInputs] = useState({});
  const [error, setError] = useState(null);
  const [showSettings, setShowSettings] = useState(false);
  // Edit-mode state (issue #193).
  //
  //   mode             — 'create' or 'edit'. Switches the system prompt
  //                      sent to the LLM and toggles the diff/save UX.
  //   editingSkillName — the on-disk name of the skill being edited;
  //                      preserved across save so the user can keep
  //                      iterating on the same skill.
  //   customSkills     — list shown in the skills panel for "Edit"
  //                      buttons; refetched after every save.
  //   originalSkillMD/originalScripts — baseline for the diff modal so
  //                      the diff is always editor-state-vs-disk, not
  //                      against the previous unsaved edit.
  //   showDiff         — controls the Monaco diff preview modal.
  //   restartPending   — controls the "restart to load changes" banner
  //                      shown after a successful edit-mode save.
  const [mode, setMode] = useState('create');
  const [editingSkillName, setEditingSkillName] = useState(null);
  const [customSkills, setCustomSkills] = useState([]);
  const [originalSkillMD, setOriginalSkillMD] = useState('');
  const [originalScripts, setOriginalScripts] = useState({});
  const [showDiff, setShowDiff] = useState(false);
  const [restartPending, setRestartPending] = useState(false);
  const [restarting, setRestarting] = useState(false);
  const abortRef = useRef(null);
  const chatEndRef = useRef(null);
  const editorRef = useRef(null);

  // Fetch provider on mount
  useEffect(() => {
    fetchSkillBuilderProvider(agentId)
      .then(setProvider)
      .catch(err => setError('Failed to load provider: ' + err.message));
  }, [agentId]);

  // Fetch custom skills attached to the agent for the edit picker
  // (issue #193). Refetched after every successful save so a newly
  // created skill appears immediately in the list.
  const refreshCustomSkills = useCallback(() => {
    fetchCustomSkills(agentId)
      .then(setCustomSkills)
      .catch(() => { /* non-fatal — the picker just stays empty */ });
  }, [agentId]);
  useEffect(() => { refreshCustomSkills(); }, [refreshCustomSkills]);

  // Auto-scroll chat
  useEffect(() => {
    if (chatEndRef.current) {
      chatEndRef.current.scrollIntoView({ behavior: 'smooth' });
    }
  }, [messages]);

  // Initialize Monaco for skill editor
  useEffect(() => {
    if (!editorRef.current) return;
    const container = editorRef.current;

    // Try to load Monaco, fallback to textarea
    const loadEditor = async () => {
      try {
        const monaco = await import('/monaco/editor.js');
        if (!container._monacoEditor) {
          container._monacoEditor = monaco.editor.create(container, {
            value: skillMD,
            language: 'markdown',
            theme: 'vs-dark',
            minimap: { enabled: false },
            automaticLayout: true,
            wordWrap: 'on',
            fontSize: 13,
            lineNumbers: 'on',
            scrollBeyondLastLine: false,
          });
          container._monacoEditor.onDidChangeModelContent(() => {
            const val = container._monacoEditor.getValue();
            setSkillMD(val);
          });
        }
      } catch {
        // Monaco not available — textarea fallback used
      }
    };
    loadEditor();

    return () => {
      if (container._monacoEditor) {
        container._monacoEditor.dispose();
        container._monacoEditor = null;
      }
    };
  }, []);

  // Update editor content when skillMD changes from AI draft
  useEffect(() => {
    if (editorRef.current && editorRef.current._monacoEditor) {
      const editor = editorRef.current._monacoEditor;
      if (editor.getValue() !== skillMD) {
        editor.setValue(skillMD);
      }
    }
  }, [skillMD]);

  const handleSend = useCallback(async () => {
    const text = input.trim();
    if (!text || streaming) return;

    const userMsg = { role: 'user', content: text };
    const newMessages = [...messages, userMsg];
    setMessages(newMessages);
    setInput('');
    setStreaming(true);
    setError(null);

    // Add placeholder assistant message. The response is a structured
    // {message, skill} envelope delivered at completion (not token-streamed),
    // so show a "designing" affordance until the message arrives.
    const assistantMsg = { role: 'assistant', content: '', pending: true };
    setMessages([...newMessages, assistantMsg]);

    const abort = new AbortController();
    abortRef.current = abort;

    try {
      await streamSkillBuilderChat(agentId, newMessages, {
        signal: abort.signal,
        mode,
        editingName: editingSkillName,
        onProgress() {
          // Content-free keepalive; the pending placeholder stays until the
          // message arrives. No per-token rendering (the stream is JSON).
        },
        onMessage(content) {
          assistantMsg.content = content;
          assistantMsg.pending = false;
          setMessages([...newMessages, { ...assistantMsg }]);
        },
        onSkillDraft(draft) {
          if (draft.skill_md) setSkillMD(draft.skill_md);
          if (draft.scripts) setScripts(draft.scripts || {});
          setValidation(null);
          setSaveStatus(null);
        },
        onError(errMsg) {
          setError(errMsg);
        },
        onDone() {
          // Clear pending so the "designing" placeholder doesn't spin forever
          // when the model returned only a skill (or nothing) with no message.
          if (assistantMsg.pending) {
            assistantMsg.pending = false;
            // Empty model response with no draft loaded: show a placeholder
            // rather than a blank bubble (#276 review).
            if (!assistantMsg.content) {
              assistantMsg.content = '_(no response)_';
            }
            setMessages([...newMessages, { ...assistantMsg }]);
          }
        },
      });
    } catch (err) {
      if (err.name !== 'AbortError') {
        setError(err.message);
      }
    } finally {
      setStreaming(false);
      abortRef.current = null;
    }
  }, [agentId, messages, input, streaming, mode, editingSkillName]);

  const handleValidate = useCallback(async () => {
    try {
      const result = await validateSkillBuilderMD(agentId, skillMD, scripts, {
        mode,
        editingName: editingSkillName,
      });
      setValidation(result);
    } catch (err) {
      setError('Validation failed: ' + err.message);
    }
  }, [agentId, skillMD, scripts, mode, editingSkillName]);

  const handleSave = useCallback(async () => {
    // Extract name from SKILL.md frontmatter
    const nameMatch = skillMD.match(/^name:\s*(.+)$/m);
    const skillName = nameMatch ? nameMatch[1].trim() : '';
    if (!skillName) {
      setError('Cannot save: no "name" field found in SKILL.md frontmatter');
      return;
    }
    // In edit mode the user can rename the skill (frontmatter
    // name ≠ editing name) — that's a breaking change for any
    // wired-up agent. We don't block it (the validator surfaces a
    // warning), but we DON'T overwrite the existing directory under
    // that case: overwrite is only valid when the names match.
    const overwrite = mode === 'edit' && editingSkillName && skillName === editingSkillName;

    try {
      const envVars = Object.keys(envInputs).length > 0 ? envInputs : undefined;
      const result = await saveSkillBuilder(agentId, skillName, skillMD, scripts, envVars, {
        overwrite,
        editingName: editingSkillName,
      });
      setSaveStatus(result);
      setEnvInputs({});
      setError(null);
      // After a successful save, baseline = saved state so the
      // diff resets, and surface the restart prompt for edit-mode
      // changes (the running agent's tool registry is still
      // pinned to its startup snapshot until restart).
      setOriginalSkillMD(skillMD);
      setOriginalScripts({ ...scripts });
      if (mode === 'edit') {
        setRestartPending(true);
      } else {
        // After a create, switch into edit mode for this skill so
        // the next iteration uses the edit-mode prompt.
        setMode('edit');
        setEditingSkillName(skillName);
      }
      refreshCustomSkills();
    } catch (err) {
      setError('Save failed: ' + err.message);
    }
  }, [agentId, skillMD, scripts, envInputs, mode, editingSkillName, refreshCustomSkills]);

  // handleEdit loads a custom skill from disk and primes the builder
  // for iteration. Edit mode is what makes the LLM use the
  // "## Edit Mode" prompt trailer and what flips the save path to
  // overwrite-in-place. Issue #193.
  const handleEdit = useCallback(async (name) => {
    try {
      const content = await loadCustomSkill(agentId, name);
      setSkillMD(content.skill_md || '');
      setScripts(content.scripts || {});
      setOriginalSkillMD(content.skill_md || '');
      setOriginalScripts(content.scripts || {});
      setMode('edit');
      setEditingSkillName(name);
      setActiveTab('skill.md');
      setValidation(null);
      setSaveStatus(null);
      setError(null);
      setRestartPending(false);
      setMessages([{
        role: 'assistant',
        content: `Loaded skill **${name}** for editing. The current SKILL.md and scripts are in the editor. Describe what you want to change.`,
      }]);
    } catch (err) {
      setError('Failed to load skill: ' + err.message);
    }
  }, [agentId]);

  // handleNewSkill resets state for a fresh create-mode session.
  // Used by the "New skill" button alongside the custom-skills list.
  const handleNewSkill = useCallback(() => {
    setMode('create');
    setEditingSkillName(null);
    setSkillMD('');
    setScripts({});
    setOriginalSkillMD('');
    setOriginalScripts({});
    setMessages([]);
    setValidation(null);
    setSaveStatus(null);
    setError(null);
    setRestartPending(false);
    setActiveTab('skill.md');
  }, []);

  // handleRestart triggers a stop + start on the agent process so
  // the runtime re-runs registerSkillTools and picks up the edited
  // SKILL.md / scripts. The dashboard already exposes these as
  // separate endpoints — see server.go:78-79. We do NOT silently
  // restart on save: the operator may be mid-task in a live chat
  // and an unsolicited restart would lose context.
  const handleRestart = useCallback(async () => {
    setRestarting(true);
    try {
      await fetch(`/api/agents/${agentId}/stop`, { method: 'POST' });
      // Small delay so the SSE state machine drains the stop event
      // before we issue start — otherwise the dashboard's status
      // chip flickers from running → stopped → starting → running
      // in a way that looks broken.
      await new Promise(r => setTimeout(r, 500));
      await fetch(`/api/agents/${agentId}/start`, { method: 'POST' });
      setRestartPending(false);
    } catch (err) {
      setError('Restart failed: ' + err.message);
    } finally {
      setRestarting(false);
    }
  }, [agentId]);

  const handleKeyDown = useCallback((e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  }, [handleSend]);

  const scriptTabs = Object.keys(scripts);

  return html`
    <main class="main skill-builder">
      <div class="skill-builder-left">
        <div class="sb-header">
          <h2>Skill Builder</h2>
          ${provider && html`
            <div class="provider-banner ${!provider.has_key || provider.source === 'unset' ? 'provider-banner-error' : ''}">
              ${provider.source === 'unset' ? html`
                <span>Workspace skill-builder LLM is not configured.</span>
                <button class="btn btn-link btn-sm" onClick=${() => setShowSettings(true)}>Configure</button>
              ` : html`
                <span>${provider.provider}/${provider.model}</span>
                ${provider.source === 'agent_fallback' && html`<span class="provider-warning" title=${provider.warning || ''}>using agent fallback (deprecated)</span>`}
                ${!provider.has_key && html`<span class="provider-warning">API key not configured (env: ${provider.api_key_env || 'unset'})</span>`}
                <button class="btn btn-link btn-sm" onClick=${() => setShowSettings(true)}>Settings</button>
              `}
            </div>
          `}
          ${showSettings && html`<${SkillBuilderSettingsModal} initial=${provider} onClose=${() => setShowSettings(false)} onSaved=${(updated) => { setProvider(updated); setShowSettings(false); }} />`}

          ${mode === 'edit' && editingSkillName && html`
            <div class="sb-mode-banner">
              <span class="sb-mode-label">Editing</span>
              <code>${editingSkillName}</code>
              <button class="btn btn-link btn-sm" onClick=${handleNewSkill}>New skill</button>
            </div>
          `}

          ${(customSkills.length > 0 || mode === 'edit') && html`
            <details class="sb-custom-skills" open=${mode === 'create'}>
              <summary>Skills attached to this agent (${customSkills.length})</summary>
              ${customSkills.length === 0 && html`
                <div class="sb-custom-skills-empty">No custom skills yet — create one below, or attach skills from the registry via the Skills tab.</div>
              `}
              ${customSkills.map(s => html`
                <div key=${s.path} class="sb-custom-skill-row">
                  <div class="sb-custom-skill-info">
                    <div class="sb-custom-skill-name">
                      <code>${s.name}</code>
                      ${editingSkillName === s.name && html`<span class="sb-pill sb-pill-active">editing</span>`}
                    </div>
                    <div class="sb-custom-skill-desc">${s.description || 'No description'}</div>
                  </div>
                  <button class="btn btn-ghost btn-sm"
                    onClick=${() => handleEdit(s.name)}
                    disabled=${streaming || editingSkillName === s.name}>
                    Edit
                  </button>
                </div>
              `)}
              ${mode === 'edit' && html`
                <div class="sb-custom-skills-footer">
                  <button class="btn btn-link btn-sm" onClick=${handleNewSkill}>+ New skill</button>
                </div>
              `}
            </details>
          `}
        </div>

        <div class="sb-messages">
          ${messages.length === 0 && mode === 'create' && html`
            <div class="sb-empty">
              <div class="sb-empty-title">Design a new skill</div>
              <div class="sb-empty-text">Describe the skill you want to create. The AI will generate a valid SKILL.md file.</div>
              <div class="sb-suggestions">
                <button class="btn btn-ghost btn-sm" onClick=${() => setInput('Create a weather lookup skill that uses curl to fetch weather data from wttr.in')}>
                  Weather skill
                </button>
                <button class="btn btn-ghost btn-sm" onClick=${() => setInput('Create a GitHub issue triage skill that uses the gh CLI')}>
                  GitHub triage
                </button>
                <button class="btn btn-ghost btn-sm" onClick=${() => setInput('Create a database health check skill for PostgreSQL using psql')}>
                  DB health check
                </button>
              </div>
            </div>
          `}
          ${messages.map((msg, i) => html`
            <div key=${i} class="sb-message sb-message-${msg.role}">
              ${msg.pending && !msg.content
                ? html`<div class="sb-message-content sb-message-pending"><em>Designing your skill…</em></div>`
                : html`<div class="sb-message-content" dangerouslySetInnerHTML=${{ __html: renderMarkdown(msg.content) }} />`}
            </div>
          `)}
          ${streaming && html`<div class="sb-typing"><span class="spinner" /> Generating...</div>`}
          <div ref=${chatEndRef} />
        </div>

        <div class="sb-input-area">
          <textarea
            class="sb-textarea"
            placeholder="Describe the skill you want to create..."
            value=${input}
            onInput=${(e) => setInput(e.target.value)}
            onKeyDown=${handleKeyDown}
            disabled=${streaming}
            rows="3"
          />
          <button
            class="btn btn-primary"
            onClick=${handleSend}
            disabled=${streaming || !input.trim()}
          >
            ${streaming ? 'Generating...' : 'Send'}
          </button>
        </div>
      </div>

      <div class="skill-builder-right">
        <div class="sb-tabs">
          <button
            class="sb-tab ${activeTab === 'skill.md' ? 'sb-tab-active' : ''}"
            onClick=${() => setActiveTab('skill.md')}
          >SKILL.md</button>
          ${scriptTabs.map(name => html`
            <button
              key=${name}
              class="sb-tab ${activeTab === name ? 'sb-tab-active' : ''}"
              onClick=${() => setActiveTab(name)}
            >${name}</button>
          `)}
        </div>

        <div class="sb-editor-container">
          ${!skillMD && !scriptTabs.length && html`
            <div class="sb-empty-editor">
              <div class="sb-empty-title">No artifacts yet</div>
              <div class="sb-empty-text">Send a message to generate a SKILL.md file. Artifacts will appear here.</div>
            </div>
          `}
          ${activeTab === 'skill.md' && skillMD && html`
            <div class="editor-container" ref=${editorRef} style="height: 100%;">
              <textarea
                class="sb-editor-textarea"
                value=${skillMD}
                onInput=${(e) => setSkillMD(e.target.value)}
                spellcheck="false"
              />
            </div>
          `}
          ${activeTab !== 'skill.md' && scripts[activeTab] && html`
            <textarea
              class="sb-editor-textarea"
              value=${scripts[activeTab]}
              onInput=${(e) => setScripts({ ...scripts, [activeTab]: e.target.value })}
              spellcheck="false"
            />
          `}
        </div>

        <div class="sb-actions">
          ${error && html`<div class="sb-error">${error}</div>`}

          ${validation && html`
            <div class="sb-validation">
              ${validation.valid && html`<span class="sb-valid">Valid</span>`}
              ${!validation.valid && validation.errors && validation.errors.map(e => html`
                <div class="sb-validation-error">${e.field}: ${e.message}</div>
              `)}
              ${validation.warnings && validation.warnings.map(w => html`
                <div class="sb-validation-warning">${w.field}: ${w.message}</div>
              `)}
            </div>
          `}

          ${saveStatus && html`
            <div class="sb-save-success">
              Saved to ${saveStatus.path}
              ${saveStatus.egress_added && saveStatus.egress_added.length > 0 && html`
                <div class="sb-save-detail">Egress domains added: ${saveStatus.egress_added.join(', ')}</div>
              `}
              ${saveStatus.env_configured && saveStatus.env_configured.length > 0 && html`
                <div class="sb-save-detail">Env vars configured: ${saveStatus.env_configured.join(', ')}</div>
              `}
            </div>
          `}
          ${saveStatus && saveStatus.env_missing && saveStatus.env_missing.filter(e => e.kind !== 'optional').length > 0 && html`
            <div class="sb-env-missing">
              <div class="sb-env-missing-title">Missing environment variables:</div>
              ${saveStatus.env_missing.filter(e => e.kind !== 'optional').map(entry => html`
                <div class="sb-env-input" key=${entry.name}>
                  <label>${entry.name} <span class="sb-env-kind">(${entry.kind})</span></label>
                  <input type="text" placeholder=${'Enter ' + entry.name}
                    value=${envInputs[entry.name] || ''}
                    onInput=${(e) => setEnvInputs(prev => ({...prev, [entry.name]: e.target.value}))} />
                </div>
              `)}
              <button class="btn btn-primary btn-sm"
                onClick=${handleSave}
                disabled=${Object.values(envInputs).every(v => !v)}>
                Save Env Vars
              </button>
            </div>
          `}

          ${restartPending && html`
            <div class="sb-restart-banner">
              <div>Skill saved. Restart the agent to load changes.</div>
              <div class="sb-restart-actions">
                <button class="btn btn-ghost btn-sm" onClick=${() => setRestartPending(false)} disabled=${restarting}>Later</button>
                <button class="btn btn-primary btn-sm" onClick=${handleRestart} disabled=${restarting}>
                  ${restarting ? 'Restarting…' : 'Restart agent'}
                </button>
              </div>
            </div>
          `}

          <div class="sb-action-buttons">
            <button class="btn btn-ghost btn-sm" onClick=${handleValidate} disabled=${!skillMD}>
              Revalidate
            </button>
            <button class="btn btn-ghost btn-sm" onClick=${() => { if (skillMD) { navigator.clipboard.writeText(skillMD); } }}>
              Copy
            </button>
            ${mode === 'edit' && html`
              <button class="btn btn-ghost btn-sm" onClick=${() => setShowDiff(true)} disabled=${!skillMD}>
                Preview changes
              </button>
            `}
            <button class="btn btn-primary btn-sm" onClick=${handleSave} disabled=${!skillMD}>
              ${mode === 'edit' ? 'Save changes' : 'Save & Attach'}
            </button>
          </div>
        </div>
      </div>
      ${showDiff && html`
        <${SkillDiffModal}
          originalSkillMD=${originalSkillMD}
          newSkillMD=${skillMD}
          originalScripts=${originalScripts}
          newScripts=${scripts}
          onClose=${() => setShowDiff(false)}
          onConfirm=${() => { setShowDiff(false); handleSave(); }}
        />
      `}
    </main>
  `;
}

// SkillDiffModal renders a Monaco side-by-side diff per artifact —
// SKILL.md plus every script that appears in either the on-disk or
// editor state. The diff is editor-state-vs-disk (issue #193): the
// originalSkillMD baseline is what was loaded at handleEdit time and
// is updated by each successful save so iteration always diffs from
// the most recent persisted state, not from initial load.
function SkillDiffModal({ originalSkillMD, newSkillMD, originalScripts, newScripts, onClose, onConfirm }) {
  const orig = originalScripts || {};
  const next = newScripts || {};
  const scriptNames = Array.from(new Set([...Object.keys(orig), ...Object.keys(next)])).sort();
  const tabs = ['skill.md', ...scriptNames];
  const [activeTab, setActiveTab] = useState('skill.md');
  const containerRef = useRef(null);
  const editorRef = useRef(null);

  // Mount + reuse a single Monaco diff editor across tabs; just swap
  // the model on tab-change. Avoids the cost of recreating the diff
  // engine per tab, which matters for skills with many scripts.
  useEffect(() => {
    if (!containerRef.current) return;
    const container = containerRef.current;
    let cancelled = false;
    const setup = async () => {
      try {
        const monaco = await import('/monaco/editor.js');
        if (cancelled) return;
        if (!editorRef.current) {
          editorRef.current = monaco.editor.createDiffEditor(container, {
            theme: 'vs-dark',
            automaticLayout: true,
            readOnly: true,
            renderSideBySide: true,
            minimap: { enabled: false },
            fontSize: 13,
          });
        }
        const language = activeTab === 'skill.md' ? 'markdown' : 'shell';
        const baseline = activeTab === 'skill.md' ? (originalSkillMD || '') : (orig[activeTab] || '');
        const draft = activeTab === 'skill.md' ? (newSkillMD || '') : (next[activeTab] || '');
        editorRef.current.setModel({
          original: monaco.editor.createModel(baseline, language),
          modified: monaco.editor.createModel(draft, language),
        });
      } catch (err) {
        console.error('Failed to mount Monaco diff editor:', err);
      }
    };
    setup();
    return () => {
      cancelled = true;
      if (editorRef.current) {
        const model = editorRef.current.getModel();
        if (model) {
          model.original.dispose();
          model.modified.dispose();
        }
      }
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeTab, originalSkillMD, newSkillMD]);

  // Tear down the diff editor on unmount so the next open is a
  // fresh mount — the model swap above handles tab changes but
  // leaving the editor instance behind across modal opens leaks
  // Monaco textures.
  useEffect(() => () => {
    if (editorRef.current) {
      editorRef.current.dispose();
      editorRef.current = null;
    }
  }, []);

  return html`
    <div class="modal-backdrop sb-diff-backdrop" onClick=${onClose}>
      <div class="modal sb-diff-modal" onClick=${(e) => e.stopPropagation()}>
        <div class="modal-header">
          <h3>Preview changes</h3>
          <button class="modal-close" onClick=${onClose}>×</button>
        </div>
        <div class="sb-diff-tabs">
          ${tabs.map(name => html`
            <button key=${name}
              class="sb-tab ${activeTab === name ? 'sb-tab-active' : ''}"
              onClick=${() => setActiveTab(name)}>
              ${name}
              ${name !== 'skill.md' && !(name in orig) && html`<span class="sb-pill sb-pill-new">new</span>`}
              ${name !== 'skill.md' && !(name in next) && html`<span class="sb-pill sb-pill-gone">removed</span>`}
            </button>
          `)}
        </div>
        <div class="sb-diff-body" ref=${containerRef} />
        <div class="modal-footer">
          <button class="btn btn-ghost" onClick=${onClose}>Cancel</button>
          <button class="btn btn-primary" onClick=${onConfirm}>Confirm save</button>
        </div>
      </div>
    </div>
  `;
}

// ── App ──────────────────────────────────────────────────────

function App() {
  const [agents, setAgents] = useState([]);
  const [loading, setLoading] = useState(true);
  const [passphrasePrompt, setPassphrasePrompt] = useState(null); // { agentId, error }
  const [forgeVersion, setForgeVersion] = useState('');
  const [updateAvailable, setUpdateAvailable] = useState(null); // { latest_version }
  const route = useHashRoute();

  const fetchingRef = useRef(false);

  const loadAgents = useCallback(async () => {
    if (fetchingRef.current) return;          // skip if a request is already in-flight
    fetchingRef.current = true;
    try {
      const data = await fetchAgents();
      setAgents(data || []);
    } catch (err) {
      console.error('Failed to load agents:', err);
    } finally {
      fetchingRef.current = false;
      setLoading(false);
    }
  }, []);

  // Initial load
  useEffect(() => { loadAgents(); }, [loadAgents]);

  // Fetch Forge version and check for updates once
  useEffect(() => {
    fetch('/api/health').then(r => r.json()).then(d => {
      if (d.version) setForgeVersion(d.version);
    }).catch(() => {});
    fetch('/api/update-check').then(r => r.json()).then(d => {
      if (d.has_update && d.latest_version) setUpdateAvailable(d);
    }).catch(() => {});
  }, []);

  // Polling fallback (every 60s) — SSE handles real-time updates; this is just a safety net
  useEffect(() => {
    const interval = setInterval(loadAgents, 60000);
    return () => clearInterval(interval);
  }, [loadAgents]);

  // SSE real-time updates
  useSSE((type, agentData) => {
    // agent_created carries only {id, directory}, not a full AgentInfo, so
    // refetch to pick up the record the cards render from (model, tools,
    // channels, status). The merge below deliberately ignores unknown ids,
    // so a create can't be handled there.
    if (type === 'agent_created') {
      loadAgents();
      return;
    }
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
      // Update agent state so the error is visible in the UI card.
      setAgents(prev => prev.map(a =>
        a.id === id ? { ...a, status: 'errored', error: err.message } : a
      ));
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
    // Flip to "stopping" on click. The server broadcasts this too, but
    // SSEBroker.Broadcast drops events for a full buffer, so the click
    // must not depend on the stream to feel responsive.
    //
    // Capture the prior status so a failure restores what was actually
    // there. Hardcoding 'running' would mislabel an agent that was, say,
    // already errored — the server's authoritative event reconciles it,
    // but not before the card renders the wrong state. Read it from the
    // rendered state rather than inside the updater, which is not
    // guaranteed to have run by the time the request settles.
    const prevStatus = agents.find(a => a.id === id)?.status;
    setAgents(prev => prev.map(a =>
      a.id === id ? { ...a, status: 'stopping', error: '' } : a
    ));
    try {
      await stopAgent(id);
    } catch (err) {
      console.error('Failed to stop agent:', err);
      // Mirror ProcessManager.Stop's rollback-to-previous so the card
      // doesn't stay stuck on "stopping" with disabled buttons.
      setAgents(prev => prev.map(a =>
        a.id === id ? { ...a, status: prevStatus || 'running', error: err.message } : a
      ));
    }
  }, [agents]);

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

  const activeAgentId = ['chat', 'config', 'skill-builder'].includes(route.page) ? route.params.id : null;

  const renderPage = () => {
    switch (route.page) {
      case 'chat':
        // key forces a fresh instance per agent. Without it Preact reuses
        // the mounted ChatPage on an agentId change, and useState survives
        // — so messages/sessionId/streaming stay on the previous agent
        // while the header and session list (props / agentId-keyed effect)
        // correctly re-render.
        return html`<${ChatPage} key=${route.params.id} agentId=${route.params.id} agents=${agents} />`;
      case 'create':
        return html`<${CreatePage} />`;
      case 'config':
        return html`<${ConfigPage} agentId=${route.params.id} />`;
      case 'skills':
        return html`<${SkillsPage} />`;
      case 'optimizer':
        return html`<${OptimizerPage} />`;
      case 'skill-builder':
        return html`<${SkillBuilderPage} agentId=${route.params.id} />`;
      default:
        return html`<${Dashboard}
          agents=${agents}
          onStart=${handleStart}
          onStop=${handleStop}
          onRescan=${handleRescan}
          onChannelsChanged=${loadAgents}
          loading=${loading}
        />`;
    }
  };

  return html`
    <div class="layout">
      <${Sidebar} agents=${agents} activeAgentId=${activeAgentId} activePage=${route.page} version=${forgeVersion} />
      ${renderPage()}
      ${passphrasePrompt && html`
        <${PassphraseModal}
          agentId=${passphrasePrompt.agentId}
          error=${passphrasePrompt.error}
          onSubmit=${handlePassphraseSubmit}
          onCancel=${() => setPassphrasePrompt(null)}
        />
      `}
      ${updateAvailable && html`
        <a class="update-banner" href="https://github.com/initializ/forge/releases/latest" target="_blank" rel="noopener noreferrer">
          Update Available! v${updateAvailable.latest_version}
        </a>
      `}
    </div>
  `;
}

// ── Mount ────────────────────────────────────────────────────

render(html`<${App} />`, document.getElementById('app'));
