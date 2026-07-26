// Preview and viewer iframes stay invisible until their document has loaded,
// then fade in. Between navigation commit and the embedded page's first paint
// the browser clears the frame to an opaque canvas — white for light-default
// documents — so a bare iframe flashes white over the dark UI no matter what
// background sits behind it. While hidden, the themed .preview/.overlay
// surface shows instead.
//
// The reveal itself is an inline onload= on each iframe (see the templates):
// it works in every engine and survives morph swaps. This file handles the
// edges around it:
//  - a WeakSet remembers which elements truly loaded, so if a morph swap
//    syncs attributes from a server fragment and strips the runtime .loaded
//    class from a preserved iframe (whose document will never fire load
//    again), the observer can restore it;
//  - a morph that bumps an iframe's src (artifact edited, ?v= changed)
//    reloads it in place, so hide it for the duration;
//  - htmx history restores re-insert snapshotted HTML with .loaded baked in
//    while the recreated iframes still have to reload, so strip it there.
(function () {
  var loaded = new WeakSet();

  function reveal(e) {
    var t = e.target;
    if (t && t.tagName === "IFRAME") {
      loaded.add(t);
      t.classList.add("loaded");
    }
  }
  document.addEventListener("load", reveal, true);
  document.addEventListener("error", reveal, true);

  new MutationObserver(function (muts) {
    muts.forEach(function (m) {
      var el = m.target;
      if (el.tagName !== "IFRAME") return;
      if (m.attributeName === "src") {
        loaded.delete(el);
        el.classList.remove("loaded");
      } else if (loaded.has(el) && !el.classList.contains("loaded")) {
        el.classList.add("loaded");
      }
    });
  }).observe(document.documentElement, {
    subtree: true,
    attributes: true,
    attributeFilter: ["src", "class"],
  });

  document.addEventListener("htmx:historyRestore", function () {
    var frames = document.querySelectorAll("iframe.loaded");
    for (var i = 0; i < frames.length; i++) {
      if (!loaded.has(frames[i])) frames[i].classList.remove("loaded");
    }
  });
})();
