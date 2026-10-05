// Lens frame prelude. The dashboard injects this ahead of every lens, so it runs before any lens
// code. It defines window.beacon and owns the frame's half of the host protocol:
//
//   host -> window   one postMessage {type: "beacon.lens.connect", version: 1} carrying a
//                    MessagePort, and nothing else.
//   frame -> port    {type: "getTrace"}            the lens asked for its data
//                    {type: "resize", height: n}   document height changed
//                    {type: "error", message: s}   uncaught error or unhandled rejection
//   port -> frame    {type: "trace", data: {...}}  the LensDataV1
//                    {type: "traceError", message: s}
//
// Trace data only ever crosses the private port. Everything that arrives here is checked, because
// the host is the only party allowed to talk to the frame and anything else is ignored.
(function () {
  "use strict";
  var TIMEOUT_MS = 10000;
  var port = null;
  var pending = [];
  var tracePromise = null;
  var settle = null;

  function send(message) {
    if (port) {
      port.postMessage(message);
    } else {
      pending.push(message);
    }
  }

  function onPortMessage(event) {
    var message = event.data;
    if (!message || typeof message !== "object" || !settle) return;
    if (message.type === "trace") {
      settle(null, message.data);
    } else if (message.type === "traceError") {
      settle(new Error(String(message.message || "the dashboard could not load this trace")));
    }
  }

  // Registered first and in the capture phase, so this listener sees the connect message before any
  // lens listener can, and stops it there: the port is the frame's only channel to the host.
  window.addEventListener("message", function onConnect(event) {
    var message = event.data;
    if (event.source !== window.parent || !message || message.type !== "beacon.lens.connect" || !event.ports || !event.ports[0]) {
      return;
    }
    event.stopImmediatePropagation();
    window.removeEventListener("message", onConnect, true);
    port = event.ports[0];
    port.onmessage = onPortMessage;
    while (pending.length) port.postMessage(pending.shift());
  }, true);

  function getTrace() {
    if (!tracePromise) {
      tracePromise = new Promise(function (resolve, reject) {
        var timer = setTimeout(function () {
          settle(new Error("the dashboard did not send this trace within " + TIMEOUT_MS / 1000 + " seconds"));
        }, TIMEOUT_MS);
        settle = function (err, data) {
          clearTimeout(timer);
          settle = null;
          if (err) {
            reject(err);
          } else {
            resolve(data);
          }
        };
      });
      send({ type: "getTrace" });
    }
    return tracePromise;
  }

  Object.defineProperty(window, "beacon", {
    value: Object.freeze({ getTrace: getTrace }),
    writable: false,
    configurable: false,
    enumerable: true
  });

  // The host sizes the frame to its content. Report the document height whenever it changes.
  var lastHeight = -1;
  function reportHeight() {
    var root = document.documentElement;
    var height = Math.ceil(Math.max(root ? root.scrollHeight : 0, document.body ? document.body.scrollHeight : 0));
    if (height !== lastHeight) {
      lastHeight = height;
      send({ type: "resize", height: height });
    }
  }
  function observeSize() {
    if (typeof ResizeObserver === "function") {
      new ResizeObserver(reportHeight).observe(document.documentElement);
    }
    reportHeight();
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", observeSize);
  } else {
    observeSize();
  }

  function reportError(message) {
    send({ type: "error", message: String(message).slice(0, 500) });
  }
  window.addEventListener("error", function (event) {
    reportError(event.message || "error");
  });
  window.addEventListener("unhandledrejection", function (event) {
    var reason = event.reason;
    reportError((reason && reason.message) || reason || "unhandled rejection");
  });
})();
