/**
 * Bunghole Portal Bridge SDK v1
 *
 * When bunghole runs inside the Bunghole Portal (as an iframe service),
 * this module handles the postMessage bridge protocol. When running
 * standalone, it's a no-op.
 *
 * Protocol: bunghole.portal.v1
 * See: bunghole_portal/docs/portal/iframe-bridge.md
 */
'use strict';

const BungholeBridge = (function () {
  const CHANNEL = 'bunghole.portal.v1';

  // --- State ---
  let embedded = false;
  let portalOrigin = null;
  let sessionId = null;
  let authToken = null;
  let listeners = {};
  let ready = false;

  // --- Detection ---
  function isEmbedded() {
    try {
      return window.self !== window.top;
    } catch (e) {
      return true; // cross-origin restriction = definitely embedded
    }
  }

  // --- Event system ---
  function on(event, fn) {
    if (!listeners[event]) listeners[event] = [];
    listeners[event].push(fn);
  }

  function off(event, fn) {
    if (!listeners[event]) return;
    listeners[event] = listeners[event].filter(f => f !== fn);
  }

  function emit(event, data) {
    if (!listeners[event]) return;
    for (const fn of listeners[event]) {
      try { fn(data); } catch (e) { console.error('bridge: listener error', event, e); }
    }
  }

  // --- Messaging ---
  function sendToPortal(type, payload, requestId) {
    if (!embedded || !portalOrigin) return;
    const msg = {
      channel: CHANNEL,
      type: type,
      sessionId: sessionId,
      ts: Date.now(),
      payload: payload || {}
    };
    if (requestId) msg.requestId = requestId;
    window.parent.postMessage(msg, portalOrigin);
  }

  function handleMessage(event) {
    const data = event.data;
    if (!data || data.channel !== CHANNEL) return;
    if (portalOrigin && event.origin !== portalOrigin) return;
    if (sessionId && data.sessionId && data.sessionId !== sessionId) return;

    switch (data.type) {
      case 'portal:init':
        portalOrigin = event.origin;
        sessionId = data.sessionId;
        if (data.payload && data.payload.auth) {
          authToken = data.payload.auth.token || null;
        }
        emit('init', {
          theme: data.payload.theme,
          locale: data.payload.locale,
          capabilities: data.payload.capabilities,
          auth: data.payload.auth
        });
        if (data.requestId) {
          sendToPortal('bridge:ack', { ok: true }, data.requestId);
        }
        break;

      case 'portal:focus':
        emit('focus', data.payload);
        if (data.requestId) sendToPortal('bridge:ack', { ok: true }, data.requestId);
        break;

      case 'portal:blur':
        emit('blur', data.payload);
        if (data.requestId) sendToPortal('bridge:ack', { ok: true }, data.requestId);
        break;

      case 'portal:resize':
        emit('resize', data.payload);
        if (data.requestId) sendToPortal('bridge:ack', { ok: true }, data.requestId);
        break;

      case 'portal:theme':
        emit('theme', data.payload);
        if (data.requestId) sendToPortal('bridge:ack', { ok: true }, data.requestId);
        break;

      case 'portal:auth:update':
        if (data.payload) authToken = data.payload.token || authToken;
        emit('auth:update', data.payload);
        if (data.requestId) sendToPortal('bridge:ack', { ok: true }, data.requestId);
        break;

      case 'portal:session:terminate':
        emit('terminate', data.payload);
        if (data.requestId) sendToPortal('bridge:ack', { ok: true }, data.requestId);
        break;

      default:
        if (data.requestId) {
          sendToPortal('bridge:nack', { ok: false, error: 'unsupported_event' }, data.requestId);
        }
        break;
    }
  }

  // --- Public API ---

  function init() {
    embedded = isEmbedded();
    if (!embedded) return false;
    window.addEventListener('message', handleMessage);
    return true;
  }

  function serviceReady(version) {
    ready = true;
    sendToPortal('service:ready', { version: version || '1.0.0' });
  }

  function serviceError(code, message, fatal) {
    sendToPortal('service:error', {
      code: code || 'UNKNOWN',
      message: message || '',
      fatal: !!fatal
    });
  }

  function notify(level, title, message) {
    sendToPortal('service:notify', {
      level: level || 'info',
      title: title || '',
      message: message || ''
    });
  }

  function openExternal(url) {
    sendToPortal('service:openExternal', { url: url });
  }

  function requestAuthRefresh(reason) {
    sendToPortal('service:requestAuthRefresh', { reason: reason || 'expiring_soon' });
  }

  function getToken() { return authToken; }
  function isPortalEmbedded() { return embedded; }

  function destroy() {
    window.removeEventListener('message', handleMessage);
    listeners = {};
    embedded = false;
    portalOrigin = null;
    sessionId = null;
    authToken = null;
    ready = false;
  }

  return {
    init,
    on,
    off,
    isEmbedded: isPortalEmbedded,
    getToken,
    serviceReady,
    serviceError,
    notify,
    openExternal,
    requestAuthRefresh,
    destroy
  };
})();

if (typeof module !== 'undefined' && module.exports) {
  module.exports = BungholeBridge;
}
