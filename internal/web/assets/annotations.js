// Parent-side half of artifact annotations (see .agents/spec/artifact-
// annotations.md). The viewer overlay's iframe is same-origin only because a
// user deliberately clicked to open it — this script leans on exactly that
// trick to inject the other half (annotator.js/annotator.css, owned
// separately) into the iframe's own document. Card preview iframes never get
// this: they run sandbox="allow-scripts" without allow-same-origin so that
// auto-running artifact JS can never reach here or the delete endpoint.
//
// This half owns: the annotate/panel toggles, the doc-switcher chips (viewer
// chrome for any multi-document artifact, not just annotated ones), the
// side panel, and all persistence (fetch/PUT/rev/409 handling). The
// annotator owns anchors, highlights, the marker gutter, and the inline
// bubbles — reached only through window.__scratchpadAnnotator inside the
// iframe.
(function () {
  // Per-open state. Everything here is scoped to one #overlay/iframe pair
  // and is fully reset by reset()/destroy() on every doc switch or close, so
  // stale listeners never end up pointed at a torn-down document.
  var doc = null; // store-relative path of the current document, "" for none
  var mode = "element"; // "element" | "text"
  var frame = null; // the viewer <iframe>
  var annotator = null; // frame.contentWindow.__scratchpadAnnotator once injected
  var frameLoadHandler = null; // removed on reset: the iframe element outlives a morph swap
  var injectedDoc = null; // the iframe document we last injected into (see tryInject)
  var injectTimer = null; // safety-net poll while a navigation is still settling
  var rev = 0; // last-known server revision for PUT's optimistic lock
  var notes = []; // authoritative notes array (mirrors the annotator's, drafts excluded)
  var annotateState = false; // annotator's own annotate-mode flag, mirrored from onState
  var panelOpen = false; // parent-owned; the annotator has no notion of the panel
  var saving = false; // one PUT in flight at a time
  var pending = null; // {notes, meta} queued while saving; only the latest survives
  var injectWarned = false; // log the inject-failure once, not on every retry
  var lastPanelKey = null; // last rendered panel content, see panelKey()
  // Bumped on every reset(); in-flight network callbacks compare against it
  // before touching shared state, so a response for a doc that's already
  // been switched away from is silently dropped instead of corrupting the
  // new doc's state.
  var session = 0;

  function $(id) {
    return document.getElementById(id);
  }

  function el(tag, cls, text) {
    var n = document.createElement(tag);
    n.className = cls;
    n.textContent = text; // every field here is untrusted text — never innerHTML
    return n;
  }

  function hide(node) {
    if (node) node.style.display = "none";
  }

  function reset() {
    session++;
    if (annotator) {
      try {
        annotator.destroy();
      } catch (_) {}
    }
    // A morph swap reuses the iframe ELEMENT across a doc switch, so a load
    // listener left on it would fire for the next document too, with the
    // previous attach's captured state.
    if (frame && frameLoadHandler) {
      frame.removeEventListener("load", frameLoadHandler);
    }
    frameLoadHandler = null;
    if (injectTimer) {
      clearInterval(injectTimer);
      injectTimer = null;
    }
    injectedDoc = null;
    lastPanelKey = null;
    doc = null;
    mode = "element";
    frame = null;
    annotator = null;
    rev = 0;
    notes = [];
    annotateState = false;
    panelOpen = false;
    saving = false;
    pending = null;
  }

  /* ------------------------------------------------------------ attach */

  document.addEventListener("htmx:beforeSwap", function (e) {
    if (e.detail && e.detail.target && e.detail.target.id === "viewer") reset();
  });

  document.addEventListener("htmx:afterSwap", function (e) {
    if (!e.detail || !e.detail.target || e.detail.target.id !== "viewer") return;
    attach();
  });

  function attach() {
    reset(); // defensive: covers the close path too, which clears #viewer
    // without an htmx swap (viewer.js's viewerClose/popstate clear()).
    var overlay = document.getElementById("overlay");
    var d = overlay && overlay.getAttribute("data-doc");
    if (!overlay || !d) return; // server rendered no annotation chrome

    var mySession = session;
    doc = d;
    mode = overlay.getAttribute("data-mode") || "element";
    frame = overlay.querySelector("iframe");
    if (!frame) return;

    wireControls();

    var loadedNotes = null;
    var loadedRev = 0;
    var haveNotes = false;

    // Knowing *when* an iframe is done settling is genuinely hard here: a
    // freshly inserted frame already holds an about:blank document that
    // reports readyState "complete"; a morph swap reuses the element with the
    // previous artifact still in it while the new src loads; and the URLs
    // can't be compared either, because ServeFile 301s /a/x/index.html to
    // /a/x/. So don't try to time it. Inject into whatever real document is
    // there, remember which document that was, and inject again if it turns
    // out to have been replaced. Injecting into a doomed document costs
    // nothing — it is discarded with the document — and this converges no
    // matter how the navigation is sequenced.
    function tryInject() {
      if (!haveNotes || mySession !== session || !frame) return;
      var idoc;
      try {
        idoc = frame.contentDocument;
      } catch (_) {
        return; // not same-origin (yet): degrade() happens on the real attempt
      }
      if (!idoc || idoc.readyState !== "complete") return;
      if (!idoc.URL || idoc.URL === "about:blank") return;
      if (idoc === injectedDoc) return;
      injectedDoc = idoc;
      inject(mySession, loadedNotes, loadedRev);
    }

    fetchNotes(d, function (annotations, r) {
      if (mySession !== session) return;
      loadedNotes = annotations;
      loadedRev = r;
      haveNotes = true;
      tryInject();
    });

    frameLoadHandler = function () {
      if (mySession !== session) return;
      tryInject();
    };
    frame.addEventListener("load", frameLoadHandler);

    // The load event is the normal signal; this only covers the window where
    // a swap has already put a stale document in front of us and the new
    // one's load has not fired yet. It stops as soon as the frame settles.
    var polls = 0;
    injectTimer = setInterval(function () {
      if (mySession !== session || ++polls > 60) {
        clearInterval(injectTimer);
        injectTimer = null;
        return;
      }
      tryInject();
    }, 100);

    tryInject();
  }

  function inject(mySession, loadedNotes, loadedRev) {
    notes = loadedNotes;
    rev = loadedRev;
    try {
      var idoc = frame.contentDocument;
      var head = idoc.head || idoc.documentElement;

      var link = idoc.createElement("link");
      link.rel = "stylesheet";
      link.href = "/static/annotator.css";
      head.appendChild(link);

      var script = idoc.createElement("script");
      script.src = "/static/annotator.js";
      script.addEventListener("load", function () {
        if (mySession !== session) return;
        // The document we injected into may already have been replaced by a
        // pending navigation; its annotator is not the live one.
        if (frame.contentDocument !== idoc) return;
        try {
          annotator = frame.contentWindow.__scratchpadAnnotator;
          annotator.init({
            mode: mode,
            notes: notes,
            annotate: false,
            onChange: onChange,
            onState: onState,
          });
        } catch (e) {
          degrade(e);
        }
      });
      head.appendChild(script);
    } catch (e) {
      degrade(e);
    }
  }

  // The iframe document is unreachable: a hostile CSP, a cross-origin
  // redirect the artifact performed after load, or annotator.js/css aren't
  // deployed yet. Hide the chrome and keep the rest of the viewer working —
  // this is the one path where the same-origin trick doesn't pay off.
  function degrade(e) {
    if (!injectWarned) {
      injectWarned = true;
      console.warn("scratchpad: could not attach annotator to viewer iframe", e);
    }
    hide($("annobtn"));
    hide($("panelbtn"));
    hide($("panel"));
    hide($("paneltab"));
  }

  /* --------------------------------------------------------------- HTTP */

  function notesURL(d) {
    var parts = d.split("/");
    var out = [];
    for (var i = 0; i < parts.length; i++) out.push(encodeURIComponent(parts[i]));
    return "/notes/" + out.join("/");
  }

  function fetchNotes(d, cb) {
    fetch(notesURL(d) + "?format=json&status=all")
      .then(function (r) {
        return r.json();
      })
      .then(function (data) {
        var dn = data && data[0];
        var f = dn && dn.notes;
        cb(f && f.annotations ? f.annotations : [], f ? f.rev : 0);
      })
      .catch(function () {
        cb([], 0); // no sidecar / unreachable — an empty set is the correct default
      });
  }

  // PUT /notes/<doc>. Only 200 and 409 carry a JSON body (see notes.go); any
  // other status is surfaced as {status, body:null} so callers don't try to
  // parse plain-text error bodies as JSON.
  function putNotes(d, expectRev, ns) {
    return fetch(notesURL(d), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ rev: expectRev, annotations: ns }),
    }).then(function (r) {
      if (r.status === 200 || r.status === 409) {
        return r.json().then(function (body) {
          return { status: r.status, body: body };
        });
      }
      return { status: r.status, body: null };
    });
  }

  /* ---------------------------------------------------------- save loop */

  function onChange(ns, meta) {
    notes = ns;
    scheduleSave(doc, ns, meta);
  }

  function scheduleSave(targetDoc, ns, meta) {
    if (saving) {
      pending = { doc: targetDoc, notes: ns, meta: meta }; // coalesce: latest wins
      return;
    }
    saving = true;
    doSave(session, targetDoc, ns, meta);
  }

  function doSave(mySession, targetDoc, ns, meta) {
    putNotes(targetDoc, rev, ns)
      .then(function (res) {
        if (mySession !== session) return;
        if (res.status === 200) {
          rev = res.body.rev;
          // No setNotes here: it would rebuild the annotator's layer and
          // close the bubble a human might still be typing in.
          clearSaveBanner();
        } else if (res.status === 409) {
          return retryOnce(mySession, targetDoc, res.body, meta);
        } else {
          showSaveBanner("could not save notes — retrying on the next change");
        }
      })
      .catch(function () {
        if (mySession !== session) return;
        // Network/5xx: leave the annotator's own state untouched. The next
        // onChange resends the whole current array anyway, so there is
        // nothing useful to retry right now.
        showSaveBanner("could not save notes — retrying on the next change");
      })
      .then(function () {
        if (mySession !== session) return;
        saving = false;
        if (pending) {
          var next = pending;
          pending = null;
          scheduleSave(next.doc, next.notes, next.meta);
        }
      });
  }

  // A 409 means someone else (the CLI, or another viewer tab) wrote first.
  // Reconcile by replaying our one intended mutation onto the server's
  // current array, then PUT once more with the server's rev. The retry is
  // bounded to exactly one: a second collision means something wrote again
  // during that round trip, and looping risks never converging, so instead
  // we adopt the server's array outright and tell the human why.
  function retryOnce(mySession, targetDoc, serverFile, meta) {
    var merged = reconcile(serverFile.annotations, meta);
    return putNotes(targetDoc, serverFile.rev, merged).then(function (res2) {
      if (mySession !== session) return;
      if (res2.status === 200) {
        rev = res2.body.rev;
        clearSaveBanner();
      } else if (res2.status === 409) {
        rev = res2.body.rev;
        notes = res2.body.annotations;
        if (annotator) {
          try {
            annotator.setNotes(notes);
          } catch (_) {}
        }
        showSaveBanner("notes changed elsewhere — reloaded");
      } else {
        showSaveBanner("could not save notes — retrying on the next change");
      }
    });
  }

  function findNote(arr, id) {
    for (var i = 0; i < arr.length; i++) if (arr[i].id === id) return arr[i];
    return null;
  }

  function copyOf(o) {
    var c = {};
    for (var k in o) if (Object.prototype.hasOwnProperty.call(o, k)) c[k] = o[k];
    return c;
  }

  function reconcile(serverAnnotations, meta) {
    var arr = serverAnnotations.slice();
    var idx = -1;
    for (var i = 0; i < arr.length; i++) {
      if (arr[i].id === meta.id) {
        idx = i;
        break;
      }
    }
    var mine = findNote(notes, meta.id); // our current authoritative copy

    if (meta.kind === "create") {
      if (idx < 0 && mine) arr.push(mine);
    } else if (meta.kind === "edit") {
      if (idx >= 0 && mine) {
        var edited = copyOf(arr[idx]);
        edited.body = mine.body;
        if (mine.updated) edited.updated = mine.updated;
        arr[idx] = edited;
      }
    } else if (meta.kind === "delete") {
      if (idx >= 0) arr.splice(idx, 1);
    } else if (meta.kind === "reopen") {
      if (idx >= 0) {
        var reopened = copyOf(arr[idx]);
        reopened.status = "open";
        var replies = (reopened.replies || []).slice();
        var last = replies[replies.length - 1];
        if (!last || last.action !== "reopen") {
          var mineReplies = mine && mine.replies ? mine.replies : [];
          var mineLast = mineReplies[mineReplies.length - 1];
          replies.push(
            mineLast && mineLast.action === "reopen"
              ? mineLast
              : { by: "user", created: new Date().toISOString(), action: "reopen", body: "" }
          );
        }
        reopened.replies = replies;
        arr[idx] = reopened;
      }
    }
    return arr;
  }

  // Delete has no annotator API — the panel removes the note parent-side,
  // tells the annotator to drop its highlight/marker, then saves like any
  // other mutation.
  function commitDelete(id) {
    var next = [];
    for (var i = 0; i < notes.length; i++) {
      if (notes[i].id !== id) next.push(notes[i]);
    }
    notes = next;
    if (annotator) {
      try {
        annotator.setNotes(next);
      } catch (_) {}
    }
    scheduleSave(doc, next, { kind: "delete", id: id });
  }

  /* -------------------------------------------------------------- banners */

  function showSaveBanner(text) {
    var banners = $("banners");
    if (!banners) return;
    var b = banners.querySelector(".banner-save");
    if (!b) {
      b = el("span", "banner warn banner-save", text);
      banners.appendChild(b);
    } else {
      b.textContent = text;
    }
  }

  function clearSaveBanner() {
    var banners = $("banners");
    var b = banners && banners.querySelector(".banner-save");
    if (b) b.parentNode.removeChild(b);
  }

  function setUnanchoredBanner(n) {
    var banners = $("banners");
    if (!banners) return;
    var b = banners.querySelector(".banner-unanchored");
    if (n > 0) {
      var text =
        "⚠ " + n + " note" + (n === 1 ? "" : "s") + " no longer anchor" + (n === 1 ? "s" : "") + " — see panel";
      if (!b) {
        b = el("span", "banner warn banner-unanchored", text);
        banners.appendChild(b);
      } else {
        b.textContent = text;
      }
    } else if (b) {
      b.parentNode.removeChild(b);
    }
  }

  /* -------------------------------------------------------------- header */

  // panelKey fingerprints everything renderPanel actually draws. Selection is
  // deliberately absent from it: hovering a row calls setActive, which makes
  // the annotator re-render and hand us a fresh state — and rebuilding the
  // panel on that destroys the very row the pointer is over, so the click that
  // follows the hover lands on a detached node and is lost. Selection changes
  // therefore restyle in place; only a real content change redraws.
  function panelKey(rows) {
    var parts = [];
    for (var i = 0; i < rows.length; i++) {
      var r = rows[i];
      parts.push([r.id, r.num, r.status, r.anchored ? 1 : 0, r.updated || "",
        (r.replies || []).length, r.body].join("\u0001"));
    }
    return parts.join("\u0002");
  }

  function onState(state) {
    annotateState = !!state.annotate;
    renderHeader(state);
    var key = panelKey(state.rows || []);
    if (key !== lastPanelKey) {
      lastPanelKey = key;
      renderPanel(state);
    }
    markActiveRow(state.activeId);
  }

  // The selection half of the old full redraw: toggle the class, touch nothing
  // else, so the row under the pointer survives to receive its click.
  function markActiveRow(activeId) {
    var body = $("panelbody");
    if (!body) return;
    var rows = body.querySelectorAll(".note");
    for (var i = 0; i < rows.length; i++) {
      var on = rows[i].getAttribute("data-id") === activeId;
      rows[i].classList.toggle("active", on);
    }
  }

  function renderHeader(state) {
    var open = state.counts.open;
    var pc = $("panelcount");
    if (pc) pc.textContent = String(open);
    var ptc = $("paneltabcount");
    if (ptc) ptc.textContent = String(open);

    var ab = $("annobtn");
    if (ab) ab.setAttribute("aria-pressed", state.annotate ? "true" : "false");
    var pb = $("panelbtn");
    if (pb) pb.setAttribute("aria-pressed", panelOpen ? "true" : "false");

    setUnanchoredBanner(state.counts.unanchored);
  }

  /* --------------------------------------------------------------- panel */

  function renderPanel(state) {
    var body = $("panelbody");
    if (!body) return;
    body.innerHTML = "";

    var rows = state.rows || [];
    if (!rows.length) {
      body.appendChild(el("p", "panel-empty", "No notes on this document yet."));
      return;
    }

    var open = [],
      resolved = [],
      unanchored = [];
    for (var i = 0; i < rows.length; i++) {
      var r = rows[i];
      if (!r.anchored) unanchored.push(r);
      else if (r.status === "open") open.push(r);
      else resolved.push(r);
    }

    for (var a = 0; a < open.length; a++) body.appendChild(noteRow(open[a], "open"));

    if (resolved.length) {
      body.appendChild(el("p", "group-head", "resolved (" + resolved.length + ")"));
      body.appendChild(
        el(
          "p",
          "group-note",
          "Closed by the agent via scratchpad notes resolve. Reopen if it isn't actually fixed; delete to accept."
        )
      );
      for (var b = 0; b < resolved.length; b++) body.appendChild(noteRow(resolved[b], "resolved"));
    }

    if (unanchored.length) {
      body.appendChild(el("p", "group-head", "unanchored (" + unanchored.length + ")"));
      body.appendChild(
        el(
          "p",
          "group-note",
          "These anchors no longer resolve in this document. Kept with what was stored, so the note still makes sense."
        )
      );
      for (var c = 0; c < unanchored.length; c++) body.appendChild(noteRow(unanchored[c], "unanchored"));
    }
  }

  function noteRow(r, kind) {
    var isUnanchored = kind === "unanchored";
    var isResolved = kind === "resolved";
    var row = document.createElement("div");
    row.className = "note" + (isUnanchored ? " unanchored" : "") + (isResolved ? " resolved" : "");
    row.setAttribute("data-id", r.id);
    if (!isUnanchored) {
      row.setAttribute("tabindex", "0");
      row.setAttribute("role", "button");
    }

    var top = document.createElement("div");
    top.className = "note-top";
    var numLabel = isResolved ? "✓" : r.num === null || r.num === undefined ? "–" : String(r.num);
    top.appendChild(el("span", "note-num" + (isResolved ? " done" : ""), numLabel));
    top.appendChild(el("span", "note-doc", relTime(r.created) + (r.updated ? " · edited" : "")));
    row.appendChild(top);

    row.appendChild(el("p", "note-body", r.body));

    if (r.replies && r.replies.length) {
      var last = r.replies[r.replies.length - 1];
      var who = last.by === "agent" ? "agent" : "you";
      var act = last.action ? (last.action === "resolve" ? " resolved" : " reopened") : "";
      row.appendChild(el("p", "note-reply " + last.by, who + act + (last.body ? ": " + last.body : "")));
    }

    if (r.target) {
      var anchorLabel =
        r.target.type === "element"
          ? "element " + r.target.selector
          : r.target.quote
          ? 'text "' + r.target.quote.exact + '"'
          : "";
      if (anchorLabel) row.appendChild(el("p", "note-meta", anchorLabel));

      if (isUnanchored) {
        var stored =
          r.target.type === "element"
            ? "stored fingerprint: " + (r.target.fingerprint || "")
            : "stored quote: " +
              (r.target.quote
                ? (r.target.quote.prefix || "") + "[" + r.target.quote.exact + "]" + (r.target.quote.suffix || "")
                : "");
        row.appendChild(el("span", "stored", stored));
      }
    }

    var del = document.createElement("button");
    del.type = "button";
    del.className = "note-del";
    del.textContent = "✕";
    del.title = isResolved ? "accept the fix and remove the note" : "delete note";
    del.setAttribute("data-del", r.id);
    row.appendChild(del);

    return row;
  }

  function relTime(iso) {
    var t = Date.parse(iso);
    if (isNaN(t)) return "";
    var diff = (Date.now() - t) / 1000;
    if (diff < 45) return "just now";
    if (diff < 3600) return Math.round(diff / 60) + "m ago";
    if (diff < 86400) return Math.round(diff / 3600) + "h ago";
    if (diff < 7 * 86400) return Math.round(diff / 86400) + "d ago";
    var d = new Date(t);
    return d.toISOString().slice(0, 10);
  }

  /* ------------------------------------------------------------ controls */

  function togglePanel() {
    panelOpen = !panelOpen;
    var panel = $("panel"),
      tab = $("paneltab"),
      btn = $("panelbtn");
    if (panel) panel.className = "panel" + (panelOpen ? "" : " collapsed");
    if (tab) tab.hidden = panelOpen;
    if (btn) btn.setAttribute("aria-pressed", panelOpen ? "true" : "false");
    // Collapsing/expanding changes the iframe's width, so every marker
    // position needs re-measuring once the CSS width transition settles.
    window.setTimeout(function () {
      if (annotator) {
        try {
          annotator.refresh();
        } catch (_) {}
      }
    }, 200);
  }

  // Idiomorph reuses DOM nodes whose ids match across a swap, so the overlay's
  // buttons and panel survive a doc switch — and a second addEventListener on
  // the same node would run the handler twice per click, toggling straight
  // back off. Bind once per node instead. Every handler below reads module
  // state only (never a per-attach closure), so one binding for the node's
  // lifetime stays correct across attaches.
  function wire(node, type, fn) {
    if (!node) return;
    var key = "__annoWired_" + type;
    if (node[key]) return;
    node[key] = true;
    node.addEventListener(type, fn);
  }

  function wireControls() {
    wire($("annobtn"), "click", function () {
      if (!annotator) return;
      try {
        annotator.setAnnotate(!annotateState);
      } catch (_) {}
    });

    wire($("panelbtn"), "click", togglePanel);
    wire($("panelclose"), "click", togglePanel);
    wire($("paneltab"), "click", togglePanel);

    var panelbody = $("panelbody");
    wire(panelbody, "click", function (e) {
      var del = e.target.closest && e.target.closest("[data-del]");
      if (del) {
        e.stopPropagation();
        commitDelete(del.getAttribute("data-del"));
        return;
      }
      var row = e.target.closest && e.target.closest(".note");
      if (!row || row.className.indexOf("unanchored") >= 0) return;
      if (annotator) {
        try {
          annotator.focus(row.getAttribute("data-id"));
        } catch (_) {}
      }
    });
    wire(panelbody, "keydown", function (e) {
      if (e.key !== "Enter" && e.key !== " ") return;
      var row = e.target.closest && e.target.closest(".note");
      if (!row || row.className.indexOf("unanchored") >= 0) return;
      e.preventDefault();
      if (annotator) {
        try {
          annotator.focus(row.getAttribute("data-id"));
        } catch (_) {}
      }
    });
    wire(panelbody, "mouseover", function (e) {
      var row = e.target.closest && e.target.closest(".note");
      if (!row || !annotator) return;
      try {
        annotator.setActive(row.getAttribute("data-id"));
      } catch (_) {}
    });
  }


  // e/n only act while the viewer holds an annotatable document, and never
  // while the human is typing somewhere (an input, textarea, or a bubble's
  // contenteditable body inside the iframe reports isContentEditable, but
  // that event never reaches this document listener in the first place —
  // this guard is for chrome-level focus, e.g. a doc-switcher chip).
  document.addEventListener("keydown", function (e) {
    if (!frame || !doc) return;
    var tag = (e.target.tagName || "").toLowerCase();
    var typing = tag === "textarea" || tag === "input" || e.target.isContentEditable;
    if (typing) return;
    if (e.key === "e" || e.key === "E") {
      var ab = $("annobtn");
      if (ab) ab.click();
    } else if (e.key === "n" || e.key === "N") {
      var pb = $("panelbtn");
      if (pb) pb.click();
    }
    // Escape is deliberately not bound here: viewer.js owns Escape for the
    // overlay itself, and the annotator handles the bubble-close case inside
    // the iframe.
  });
})();
