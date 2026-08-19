// Artifact annotations: injected by the parent (trusted scratchpad page) into
// the viewer iframe's own (same-origin) document. This script owns anchor
// resolution, highlights, the gutter markers, and the inline bubbles that
// double as the composer/editor. It never touches the network or storage —
// it reports human mutations to the parent via window.__scratchpadAnnotator's
// onChange callback and lets the parent persist them.
//
// Ported from the proven mockup at ~/.scratchpad/scratchpad/annotations
// (app.js / style.css). See .agents/spec/artifact-annotations.md, sections
// "Anchoring" and "Mechanism: annotating inside the viewer", for the anchor
// model and the injection boundary this script lives inside of.
(function () {
  "use strict";

  /* ------------------------------------------------------------- state */
  // All of this is re-initialized by init() and torn all the way down by
  // destroy(); init() calling destroy() first is what makes "safe to call
  // twice" true.

  var initialized = false;
  var mode = "element"; // "element" | "text"
  var annotate = false;
  var notesArr = []; // this document's notes, plus at most one draft
  var resolved = {}; // id -> {kind:"element", el} | {kind:"text", marks, num?}
  var activeId = null;
  var bubbleId = null; // note whose inline bubble is open, one at a time
  var draftId = null; // unsaved new note being composed in its bubble

  var onChangeCb = function () {};
  var onStateCb = function () {};

  var layer = null; // #__anno-layer, see buildLayer()
  var originX = 0, originY = 0; // layer's own origin, remeasured every render
  var pickBox = null;

  var mo = null; // MutationObserver watching the artifact's own DOM churn
  var moTimer = null; // debounces mo callbacks 250ms
  var suppressMO = false; // true while WE are mutating the DOM (see guardedRender)
  var suppressTimer = null;
  var resizeRaf = null;
  var remeasureTimer = null;

  /* ------------------------------------------------------------ helpers */

  function norm(s) { return (s || "").replace(/\s+/g, " ").trim(); }
  // Only trims the ends; a note body is pre-wrap multiline text and internal
  // whitespace/newlines are part of what the human typed.
  function trimBody(s) { return (s || "").replace(/^\s+/, "").replace(/\s+$/, ""); }
  function newId() { return Math.random().toString(36).slice(2, 8); }
  function reduceMotion() {
    return window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  }
  function el(tag, cls, text) {
    var n = document.createElement(tag);
    n.className = cls;
    n.textContent = text;
    return n;
  }
  function closestEl(node, sel) {
    return node && node.closest ? node.closest(sel) : null;
  }
  function noteById(id) {
    for (var i = 0; i < notesArr.length; i++) {
      if (notesArr[i].id === id) return notesArr[i];
    }
    return null;
  }
  function cloneNotes(list) {
    var out;
    try { out = JSON.parse(JSON.stringify(list || [])); } catch (e) { out = []; }
    for (var i = 0; i < out.length; i++) {
      if (!out[i].replies) out[i].replies = [];
    }
    return out;
  }
  // The set as reported to the parent: JSON-safe, drafts excluded (a draft
  // isn't a note yet — nothing to persist until it's saved).
  function exportNotes() {
    var out = [];
    for (var i = 0; i < notesArr.length; i++) {
      if (notesArr[i].draft) continue;
      out.push(notesArr[i]);
    }
    try { return JSON.parse(JSON.stringify(out)); } catch (e) { return out; }
  }
  function notifyChange(kind, id) {
    onChangeCb(exportNotes(), { kind: kind, id: id });
  }

  // Relative-time labels derived from the stored ISO timestamps (the mockup
  // carried pre-baked "…Label" strings; here there's only the real field).
  function relTime(iso) {
    var t = Date.parse(iso);
    if (isNaN(t)) return "";
    var diff = Date.now() - t;
    if (diff < 0) diff = 0;
    var sec = Math.floor(diff / 1000);
    if (sec < 45) return "just now";
    var min = Math.floor(sec / 60);
    if (min < 60) return min + "m ago";
    var hr = Math.floor(min / 60);
    if (hr < 24) return hr + "h ago";
    var day = Math.floor(hr / 24);
    if (day < 7) return day + "d ago";
    var d = new Date(t);
    return (d.getMonth() + 1) + "/" + d.getDate() + "/" + d.getFullYear();
  }

  /* --------------------------------------------------------- text nodes */

  // Flat map of the document's rendered text, so a quote selector can be
  // searched over it and mapped back to real nodes. (Verbatim port.)
  function textMap(root) {
    var nodes = [], text = "";
    var walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, null, false);
    var n;
    while ((n = walker.nextNode())) {
      if (!n.nodeValue.length) continue;
      nodes.push({ node: n, start: text.length });
      text += n.nodeValue;
    }
    return { nodes: nodes, text: text };
  }

  // Wrap [start,end) of the flat text in <mark> elements, one per text node
  // it crosses (Range.surroundContents cannot straddle element boundaries).
  function wrapRange(root, start, end, id) {
    var map = textMap(root), marks = [];
    for (var i = 0; i < map.nodes.length; i++) {
      var entry = map.nodes[i];
      var s = entry.start, e = s + entry.node.nodeValue.length;
      if (e <= start || s >= end) continue;
      var node = entry.node;
      var from = Math.max(start, s) - s;
      var to = Math.min(end, e) - s;
      if (to < node.nodeValue.length) node.splitText(to);
      var target = from > 0 ? node.splitText(from) : node;
      var m = document.createElement("mark");
      m.className = "anno-mark";
      m.setAttribute("data-anno-id", id);
      target.parentNode.insertBefore(m, target);
      m.appendChild(target);
      marks.push(m);
    }
    return marks;
  }

  /* ------------------------------------------------------------ anchors */

  // Element anchor: stable #id if there is one, else a structural
  // tag:nth-of-type path down from the nearest id-bearing ancestor.
  function buildSelector(elm, root) {
    if (elm.id) return "#" + elm.id;
    var parts = [], cur = elm;
    while (cur && cur !== root) {
      if (cur.id) { parts.unshift("#" + cur.id); break; }
      var tag = (cur.tagName || "").toLowerCase();
      var idx = 1, sib = cur;
      while ((sib = sib.previousElementSibling)) {
        if (sib.tagName === cur.tagName) idx++;
      }
      parts.unshift(tag + ":nth-of-type(" + idx + ")");
      cur = cur.parentElement;
    }
    return parts.join(" > ");
  }

  function fingerprint(elm) { return norm(elm.textContent).slice(0, 80); }

  // The raw flat text with whitespace runs collapsed to single spaces, plus a
  // map from each normalized character back to its raw offset.
  function normMap(text) {
    var out = "", idx = [], pendingWs = false;
    for (var i = 0; i < text.length; i++) {
      var ch = text.charAt(i);
      if (/\s/.test(ch)) { pendingWs = out.length > 0; continue; }
      if (pendingWs) { out += " "; idx.push(i); pendingWs = false; }
      out += ch;
      idx.push(i);
    }
    return { text: out, idx: idx };
  }

  // Searches normalized text with normalized quote parts. Prefix/suffix are
  // disambiguators, not requirements.
  function findQuote(normText, q) {
    var exact = norm(q.exact || "");
    if (!exact) return -1;
    var prefix = norm(q.prefix || ""), suffix = norm(q.suffix || "");
    var from = 0, first = -1;
    while (true) {
      var i = normText.indexOf(exact, from);
      if (i < 0) break;
      if (first < 0) first = i;
      var okPre = !prefix || normText.slice(Math.max(0, i - prefix.length - 2), i).indexOf(prefix.slice(-8)) >= 0;
      var okSuf = !suffix || normText.slice(i + exact.length, i + exact.length + suffix.length + 4).indexOf(suffix.slice(0, 8)) >= 0;
      if (okPre && okSuf) return i;
      from = i + 1;
    }
    return first;
  }

  // Returns null when the anchor no longer resolves -> the note is reported
  // unanchored rather than dropped.
  function resolveAnchor(a) {
    var root = document.body;
    if (a.target.type === "element") {
      var elm = null;
      try { elm = root.querySelector(a.target.selector); } catch (e) { elm = null; }
      if (!elm) return null;
      if (layer && (elm === layer || layer.contains(elm))) return null; // never anchor onto our own UI
      var fp = a.target.fingerprint;
      if (fp && fingerprint(elm).indexOf(fp.slice(0, 40)) !== 0) return null;
      return { kind: "element", el: elm };
    }

    var q = a.target.quote || {};
    var map = textMap(root);
    var nm = normMap(map.text);
    var idx = findQuote(nm.text, q);
    if (idx < 0) return null;
    var exactN = norm(q.exact || "");
    if (!exactN) return null;
    var rawStart = nm.idx[idx];
    var rawEnd = nm.idx[idx + exactN.length - 1] + 1;
    var marks = wrapRange(root, rawStart, rawEnd, a.id);
    return marks.length ? { kind: "text", marks: marks } : null;
  }

  function resolveAll() {
    resolved = {};
    var ns = notesArr, num = 0;
    for (var i = 0; i < ns.length; i++) {
      var r = resolveAnchor(ns[i]);
      if (!r) continue;
      if (ns[i].status === "open") { num++; r.num = num; }
      resolved[ns[i].id] = r;
    }
  }

  /* -------------------------------------------------------------- layer */

  // Coordinate layer: absolutely positioned at the top-left of its own
  // containing block, sized 0x0, so its children (also position:absolute)
  // scroll with the document for free. An artifact may set body{position:
  // relative} (or similar), which changes what "top:0;left:0" means, so we
  // never assume the initial containing block — we measure the layer's own
  // rendered origin every render instead. (A transform on some ancestor
  // would still break this; not handled, noted here rather than chased.)
  function buildLayer() {
    layer = document.createElement("div");
    layer.id = "__anno-layer";
    layer.className = "anno-layer";
    // Critical geometry set inline too: the parent injects the stylesheet
    // and this script together, but load order between the two isn't
    // guaranteed, and a frame with no positioning at all would scatter
    // absolutely-positioned children across the page.
    layer.style.position = "absolute";
    layer.style.top = "0";
    layer.style.left = "0";
    layer.style.width = "0";
    layer.style.height = "0";
    layer.style.zIndex = "2147483000";
    document.body.appendChild(layer);
  }

  function measureOrigin() {
    var o = layer.getBoundingClientRect();
    originX = o.left + window.scrollX;
    originY = o.top + window.scrollY;
  }

  // Converts a viewport-relative DOMRect (or rect-shaped plain object) into
  // layer-local coordinates: document coordinates minus the layer's origin.
  function toLayer(rect) {
    return {
      top: rect.top + window.scrollY - originY,
      left: rect.left + window.scrollX - originX,
      right: rect.right + window.scrollX - originX,
      bottom: rect.bottom + window.scrollY - originY,
      width: rect.width,
      height: rect.height,
    };
  }

  /* ------------------------------------------------------------- render */

  // Undo the previous render's DOM footprint: unwrap every <mark>, empty the
  // layer. Must run before resolveAll() re-wraps ranges, or offsets computed
  // against the still-wrapped text would be wrong.
  function clearLayer() {
    var marks = document.body.querySelectorAll("mark.anno-mark");
    for (var i = 0; i < marks.length; i++) {
      var m = marks[i], p = m.parentNode;
      if (!p) continue;
      while (m.firstChild) p.insertBefore(m.firstChild, m);
      p.removeChild(m);
      p.normalize();
    }
    while (layer.firstChild) layer.removeChild(layer.firstChild);
    pickBox = null;
  }

  function doRender() {
    clearLayer();
    resolveAll();
    renderMarkers();
    notifyState();
  }

  // Render is wrapped in a suppress flag so our own DOM writes (unwrapping/
  // rewrapping <mark>s, building marker/bubble elements) don't re-trigger the
  // MutationObserver and spin forever. The flag comes down one macrotask
  // later, after draining whatever records queued while it was up.
  function guardedRender() {
    if (!initialized) return;
    suppressMO = true;
    doRender();
    if (suppressTimer) clearTimeout(suppressTimer);
    suppressTimer = setTimeout(function () {
      suppressTimer = null;
      if (mo) mo.takeRecords();
      suppressMO = false;
    }, 0);
  }

  // blockRect finds the box of the nearest block-level ancestor of a node —
  // the paragraph, list item or cell the marked text lives in. Inline
  // ancestors are skipped because their box is the run of text itself, which
  // is the very thing we are trying not to sit on top of.
  function blockRect(node) {
    var el = node && node.nodeType === 1 ? node : node && node.parentElement;
    while (el && el !== document.body) {
      var d = window.getComputedStyle(el).display;
      if (d !== "inline" && d !== "inline-block" && d !== "contents") break;
      el = el.parentElement;
    }
    return (el || document.body).getBoundingClientRect();
  }

  function renderMarkers() {
    measureOrigin();
    var ns = notesArr;
    for (var i = 0; i < ns.length; i++) {
      var a = ns[i];
      var r = resolved[a.id];
      if (!r) continue;
      var targetEl = r.kind === "element" ? r.el : r.marks[0];
      var rawRect = targetEl.getBoundingClientRect();
      var tl = toLayer(rawRect);
      var isOpen = a.status === "open";

      if (r.kind === "element" && isOpen) {
        var box = document.createElement("div");
        box.className = "anno-box";
        box.setAttribute("data-anno-id", a.id);
        box.style.top = (tl.top - 4) + "px";
        box.style.left = (tl.left - 4) + "px";
        box.style.width = (rawRect.width + 8) + "px";
        box.style.height = (rawRect.height + 8) + "px";
        layer.appendChild(box);
      }

      var chip = document.createElement("button");
      chip.type = "button";
      chip.className = "anno-marker" + (isOpen ? "" : " done");
      chip.textContent = isOpen ? String(r.num) : "✓";
      chip.title = a.body;
      chip.setAttribute("data-anno-id", a.id);
      chip.style.top = tl.top + "px";
      // An element anchor is pinned to its own left edge. A text mark is not:
      // it usually starts mid-line, so its own edge would drop the number on
      // top of the words just before it. Pin those to the left edge of the
      // block the text sits in — the prose column, which is where a reader
      // expects a margin note. The CSS negative margin on .anno-marker
      // supplies the gutter; clamp so the chip can never run off the
      // document's own left edge.
      var anchorLeft = r.kind === "element" ? tl.left : toLayer(blockRect(r.marks[0])).left;
      chip.style.left = Math.max(4, anchorLeft) + "px";
      layer.appendChild(chip);
    }
    renderBubble();
    highlightActive();
  }

  // The inline bubble: the note plus its whole thread, drawn beside its
  // anchor in the same content-coordinate layer as the markers. Also the
  // composer/editor — see startDraft/saveNote.
  function renderBubble() {
    var id = bubbleId;
    if (!id) return;
    var r = resolved[id], a = noteById(id);
    if (!r || !a) { bubbleId = null; return; }

    var bubble = document.createElement("div");
    bubble.className = "anno-bubble";
    bubble.setAttribute("data-anno-id", id);

    var head = document.createElement("div");
    head.className = "anno-bubble-head";
    head.appendChild(el("span", "anno-bubble-when",
      a.draft ? "new note" : relTime(a.created) + (a.updated ? " · edited" : "")));
    if (!a.draft) { // no delete while composing — the note doesn't exist yet
      var del = document.createElement("button");
      del.type = "button";
      del.className = "anno-bubble-del";
      del.textContent = "✕ delete";
      del.title = a.status === "resolved" ? "accept the fix and remove the note" : "delete note";
      del.setAttribute("data-anno-del", id);
      head.appendChild(del);
    }
    var close = document.createElement("button");
    close.type = "button";
    close.className = "anno-bubble-close";
    close.textContent = "✕";
    close.title = a.draft ? "discard new note" : "close bubble";
    close.setAttribute("data-anno-close", "1");
    head.appendChild(close);
    bubble.appendChild(head);

    // the body doubles as the editor: same look reading or writing
    var bodyEl = document.createElement("p");
    bodyEl.className = "anno-bubble-body";
    bodyEl.setAttribute("contenteditable", "true");
    bodyEl.setAttribute("data-anno-edit", id);
    bodyEl.setAttribute("data-ph", "What should change here?");
    bodyEl.setAttribute("spellcheck", "false");
    bodyEl.textContent = a.body; // textContent, never innerHTML — body is untrusted text
    bubble.appendChild(bodyEl);

    // the thread: agent replies and resolve/reopen events, oldest first
    for (var i = 0; i < a.replies.length; i++) {
      var rep = a.replies[i];
      var row = document.createElement("div");
      row.className = "anno-reply " + (rep.by === "agent" ? "agent" : "user");
      var rh = document.createElement("div");
      rh.className = "anno-reply-head";
      rh.appendChild(el("span", "anno-who " + (rep.by === "agent" ? "agent" : "user"), rep.by === "agent" ? "agent" : "you"));
      if (rep.action) rh.appendChild(el("span", "anno-act " + rep.action, rep.action === "resolve" ? "resolved" : "reopened"));
      rh.appendChild(el("span", "anno-when", relTime(rep.created)));
      row.appendChild(rh);
      if (rep.body) row.appendChild(el("p", "anno-reply-body", rep.body)); // textContent
      bubble.appendChild(row);
    }

    if (a.status === "resolved") {
      var actions = document.createElement("div");
      actions.className = "anno-bubble-actions";
      var reopen = document.createElement("button");
      reopen.type = "button";
      reopen.className = "anno-bubble-reopen";
      reopen.textContent = "↺ reopen";
      reopen.title = "not fixed — put it back in front of the agent";
      reopen.setAttribute("data-anno-reopen", id);
      actions.appendChild(reopen);
      actions.appendChild(el("span", "anno-bubble-actions-hint", "or ✕ delete to accept"));
      bubble.appendChild(actions);
    }

    bubble.appendChild(el("p", "anno-bubble-meta",
      a.target.type === "element" ? "element " + a.target.selector : "text “" + a.target.quote.exact + "”"));

    // the ✓ save chip, bottom-right: shown only while the text differs from
    // what's stored (for a draft: once there is any text at all)
    var save = document.createElement("button");
    save.type = "button";
    save.className = "anno-bubble-save";
    save.textContent = "✓";
    save.title = a.draft ? "save note" : "save edit";
    save.setAttribute("data-anno-save", id);
    bubble.appendChild(save);

    var placementCls = "anno-from-top";
    function syncDirty() {
      var txt = trimBody(bodyEl.textContent);
      var dirty = a.draft ? txt.length > 0 : txt !== (a.body || "");
      bubble.className = "anno-bubble " + placementCls + (dirty ? " anno-dirty" : "");
    }
    bodyEl.addEventListener("input", syncDirty);
    bodyEl.addEventListener("keydown", function (e) {
      if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        saveNoteImpl(id, bodyEl.textContent);
      } else if (e.key === "Enter") {
        // contenteditable would insert <div>s; keep the body plain text
        e.preventDefault();
        document.execCommand("insertText", false, "\n");
        syncDirty();
      }
    });
    syncDirty();

    layer.appendChild(bubble);

    // ---- placement --------------------------------------------------
    // Anchor to the note's gutter marker: the notch points at the number.
    var chip = null;
    var markerNodes = layer.querySelectorAll(".anno-marker");
    for (var mi = 0; mi < markerNodes.length; mi++) {
      if (markerNodes[mi].getAttribute("data-anno-id") === id) { chip = markerNodes[mi]; break; }
    }
    if (!chip) { layer.removeChild(bubble); bubbleId = null; return; }

    var mL = toLayer(chip.getBoundingClientRect());
    var targetEl = r.kind === "element" ? r.el : r.marks[0];
    var tL = toLayer(targetEl.getBoundingClientRect());
    // "Visible viewport" stands in for the mockup's pane bounds — there's no
    // sub-pane here, the artifact is the whole page, so the fallback
    // candidates below must stay on today's screen, not some abstract
    // document-wide box.
    var vp = toLayer({ top: 0, left: 0, right: window.innerWidth, bottom: window.innerHeight });

    var best = null;

    // Preferred spot: the margin to the LEFT of the anchor, measured against
    // the document's own left edge (x=0 in layer-local coordinates) rather
    // than a content column's edge — there is no column, the artifact is
    // the whole page.
    var edge = Math.min(mL.left, tL.left) - 10; // the bubble's right limit
    var availL = edge - 8; // small inset from the document's left edge
    if (availL >= 200) {
      var wL = Math.min(bubble.offsetWidth, availL);
      bubble.style.width = wL + "px"; // narrower margin -> narrower bubble
      best = { cls: "anno-from-right", left: edge - wL, top: mL.top + mL.height / 2 - 19 };
    } else {
      bubble.style.width = ""; // default width for in-column placements
      var bw = bubble.offsetWidth, bh = bubble.offsetHeight;

      var obstacles = [];
      var anchors = layer.querySelectorAll(".anno-marker, .anno-box");
      for (var o = 0; o < anchors.length; o++) {
        if (anchors[o].getAttribute("data-anno-id") !== id) obstacles.push(toLayer(anchors[o].getBoundingClientRect()));
      }
      var docMarks = document.body.querySelectorAll("mark.anno-mark");
      for (var dm = 0; dm < docMarks.length; dm++) {
        if (docMarks[dm].getAttribute("data-anno-id") !== id) obstacles.push(toLayer(docMarks[dm].getBoundingClientRect()));
      }
      var overlapCount = function (x, y) {
        var n = 0;
        for (var i2 = 0; i2 < obstacles.length; i2++) {
          var b = obstacles[i2];
          if (x < b.right && b.left < x + bw && y < b.bottom && b.top < y + bh) n++;
        }
        return n;
      };

      // the notch centre sits 19px into the bubble's anchored edge
      var cands = [
        { cls: "anno-from-top", left: mL.left + mL.width / 2 - 19, top: mL.bottom + 10 },
        { cls: "anno-from-bottom", left: mL.left + mL.width / 2 - 19, top: mL.top - bh - 10 },
        { cls: "anno-from-left", left: mL.right + 10, top: mL.top + mL.height / 2 - 19 },
      ];
      var bestScore = Infinity;
      for (var ci = 0; ci < cands.length; ci++) {
        var cd = cands[ci];
        if (cd.left + bw > vp.right - 8) continue; // would run off the visible viewport's right edge
        if (cd.top < vp.top + 4) continue; // would poke out above the visible viewport
        var score = overlapCount(cd.left, cd.top);
        if (score === 0) { best = cd; break; }
        if (score < bestScore) { best = cd; bestScore = score; }
      }
      if (!best) best = cands[0]; // below the marker always exists
    }

    placementCls = best.cls;
    bubble.style.left = Math.max(8, best.left) + "px";
    bubble.style.top = Math.max(8, best.top) + "px";
    syncDirty(); // re-applies the class list with the placement settled
    if (a.draft) bodyEl.focus();
  }

  function highlightActive() {
    var nodes = layer.querySelectorAll(".anno-marker, .anno-box");
    for (var i = 0; i < nodes.length; i++) {
      var on = nodes[i].getAttribute("data-anno-id") === activeId;
      var cls = nodes[i].className.replace(/\s*active\b/g, "");
      nodes[i].className = cls + (on ? " active" : "");
    }
    var marks = document.body.querySelectorAll("mark.anno-mark");
    for (var j = 0; j < marks.length; j++) {
      var onM = marks[j].getAttribute("data-anno-id") === activeId;
      marks[j].className = "anno-mark" + (onM ? " active" : "");
    }
  }

  function flash(id) {
    var all = document.querySelectorAll("[data-anno-id]");
    for (var i = 0; i < all.length; i++) {
      if (all[i].getAttribute("data-anno-id") !== id) continue;
      var n = all[i];
      n.classList.remove("anno-flash");
      void n.offsetWidth; // restart the animation
      n.classList.add("anno-flash");
    }
  }

  // Reports render results to the parent in the onState shape. Excludes
  // drafts (nothing to show in a panel for a note that isn't saved yet).
  // "document order": anchored notes sorted by their anchor's vertical
  // position, unanchored ones appended in their original (creation) order
  // since they have no position to sort by.
  function notifyState() {
    var openN = 0, resolvedN = 0, unanchoredN = 0, total = 0;
    var anchoredPairs = [], unanchoredRows = [];
    for (var i = 0; i < notesArr.length; i++) {
      var a = notesArr[i];
      if (a.draft) continue;
      total++;
      var r = resolved[a.id];
      var row = {
        id: a.id,
        num: (r && a.status === "open") ? r.num : null,
        status: a.status,
        anchored: !!r,
        body: a.body,
        target: a.target,
        replies: a.replies,
        created: a.created,
        updated: a.updated || null,
      };
      if (r) {
        if (a.status === "open") openN++; else resolvedN++;
        var targetEl = r.kind === "element" ? r.el : r.marks[0];
        anchoredPairs.push({ top: targetEl.getBoundingClientRect().top, row: row });
      } else {
        unanchoredN++;
        unanchoredRows.push(row);
      }
    }
    anchoredPairs.sort(function (x, y) { return x.top - y.top; });
    var rows = [];
    for (var p = 0; p < anchoredPairs.length; p++) rows.push(anchoredPairs[p].row);
    for (var u = 0; u < unanchoredRows.length; u++) rows.push(unanchoredRows[u]);

    var safeRows;
    try { safeRows = JSON.parse(JSON.stringify(rows)); } catch (e) { safeRows = rows; }

    onStateCb({
      counts: { open: openN, resolved: resolvedN, unanchored: unanchoredN, total: total },
      rows: safeRows,
      activeId: activeId,
      bubbleId: bubbleId,
      annotate: annotate,
      mode: mode,
    });
  }

  /* --------------------------------------------------------- mutations */

  // Composing reuses the note bubble itself: startDraft pushes an empty note
  // flagged draft, whose bubble opens editable with no delete button. Saving
  // clears the flag (and fires onChange "create"); closing it discards it.
  function startDraft(target) {
    discardDraft();
    var id = newId();
    notesArr.push({
      id: id,
      draft: true,
      created: new Date().toISOString(),
      status: "open",
      body: "",
      target: target,
      replies: [],
    });
    draftId = id;
    activeId = id;
    bubbleId = id;
    guardedRender();
  }

  function discardDraft() {
    if (!draftId) return;
    for (var i = 0; i < notesArr.length; i++) {
      if (notesArr[i].id === draftId) { notesArr.splice(i, 1); break; }
    }
    if (bubbleId === draftId) bubbleId = null;
    if (activeId === draftId) activeId = null;
    draftId = null;
  }

  function saveNoteImpl(id, text) {
    var a = noteById(id);
    var body = trimBody(text);
    if (!a || !body) return;
    var wasDraft = !!a.draft;
    if (wasDraft) {
      delete a.draft;
      a.body = body;
      if (draftId === id) draftId = null;
    } else if (body !== a.body) {
      a.body = body;
      a.updated = new Date().toISOString();
    } else {
      return; // unchanged — no mutation happened, no onChange
    }
    guardedRender();
    notifyChange(wasDraft ? "create" : "edit", id);
  }

  function deleteNoteImpl(id) {
    var wasDraft = false, found = false;
    for (var i = 0; i < notesArr.length; i++) {
      if (notesArr[i].id === id) { wasDraft = !!notesArr[i].draft; notesArr.splice(i, 1); found = true; break; }
    }
    if (!found) return;
    if (activeId === id) activeId = null;
    if (bubbleId === id) bubbleId = null;
    if (draftId === id) draftId = null;
    guardedRender();
    if (!wasDraft) notifyChange("delete", id); // deleting a draft is just a discard
  }

  function reopenNoteImpl(id) {
    var a = noteById(id);
    if (!a || a.status !== "resolved") return;
    a.status = "open";
    a.replies.push({ by: "user", created: new Date().toISOString(), action: "reopen", body: "" });
    guardedRender();
    notifyChange("reopen", id);
  }

  function closeBubbleInternal() {
    if (!bubbleId) return;
    if (draftId === bubbleId) discardDraft();
    else bubbleId = null;
    guardedRender();
  }

  /* ------------------------------------------------------------- picker */

  function showPick(rect) {
    if (!pickBox) {
      pickBox = document.createElement("div");
      pickBox.className = "anno-pick";
      layer.appendChild(pickBox);
    }
    if (pickBox.parentNode !== layer) layer.appendChild(pickBox);
    var l = toLayer(rect);
    pickBox.style.display = "block";
    pickBox.style.top = (l.top - 3) + "px";
    pickBox.style.left = (l.left - 3) + "px";
    pickBox.style.width = (rect.width + 6) + "px";
    pickBox.style.height = (rect.height + 6) + "px";
  }
  function hidePick() { if (pickBox) pickBox.style.display = "none"; }

  function pickable(node) {
    if (!node || node.nodeType !== 1) return null;
    if (node === document.body) return null;
    if (layer && (node === layer || layer.contains(node))) return null; // never pick our own UI
    return node;
  }

  /* -------------------------------------------------------- interaction */

  function onDocMouseMove(e) {
    if (!annotate || mode !== "element") { hidePick(); return; }
    var target = pickable(e.target);
    if (!target) { hidePick(); return; }
    measureOrigin();
    showPick(target.getBoundingClientRect());
  }
  function onDocMouseLeave() { hidePick(); }

  function onDocClick(e) {
    var bub = closestEl(e.target, ".anno-bubble");
    if (bub) {
      if (e.target.hasAttribute && e.target.hasAttribute("data-anno-close")) closeBubbleInternal();
      else if (e.target.hasAttribute && e.target.hasAttribute("data-anno-del")) deleteNoteImpl(e.target.getAttribute("data-anno-del"));
      else if (e.target.hasAttribute && e.target.hasAttribute("data-anno-reopen")) reopenNoteImpl(e.target.getAttribute("data-anno-reopen"));
      else if (e.target.hasAttribute && e.target.hasAttribute("data-anno-save")) {
        var editable = bub.querySelector("[data-anno-edit]");
        saveNoteImpl(e.target.getAttribute("data-anno-save"), editable ? editable.textContent : "");
      }
      return; // clicks inside a bubble never fall through to the picker
    }
    var marker = closestEl(e.target, ".anno-marker");
    if (marker) {
      var mid = marker.getAttribute("data-anno-id");
      if (bubbleId === mid) { closeBubbleInternal(); return; }
      if (draftId && draftId !== mid) discardDraft(); // switching away kills the draft
      bubbleId = mid;
      activeId = mid;
      guardedRender();
      return;
    }
    if (!annotate || mode !== "element") return;
    var target = pickable(e.target);
    if (!target) return;
    e.preventDefault();
    startDraft({
      type: "element",
      selector: buildSelector(target, document.body),
      fingerprint: fingerprint(target),
    });
  }

  // A Range boundary can sit on an element (selectNodeContents, or a drag
  // that ends between nodes), so map both node kinds onto the flat text.
  function flatOffset(map, container, offset) {
    var i;
    if (container.nodeType === 3) {
      for (i = 0; i < map.nodes.length; i++) {
        if (map.nodes[i].node === container) return map.nodes[i].start + offset;
      }
      return -1;
    }
    var child = container.childNodes[offset] || container.lastChild;
    if (!child) return -1;
    for (i = 0; i < map.nodes.length; i++) {
      var n = map.nodes[i].node;
      if (child === n || child.contains(n)) return map.nodes[i].start;
    }
    return -1;
  }

  // Any selection in the document becomes a quote-selector anchor. Markdown
  // is rendered server-side to HTML before it reaches the iframe, so there's
  // no per-doc gating to port here — just the mode check.
  function quoteFromSelection() {
    var sel = window.getSelection();
    if (!sel || sel.isCollapsed || !sel.rangeCount) return null;
    var range = sel.getRangeAt(0);
    var root = document.body;
    if (!root.contains(range.startContainer) || !root.contains(range.endContainer)) return null;
    if (layer && (layer.contains(range.startContainer) || layer.contains(range.endContainer))) return null;

    var map = textMap(root);
    var start = flatOffset(map, range.startContainer, range.startOffset);
    var end = flatOffset(map, range.endContainer, range.endOffset);
    if (start < 0 || end < 0 || end <= start) return null;
    var exact = norm(map.text.slice(start, end));
    if (exact.length < 3) return null;

    return {
      type: "text",
      quote: {
        exact: exact,
        prefix: norm(map.text.slice(Math.max(0, start - 20), start)),
        suffix: norm(map.text.slice(end, end + 20)),
      },
    };
  }

  function onDocMouseUp(e) {
    if (!annotate || mode !== "text") return;
    if (closestEl(e.target, ".anno-bubble")) return; // typing, not selecting
    var q = quoteFromSelection();
    if (!q) return;
    startDraft(q);
  }

  function onKeyDown(e) {
    if (e.key !== "Escape") return;
    if (bubbleId) {
      // A draft bubble is discarded by closeBubbleInternal. Stop this from
      // reaching the parent's own Esc-to-close handler for the viewer.
      e.stopPropagation();
      closeBubbleInternal();
    }
    // Otherwise leave the event alone — the existing viewer Esc-to-close
    // still needs it. Also deliberately not binding e/n here: those are the
    // parent's shortcuts, not ours.
  }

  function onResize() {
    if (resizeRaf) cancelAnimationFrame(resizeRaf);
    resizeRaf = requestAnimationFrame(function () {
      resizeRaf = null;
      guardedRender();
    });
  }
  function onLoad() { guardedRender(); }

  // touchesArtifact reports whether any record came from outside our own
  // layer. A text-node target is attributed to its parent element, since a
  // bare text node is never the thing `contains` was asked about.
  function touchesArtifact(records) {
    if (!layer || !records || !records.length) return true;
    for (var i = 0; i < records.length; i++) {
      var t = records[i].target;
      if (t && t.nodeType === 3) t = t.parentNode;
      if (!t || !layer.contains(t)) return true;
    }
    return false;
  }

  // Script-driven artifacts rebuild their own DOM; re-resolve when that
  // happens. Debounced, and ignored entirely while WE are the ones mutating
  // (see guardedRender) so wrapping/unwrapping <mark>s never spins this.
  function onMutate(records) {
    if (suppressMO) return;
    // Our layer lives inside document.body, so the user typing in a bubble
    // reaches this callback too — and re-rendering on that would rebuild the
    // bubble from the stored note and throw away what was being typed. The
    // suppress flag can't cover it: typing is the user's mutation, not ours.
    // Only the artifact's own DOM churn is worth re-resolving for.
    if (!touchesArtifact(records)) return;
    if (moTimer) clearTimeout(moTimer);
    moTimer = setTimeout(function () {
      moTimer = null;
      guardedRender();
    }, 250);
  }

  /* ------------------------------------------------------------- public */

  function initImpl(opts) {
    if (initialized) destroyImpl(); // safe to call twice: tear down first
    opts = opts || {};
    mode = opts.mode === "text" ? "text" : "element";
    onChangeCb = typeof opts.onChange === "function" ? opts.onChange : function () {};
    onStateCb = typeof opts.onState === "function" ? opts.onState : function () {};
    notesArr = cloneNotes(opts.notes);
    annotate = !!opts.annotate;
    activeId = null; bubbleId = null; draftId = null;
    resolved = {};

    buildLayer();
    if (annotate) {
      document.body.classList.add(mode === "text" ? "anno-annotating-text" : "anno-annotating-element");
    }

    mo = new MutationObserver(onMutate);
    mo.observe(document.body, { childList: true, subtree: true, characterData: true });

    window.addEventListener("resize", onResize);
    window.addEventListener("load", onLoad);
    document.addEventListener("keydown", onKeyDown, true);
    document.addEventListener("mousemove", onDocMouseMove);
    document.addEventListener("mouseleave", onDocMouseLeave);
    document.addEventListener("click", onDocClick);
    document.addEventListener("mouseup", onDocMouseUp);

    initialized = true;
    guardedRender();
    // Web fonts and images can settle after first paint; markers are
    // measured, so re-measure once things have.
    remeasureTimer = setTimeout(function () {
      remeasureTimer = null;
      guardedRender();
    }, 120);
  }

  function destroyImpl() {
    if (!initialized) return;
    if (moTimer) { clearTimeout(moTimer); moTimer = null; }
    if (suppressTimer) { clearTimeout(suppressTimer); suppressTimer = null; }
    if (remeasureTimer) { clearTimeout(remeasureTimer); remeasureTimer = null; }
    if (resizeRaf) { cancelAnimationFrame(resizeRaf); resizeRaf = null; }
    if (mo) { mo.disconnect(); mo = null; }

    window.removeEventListener("resize", onResize);
    window.removeEventListener("load", onLoad);
    document.removeEventListener("keydown", onKeyDown, true);
    document.removeEventListener("mousemove", onDocMouseMove);
    document.removeEventListener("mouseleave", onDocMouseLeave);
    document.removeEventListener("click", onDocClick);
    document.removeEventListener("mouseup", onDocMouseUp);

    if (layer) {
      clearLayer(); // unwraps every <mark>, empties the layer
      if (layer.parentNode) layer.parentNode.removeChild(layer);
    }
    layer = null;
    pickBox = null;

    document.body.classList.remove("anno-annotating-element", "anno-annotating-text");

    notesArr = [];
    resolved = {};
    activeId = null; bubbleId = null; draftId = null;
    onChangeCb = function () {};
    onStateCb = function () {};
    suppressMO = false;
    initialized = false;
  }

  function setNotesImpl(notes) {
    if (!initialized) return;
    discardDraft(); // fresh authoritative data replaces any unsaved local draft
    notesArr = cloneNotes(notes);
    if (activeId && !noteById(activeId)) activeId = null;
    if (bubbleId && !noteById(bubbleId)) bubbleId = null;
    guardedRender();
  }

  function setAnnotateImpl(on) {
    if (!initialized) return;
    annotate = !!on;
    if (annotate) bubbleId = null; // entering pick/select mode: picking, not reading
    if (!annotate) { hidePick(); discardDraft(); }
    document.body.classList.remove("anno-annotating-element", "anno-annotating-text");
    if (annotate) document.body.classList.add(mode === "text" ? "anno-annotating-text" : "anno-annotating-element");
    guardedRender();
  }

  function setActiveImpl(id) {
    if (!initialized) return;
    activeId = id || null;
    highlightActive();
    notifyState();
  }

  function focusImpl(id) {
    if (!initialized) return;
    if (draftId && draftId !== id) discardDraft();
    if (!noteById(id)) return;
    activeId = id;
    bubbleId = id;
    guardedRender(); // resolves + (re)builds the bubble; clears bubbleId again if it doesn't resolve
    var r = resolved[id];
    if (!r) return;
    var target = r.kind === "element" ? r.el : r.marks[0];
    if (typeof target.scrollIntoView === "function") {
      try {
        target.scrollIntoView({ block: "center", behavior: reduceMotion() ? "auto" : "smooth" });
      } catch (e) {
        // options object form unsupported (older engines) -- fall back to
        // the no-args form, which every implementation understands
        target.scrollIntoView();
      }
    }
    flash(id);
  }

  window.__scratchpadAnnotator = {
    version: 1,
    init: initImpl,
    setNotes: setNotesImpl,
    setAnnotate: setAnnotateImpl,
    setActive: setActiveImpl,
    focus: focusImpl,
    closeBubble: closeBubbleInternal,
    refresh: function () { guardedRender(); },
    destroy: destroyImpl,
  };
})();
