package conversation

var webUIScript = `let allNodes = [];
let maxID = 0;
const expandedSet = new Set();
const isoSet = new Set();
const minimizedSet = new Set();

function buildTree() {
  var nodeMap = {};
  allNodes.forEach(function(n) { nodeMap[n.hash] = Object.assign({}, n, { children: [] }); });
  var roots = [];
  Object.keys(nodeMap).forEach(function(k) {
    var n = nodeMap[k];
    if (n.parent_hash && nodeMap[n.parent_hash]) {
      nodeMap[n.parent_hash].children.push(n);
    } else {
      roots.push(n);
    }
  });
  return roots;
}

function renderChain(node) {
  var html = '';
  var cur = node;
  while (cur) {
    var nid = 'n-' + cur.hash.slice(0, 12);
    html += renderBaseline(cur);
    if (minimizedSet.has(nid)) {
      cur = null;
    } else if (cur.children.length === 1) {
      cur = cur.children[0];
    } else if (cur.children.length > 1) {
      cur.children.forEach(function(child, i) {
        html += '<div class="fork-group">';
        html += '<div class="fork-label">branch ' + (i + 1) + '</div>';
        html += renderChain(child);
        html += '</div>';
      });
      cur = null;
    } else {
      cur = null;
    }
  }
  return html;
}

function renderBaseline(node) {
  var role = node.role || 'unknown';
  var time = node.created_at ? new Date(node.created_at).toLocaleTimeString() : '';
  var preview = buildPreview(node);
  var nid = 'n-' + node.hash.slice(0, 12);
  var hasChildren = node.children && node.children.length > 0;
  var isMin = minimizedSet.has(nid);
  var html = '<div class="baseline">';
  if (hasChildren) {
    html += '<span class="min-icon" onclick="event.stopPropagation(); toggleMinimize(\'' + nid + '\')">' + (isMin ? '\u25b8' : '\u25be') + '</span>';
  } else {
    html += '<span class="min-icon">\u00a0</span>';
  }
  html += '<span class="expand-icon" id="icon-' + nid + '" onclick="toggleExpanded(\'' + nid + '\')">+</span>';
  html += '<span class="role role-' + role + '">' + esc(role) + '</span>';
  html += '<span class="preview" onclick="toggleExpanded(\'' + nid + '\')">' + esc(preview) + '</span>';
  html += '<span class="time">' + time + '</span>';
  html += '</div>';
  html += '<div class="expanded" id="exp-' + nid + '">';
  html += renderExpanded(node, nid);
  html += '</div>';
  return html;
}

function buildPreview(node) {
  var msg;
  try { msg = JSON.parse(node.message_json); } catch(e) { return node.summary || ''; }
  var role = (msg.Role || '').toLowerCase();

  if (role === 'tool' && msg.ToolResults && msg.ToolResults.length > 0) {
    return msg.ToolResults.map(function(tr) {
      var status = tr.IsError ? 'ERR' : 'ok';
      var out = (tr.Output || '').replace(/\n/g, ' ').slice(0, 80);
      return (tr.Name || tr.CallID || '?') + ' \u2192 ' + status + (out ? ': ' + out : '');
    }).join(' | ');
  }

  if (msg.ToolCalls && msg.ToolCalls.length > 0) {
    return msg.ToolCalls.map(function(tc) {
      var argSnippet = '';
      if (tc.Args) {
        var keys = Object.keys(tc.Args);
        if (keys.length <= 3) {
          argSnippet = keys.map(function(k) {
            var v = tc.Args[k];
            if (typeof v === 'string' && v.length > 30) v = v.slice(0,30) + '\u2026';
            return k + '=' + JSON.stringify(v);
          }).join(', ');
        } else {
          argSnippet = keys.length + ' args';
        }
      }
      return tc.Name + '(' + argSnippet + ')';
    }).join(' | ');
  }

  var text = (msg.Text || '').replace(/\n/g, ' ').trim();
  if (text) return text.length > 200 ? text.slice(0, 200) + '\u2026' : text;
  return node.summary || '';
}

function renderExpanded(node, nid) {
  var msg;
  try { msg = JSON.parse(node.message_json); } catch(e) { return '<pre>' + esc(node.message_json) + '</pre>'; }
  var html = '';
  var text = (msg.Text || '').trim();

  if (text) {
    html += '<div class="section"><div class="text-content"><pre>' + esc(text) + '</pre></div></div>';
  }

  if (msg.Parts && msg.Parts.length > 0) {
    var extraParts = msg.Parts.filter(function(p) {
      return p.Text && p.Text.trim() && p.Text.trim() !== text && !p.ThoughtSignature;
    });
    if (extraParts.length > 0) {
      html += '<div class="section"><span class="section-label">Parts</span>';
      extraParts.forEach(function(p) { html += '<pre>' + esc(p.Text.trim()) + '</pre>'; });
      html += '</div>';
    }
  }

  if (msg.ToolCalls && msg.ToolCalls.length > 0) {
    html += '<div class="section"><span class="section-label">Tool Calls</span>';
    msg.ToolCalls.forEach(function(tc) {
      html += '<div class="tool-call">';
      html += '<span class="tool-name">' + esc(tc.Name || '') + '</span>';
      html += '<span class="tool-id">' + esc(tc.ID || '') + '</span>';
      if (tc.Args && Object.keys(tc.Args).length > 0) {
        html += '<div class="tool-args"><pre>' + esc(JSON.stringify(tc.Args, null, 2)) + '</pre></div>';
      }
      html += '</div>';
    });
    html += '</div>';
  }

  if (msg.ToolResults && msg.ToolResults.length > 0) {
    html += '<div class="section"><span class="section-label">Tool Results</span>';
    msg.ToolResults.forEach(function(tr) {
      html += '<div class="tool-result">';
      html += '<div class="tool-result-header">';
      html += '<span class="tool-name">' + esc(tr.Name || tr.CallID || '') + '</span>';
      var cls = tr.IsError ? 'tool-result-err' : 'tool-result-ok';
      var label = tr.IsError ? 'error' : 'ok';
      html += '<span class="tool-result-status ' + cls + '">' + label + '</span>';
      if (tr.CallID) html += '<span class="tool-id">' + esc(tr.CallID) + '</span>';
      html += '</div>';
      if (tr.Output) {
        html += '<div class="tool-result-output"><pre>' + esc(tr.Output) + '</pre></div>';
      }
      html += '</div>';
    });
    html += '</div>';
  }

  if (!html) html = '<div class="section" style="color:#484f58">(empty message)</div>';

  html += '<span class="iso-toggle" onclick="event.stopPropagation(); toggleIso(\'' + nid + '\')">raw</span>';
  html += '<div class="iso" id="iso-' + nid + '">';
  html += '<span class="iso-label">hash</span><pre>' + node.hash + '</pre>';
  if (node.parent_hash) {
    html += '<span class="iso-label">parent</span><pre>' + node.parent_hash + '</pre>';
  }
  html += '<span class="iso-label">message</span><pre>' + esc(JSON.stringify(msg, null, 2)) + '</pre>';
  try {
    var meta = JSON.parse(node.metadata_json);
    if (Object.keys(meta).length > 0) {
      html += '<span class="iso-label">metadata</span><pre>' + esc(JSON.stringify(meta, null, 2)) + '</pre>';
    }
  } catch(e) {}
  html += '</div>';
  return html;
}

function toggleMinimize(nid) {
  if (minimizedSet.has(nid)) { minimizedSet.delete(nid); } else { minimizedSet.add(nid); }
  render();
}

function toggleExpanded(nid) {
  var el = document.getElementById('exp-' + nid);
  var icon = document.getElementById('icon-' + nid);
  if (!el) return;
  el.classList.toggle('open');
  var isOpen = el.classList.contains('open');
  icon.textContent = isOpen ? '\u2212' : '+';
  if (isOpen) expandedSet.add(nid); else expandedSet.delete(nid);
}

function toggleIso(nid) {
  var el = document.getElementById('iso-' + nid);
  if (!el) return;
  el.classList.toggle('open');
  if (el.classList.contains('open')) isoSet.add(nid); else isoSet.delete(nid);
}

function esc(s) {
  var d = document.createElement('div');
  d.textContent = s;
  return d.innerHTML;
}

function render() {
  var roots = buildTree();
  var treeEl = document.getElementById('tree');
  var html = '';
  roots.forEach(function(r) { html += renderChain(r); });
  treeEl.innerHTML = html;
  expandedSet.forEach(function(nid) {
    var el = document.getElementById('exp-' + nid);
    var icon = document.getElementById('icon-' + nid);
    if (el) { el.classList.add('open'); }
    if (icon) { icon.textContent = '\u2212'; }
  });
  isoSet.forEach(function(nid) {
    var el = document.getElementById('iso-' + nid);
    if (el) { el.classList.add('open'); }
  });
  document.getElementById('stats').textContent =
    allNodes.length + ' nodes | polling every 1s | max_id=' + maxID;
}

async function poll() {
  try {
    var resp = await fetch('/api/nodes?since_id=' + maxID);
    var nodes = await resp.json();
    if (nodes && nodes.length > 0) {
      nodes.forEach(function(n) {
        if (!allNodes.find(function(e) { return e.hash === n.hash; })) allNodes.push(n);
        if (n.id > maxID) maxID = n.id;
      });
      render();
    }
  } catch(e) {
    console.error('poll error:', e);
  }
}

async function init() {
  try {
    var resp = await fetch('/api/tree');
    var nodes = await resp.json();
    if (nodes) {
      allNodes = nodes;
      nodes.forEach(function(n) { if (n.id > maxID) maxID = n.id; });
    }
  } catch(e) {
    console.error('init error:', e);
  }
  render();
  setInterval(poll, 1000);
}

init();
`
