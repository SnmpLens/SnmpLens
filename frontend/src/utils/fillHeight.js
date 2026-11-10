/**
 * A Svelte action giving an element the height left below it in the window, so
 * that a table or a drawing ends at the bottom edge instead of past it.
 *
 * A constant subtracted from 100vh is right for one height of what sits above,
 * and what sits above changes: a row of chips wraps, a hint appears, a warning is
 * shown. So the height is measured, again when the window is resized and when
 * anything in the element's parent changes size.
 *
 *   <div use:fillHeight={{ prop: 'maxHeight', min: 240 }}>
 */
export function fillHeight(node, options) {
  let opts = settle(options);
  let frame = 0;
  const scroller = scrollParent(node);

  function measure() {
    cancelAnimationFrame(frame);
    frame = requestAnimationFrame(() => {
      // Where the element would be with nothing scrolled: the height left is the
      // window's, not what happens to be in view.
      const top = node.getBoundingClientRect().top + (scroller ? scroller.scrollTop : 0);
      const px = Math.max(opts.min, Math.floor(window.innerHeight - top - opts.margin));
      node.style[opts.prop] = `${px}px`;
    });
  }

  const observer = typeof ResizeObserver === 'function' ? new ResizeObserver(measure) : null;
  observer?.observe(node.parentElement || node);
  window.addEventListener('resize', measure);
  measure();

  return {
    update(next) {
      opts = settle(next);
      measure();
    },
    destroy() {
      cancelAnimationFrame(frame);
      observer?.disconnect();
      window.removeEventListener('resize', measure);
    },
  };
}

/**
 * Resolves once the browser has laid out and painted twice: after the frame in
 * which an element given `fillHeight` is first measured.
 */
export function afterLayout() {
  return new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
}

function settle(o) {
  return { prop: 'height', min: 240, margin: 16, ...(o || {}) };
}

function scrollParent(node) {
  for (let el = node.parentElement; el; el = el.parentElement) {
    const y = getComputedStyle(el).overflowY;
    if (y === 'auto' || y === 'scroll') return el;
  }
  return null;
}
