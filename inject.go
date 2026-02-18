package main

import "regexp"

// debugScriptTag is injected into HTML responses to enable debug channel
const debugScriptTag = `<script src="/__agent-reverse-proxy-debug__/inject.js"></script>`

// debugInjectScriptRe matches <head> or <body> tag (case insensitive)
var debugInjectScriptRe = regexp.MustCompile(`(?i)(<head[^>]*>|<body[^>]*>)`)

// previewProxyErrorPage returns an HTML error page for when the app is not running
// Uses fetch-based polling to avoid white flash on reload
// Note: %% is used to escape % characters in CSS (e.g., 50%%, 100vh) for fmt.Fprintf
const previewProxyErrorPage = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <title>App Preview</title>
    <script>
        (function(){var m=document.cookie.match(/(?:^|;\s*)%s=([^;]+)/);
        if(m)document.documentElement.setAttribute('data-theme',m[1]);})();
    </script>
    <style>
        :root {
            --pp-bg: #1e1e1e;
            --pp-text: #9ca3af;
            --pp-heading: #e5e7eb;
            --pp-instr-bg: #262626;
            --pp-instr-label: #6b7280;
            --pp-instr-text: #d1d5db;
            --pp-port: #60a5fa;
            --pp-status: #6b7280;
        }
        [data-theme="light"] {
            --pp-bg: #ffffff;
            --pp-text: #64748b;
            --pp-heading: #1e293b;
            --pp-instr-bg: #f1f5f9;
            --pp-instr-label: #94a3b8;
            --pp-instr-text: #334155;
            --pp-port: #2563eb;
            --pp-status: #94a3b8;
        }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            display: flex;
            align-items: center;
            justify-content: center;
            min-height: 100vh;
            margin: 0;
            background: var(--pp-bg);
            color: var(--pp-text);
        }
        .container {
            text-align: center;
            padding: 2rem;
            max-width: 400px;
        }
        h1 { color: var(--pp-heading); font-size: 1.25rem; font-weight: 500; margin-bottom: 1.5rem; }
        .instruction {
            background: var(--pp-instr-bg);
            border-radius: 8px;
            padding: 1rem 1.25rem;
            margin: 1rem 0;
            text-align: left;
        }
        .instruction-label {
            font-size: 0.8rem;
            color: var(--pp-instr-label);
            margin-bottom: 0.5rem;
        }
        .instruction-text {
            color: var(--pp-instr-text);
            font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
            font-size: 0.9rem;
            line-height: 1.5;
        }
        .port { color: var(--pp-port); }
        .status {
            font-size: 0.8rem;
            color: var(--pp-status);
            margin-top: 1.5rem;
        }
        .status-dot {
            display: inline-block;
            width: 6px;
            height: 6px;
            background: var(--pp-status);
            border-radius: 50%%;
            margin-right: 6px;
            animation: pulse 2s ease-in-out infinite;
        }
        @keyframes pulse {
            0%%, 100%% { opacity: 0.4; }
            50%% { opacity: 1; }
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>App Preview</h1>
        <div class="instruction">
            <div class="instruction-label">Tell your agent:</div>
            <div class="instruction-text">Start a hot-reload web app on <span class="port">localhost:%s</span></div>
        </div>
        <div class="status">
            <span class="status-dot"></span>
            <span id="status-text">Listening for app...</span>
        </div>
    </div>
    <script>
        // Poll for app availability without page reload (no white flash)
        async function checkApp() {
            try {
                const response = await fetch(window.location.href, { method: 'GET' });
                // 502 = proxy can't reach app (this page). Anything else = app is responding.
                if (response.status !== 502) {
                    window.location.reload();
                }
            } catch (e) {
                // Network error, keep polling
            }
        }
        // Check every 3 seconds
        setInterval(checkApp, 3000);
    </script>
</body>
</html>`

// shellPageHTML is the double-iframe shell page that wraps user content.
// It manages navigation (back/forward/reload) via WebSocket commands from the parent UI.
// The inner iframe loads the actual user app content. The shell page connects to the
// debug WebSocket as an iframe client and relays navigation state to the parent.
const shellPageHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Shell</title>
<style>*{margin:0;padding:0}html,body{width:100%%;height:100%%;overflow:hidden}
#inner{width:100%%;height:100%%;border:none}</style>
</head>
<body>
<iframe id="inner" src=""></iframe>
<script>
(function(){
  'use strict';
  var inner = document.getElementById('inner');
  var params = new URLSearchParams(location.search);
  var initialPath = params.get('path') || '/';
  inner.src = initialPath;

  // Shell-level navigation tracking for full-page (non-SPA) navigations
  var _shellNavIdx = 0;
  var _shellNavMax = 0;
  var _shellInitialLoad = true;
  var _shellPendingBack = false;
  var _shellPendingForward = false;

  // Connect to debug WS as iframe client
  var wsUrl = (location.protocol === 'https:' ? 'wss:' : 'ws:') + '//' + location.host + '/__agent-reverse-proxy-debug__/ws';
  var ws = null;
  var reconnectAttempts = 0;

  function send(msg) {
    if (ws && ws.readyState === 1) ws.send(JSON.stringify(msg));
  }

  function connect() {
    if (reconnectAttempts >= 5) return;
    try {
      ws = new WebSocket(wsUrl);
      ws.onopen = function() { reconnectAttempts = 0; };
      ws.onclose = function() {
        reconnectAttempts++;
        setTimeout(connect, Math.min(1000 * reconnectAttempts, 5000));
      };
      ws.onmessage = function(e) {
        try {
          var cmd = JSON.parse(e.data);
          if (cmd.t === 'navigate') {
            if (cmd.action === 'back') {
              if (_shellNavIdx > 0) {
                _shellNavIdx--;
                _shellPendingBack = true;
              }
              inner.contentWindow.history.back();
            } else if (cmd.action === 'forward') {
              if (_shellNavIdx < _shellNavMax) {
                _shellNavIdx++;
                _shellPendingForward = true;
              }
              inner.contentWindow.history.forward();
            } else if (cmd.url) {
              inner.src = cmd.url;
            }
          } else if (cmd.t === 'reload') {
            inner.contentWindow.location.reload();
          }
        } catch(err) {}
      };
    } catch(e) {}
  }

  // On inner iframe load, send urlchange + shell-level navstate
  inner.onload = function() {
    try {
      var url = inner.contentWindow.location.href;
      send({ t: 'urlchange', url: url, ts: Date.now() });
    } catch(e) {
      // Cross-origin: can't read inner URL
      send({ t: 'urlchange', url: inner.src, ts: Date.now() });
    }
    // Track full-page navigations at shell level.
    // _shellPendingBack/Forward are set when we trigger back/forward via WS command.
    // On initial load, do nothing. On subsequent loads, increment unless it was a back/forward.
    if (_shellInitialLoad) {
      _shellInitialLoad = false;
    } else if (_shellPendingBack) {
      _shellPendingBack = false;
    } else if (_shellPendingForward) {
      _shellPendingForward = false;
    } else {
      // New forward navigation (link click, form submit, navigate command)
      _shellNavIdx++;
      _shellNavMax = _shellNavIdx;
    }
    send({ t: 'navstate', canGoBack: _shellNavIdx > 0, canGoForward: _shellNavIdx < _shellNavMax });
  };

  connect();
})();
</script>
</body>
</html>`

// debugInjectJS is the debug script served at /__agent-reverse-proxy-debug__/inject.js
// It captures console logs, errors, fetch/XHR requests and forwards them via WebSocket
const debugInjectJS = `(function() {
  'use strict';

  // Prevent double initialization
  if (window.__arpDebugDebugInit) return;
  window.__arpDebugDebugInit = true;

  var ws = null;
  var wsUrl = (location.protocol === 'https:' ? 'wss:' : 'ws:') + '//' + location.host + '/__agent-reverse-proxy-debug__/ws';
  var messageQueue = [];
  var reconnectAttempts = 0;
  var maxReconnectAttempts = 5;

  // Serialize values safely (handle circular refs, DOM nodes, etc.)
  function serialize(val, depth) {
    if (depth === undefined) depth = 0;
    if (depth > 3) return '[max depth]';
    if (val === null) return null;
    if (val === undefined) return '[undefined]';
    if (typeof val === 'function') return '[function]';
    if (typeof val === 'symbol') return val.toString();
    if (val instanceof Error) return { name: val.name, message: val.message, stack: val.stack };
    if (val instanceof HTMLElement) return '<' + val.tagName.toLowerCase() + (val.id ? '#' + val.id : '') + '>';
    if (val instanceof Event) return { type: val.type, target: serialize(val.target, depth + 1) };
    if (Array.isArray(val)) return val.slice(0, 10).map(function(v) { return serialize(v, depth + 1); });
    if (typeof val === 'object') {
      try {
        var obj = {};
        var keys = Object.keys(val).slice(0, 20);
        for (var i = 0; i < keys.length; i++) {
          obj[keys[i]] = serialize(val[keys[i]], depth + 1);
        }
        return obj;
      } catch (e) {
        return '[object]';
      }
    }
    return val;
  }

  function send(msg) {
    var data = JSON.stringify(msg);
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(data);
    } else {
      messageQueue.push(data);
      if (messageQueue.length > 100) messageQueue.shift();
    }
  }

  function connect() {
    if (reconnectAttempts >= maxReconnectAttempts) return;

    try {
      ws = new WebSocket(wsUrl);

      ws.onopen = function() {
        reconnectAttempts = 0;
        // Flush queued messages
        while (messageQueue.length > 0) {
          ws.send(messageQueue.shift());
        }
      };

      ws.onclose = function() {
        reconnectAttempts++;
        setTimeout(connect, Math.min(1000 * reconnectAttempts, 5000));
      };

      ws.onerror = function() {
        // Error handling done in onclose
      };

      ws.onmessage = function(e) {
        try {
          var cmd = JSON.parse(e.data);
          if (cmd.t === 'query') {
            var el = document.querySelector(cmd.selector);
            send({
              t: 'queryResult',
              id: cmd.id,
              found: !!el,
              text: el ? el.textContent : null,
              html: el ? el.innerHTML.substring(0, 1000) : null,
              visible: el ? (el.offsetParent !== null || el.offsetWidth > 0 || el.offsetHeight > 0) : false,
              rect: el ? el.getBoundingClientRect() : null
            });
          }
        } catch (err) {
          // Ignore parse errors
        }
      };
    } catch (e) {
      // WebSocket not supported or blocked
    }
  }

  // Wrap console methods
  ['log', 'warn', 'error', 'info', 'debug'].forEach(function(method) {
    var original = console[method];
    console[method] = function() {
      var args = Array.prototype.slice.call(arguments);
      send({ t: 'console', m: method, args: args.map(function(a) { return serialize(a); }), ts: Date.now() });
      return original.apply(console, arguments);
    };
  });

  // Capture uncaught errors
  window.addEventListener('error', function(e) {
    send({
      t: 'error',
      msg: e.message,
      file: e.filename,
      line: e.lineno,
      col: e.colno,
      stack: e.error ? e.error.stack : null,
      ts: Date.now()
    });
  });

  // Capture unhandled promise rejections
  window.addEventListener('unhandledrejection', function(e) {
    send({
      t: 'rejection',
      reason: serialize(e.reason),
      ts: Date.now()
    });
  });

  // Wrap fetch
  var originalFetch = window.fetch;
  if (originalFetch) {
    window.fetch = function(input, init) {
      var url = typeof input === 'string' ? input : (input.url || String(input));
      var method = (init && init.method) || 'GET';
      var start = Date.now();

      return originalFetch.apply(this, arguments).then(function(response) {
        send({
          t: 'fetch',
          url: url,
          method: method,
          status: response.status,
          ok: response.ok,
          ms: Date.now() - start,
          ts: Date.now()
        });
        return response;
      }).catch(function(err) {
        send({
          t: 'fetch',
          url: url,
          method: method,
          error: err.message,
          ms: Date.now() - start,
          ts: Date.now()
        });
        throw err;
      });
    };
  }

  // Wrap XMLHttpRequest
  var XHROpen = XMLHttpRequest.prototype.open;
  var XHRSend = XMLHttpRequest.prototype.send;

  XMLHttpRequest.prototype.open = function(method, url) {
    this.__sweMethod = method;
    this.__sweUrl = url;
    this.__sweStart = null;
    return XHROpen.apply(this, arguments);
  };

  XMLHttpRequest.prototype.send = function() {
    var xhr = this;
    xhr.__sweStart = Date.now();

    xhr.addEventListener('loadend', function() {
      send({
        t: 'xhr',
        url: xhr.__sweUrl,
        method: xhr.__sweMethod,
        status: xhr.status,
        ok: xhr.status >= 200 && xhr.status < 300,
        ms: Date.now() - xhr.__sweStart,
        ts: Date.now()
      });
    });

    return XHRSend.apply(this, arguments);
  };

  // Navigation index tracking for back/forward button state
  var _navIdx = 0;
  var _navMax = 0;

  function stampState(idx) {
    try {
      var state = history.state;
      var merged = (state && typeof state === 'object') ? Object.assign({}, state) : {};
      merged.__arpDebugNavIdx = idx;
      origReplace.call(history, merged, '', location.href);
    } catch(e) {}
  }

  function sendNavState() {
    send({ t: 'navstate', canGoBack: _navIdx > 0, canGoForward: _navIdx < _navMax });
  }

  // URL change detection for SPA navigations
  var lastUrl = location.href;
  function checkUrl() {
    if (location.href !== lastUrl) {
      lastUrl = location.href;
      send({ t: 'urlchange', url: location.href, ts: Date.now() });
    }
  }

  var origPush = history.pushState;
  var origReplace = history.replaceState;

  history.pushState = function() {
    _navIdx++;
    _navMax = _navIdx;
    origPush.apply(this, arguments);
    stampState(_navIdx);
    checkUrl();
    sendNavState();
  };

  history.replaceState = function() {
    origReplace.apply(this, arguments);
    stampState(_navIdx);
    checkUrl();
  };

  window.addEventListener('popstate', function(e) {
    var state = e.state;
    if (state && typeof state.__arpDebugNavIdx === 'number') {
      _navIdx = state.__arpDebugNavIdx;
    }
    checkUrl();
    sendNavState();
  });

  window.addEventListener('hashchange', function() {
    checkUrl();
    sendNavState();
  });

  // Initialize: read existing navIdx from history.state or assume end-of-stack
  (function() {
    var state = history.state;
    if (state && typeof state.__arpDebugNavIdx === 'number') {
      _navIdx = state.__arpDebugNavIdx;
      _navMax = _navIdx;
    } else {
      // First visit: stamp index 0
      _navIdx = 0;
      _navMax = 0;
      stampState(0);
    }
  })();

  // Auto-upgrade ws:// to wss:// on HTTPS pages
  var OrigWebSocket = window.WebSocket;
  var wsWarningBar = null;
  function showWsWarning(originalUrl, fixedUrl) {
    send({ t: 'ws-upgrade', from: originalUrl, to: fixedUrl, ts: Date.now() });
    if (!wsWarningBar) {
      wsWarningBar = document.createElement('div');
      wsWarningBar.style.cssText = 'position:fixed;top:0;left:0;right:0;z-index:2147483647;'
        + 'background:#fef3cd;color:#856404;font:bold 12px/1.4 -apple-system,sans-serif;'
        + 'padding:4px 12px;text-align:center;border-bottom:1px solid #ffc107;';
      wsWarningBar.innerHTML = '';
      (document.body || document.documentElement).appendChild(wsWarningBar);
    }
    var line = document.createElement('div');
    line.textContent = 'WebSocket auto-upgraded: ' + originalUrl + ' \u2192 ' + fixedUrl
      + ' \u2014 Fix: use wss:// or (location.protocol==="https:"?"wss://":"ws://")';
    wsWarningBar.appendChild(line);
  }
  window.WebSocket = function(url, protocols) {
    if (location.protocol === 'https:' && typeof url === 'string' && url.indexOf('ws://') === 0) {
      var fixed = 'wss://' + url.slice(5);
      showWsWarning(url, fixed);
      url = fixed;
    }
    if (protocols !== undefined) {
      return new OrigWebSocket(url, protocols);
    }
    return new OrigWebSocket(url);
  };
  window.WebSocket.prototype = OrigWebSocket.prototype;
  window.WebSocket.CONNECTING = OrigWebSocket.CONNECTING;
  window.WebSocket.OPEN = OrigWebSocket.OPEN;
  window.WebSocket.CLOSING = OrigWebSocket.CLOSING;
  window.WebSocket.CLOSED = OrigWebSocket.CLOSED;

  // Connect to debug channel
  connect();

  // Send initial page load message (navstate sent by shell page on onload)
  send({ t: 'init', url: location.href, ts: Date.now() });
})();
`
