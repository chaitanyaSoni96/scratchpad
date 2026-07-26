// History glue for the artifact overlay. Opening an artifact is an htmx swap
// into #viewer, invisible to the browser's session history; this pushes an
// entry with a ?view= URL for it so Back closes the overlay, Forward reopens
// it, reloads restore it, and the URL can be shared as a deep link.
(function () {
  var restoring = false; // current swap comes from history, not a user click
  var FRAG = "/fragments/viewer/";

  function viewer() {
    return document.getElementById("viewer");
  }

  function clear() {
    var v = viewer();
    if (v) v.innerHTML = "";
  }

  function restore(path) {
    restoring = true;
    htmx
      .ajax("GET", path, { target: "#viewer", swap: "innerHTML" })
      .finally(function () {
        restoring = false;
      });
  }

  // Overlay deep link: the viewed file's own path under /p/. The server
  // renders such paths as the parent folder page with the overlay open.
  function viewerURL(rel) {
    return "/p/" + encodeURIComponent(rel).replace(/%2F/gi, "/");
  }

  // Shared close path for the ✕ button, Esc, and anything else: clear the
  // overlay right away for instant feedback, then pop the history entry we
  // pushed for it so Back/Forward and the URL stay consistent with the screen.
  window.viewerClose = function () {
    var v = viewer();
    if (!v || !v.firstElementChild) return;
    v.innerHTML = "";
    if (history.state && history.state.viewer) history.back();
  };

  document.addEventListener("htmx:afterSwap", function (e) {
    if (e.detail.target !== viewer() || restoring) return;
    var path = new URL(e.detail.xhr.responseURL).pathname;
    var rel = decodeURIComponent(path.slice(FRAG.length));
    // Opening on top of an already-open viewer replaces rather than stacks.
    if (history.state && history.state.viewer) {
      history.replaceState({ viewer: path }, "", viewerURL(rel));
    } else {
      history.pushState({ viewer: path }, "", viewerURL(rel));
    }
  });

  window.addEventListener("popstate", function (e) {
    if (e.state && e.state.viewer) restore(e.state.viewer);
    else clear();
  });

  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") viewerClose();
  });

  // A document can load with the viewer open in two ways: its history entry
  // says so (reload while open, or Back from a page navigated to from the
  // overlay), or it is a fresh deep link — a /p/ file path the server
  // rendered with data-view set, or a legacy ?view= query. For a deep link,
  // synthesize the normal two-entry stack (clean list below, viewer on top)
  // so Back and close land on the list instead of leaving the site.
  document.addEventListener("DOMContentLoaded", function () {
    if (history.state && history.state.viewer) {
      restore(history.state.viewer);
      return;
    }
    var v = viewer();
    var rel = v && v.getAttribute("data-view");
    var clean;
    if (rel) {
      var folder = v.getAttribute("data-folder");
      clean = folder ? "/p/" + folder : "/";
    } else {
      rel = new URLSearchParams(location.search).get("view");
      clean = location.pathname;
    }
    if (rel) {
      var path = FRAG + rel;
      history.replaceState(null, "", clean);
      history.pushState({ viewer: path }, "", viewerURL(rel));
      restore(path);
    }
  });
})();
