// The breadcrumb sits at the top of the page, so a sibling popover opening
// upward can extend past the viewport. Hidden columns still have layout, so
// measure the .above column and pin it to the viewport top when it would
// clip; it then overlays the header downward instead of vanishing. Runs both
// when the fragment swaps in (the pointer may sit still on the crumb) and on
// later hovers (cheap and idempotent).
(function () {
  function clamp(wrap) {
    if (!wrap) return;
    wrap.querySelectorAll(".sib-col.above").forEach(function (col) {
      var top = col.getBoundingClientRect().top;
      if (top >= 8) return;
      col.style.bottom = "auto";
      col.style.top = 8 - wrap.getBoundingClientRect().top + "px";
    });
  }
  document.addEventListener("htmx:afterSwap", function (e) {
    if (e.detail.target.classList && e.detail.target.classList.contains("sibs"))
      clamp(e.detail.target.closest(".crumb-wrap"));
  });
  document.addEventListener("mouseover", function (e) {
    if (e.target.closest) clamp(e.target.closest(".crumb-wrap"));
  });
})();
