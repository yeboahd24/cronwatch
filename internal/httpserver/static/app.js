(() => {
  // Show the end of logs first: that is where failures usually are.
  const scrollToEnd = (root) => {
    root.querySelectorAll('[data-scroll-end]').forEach((el) => { el.scrollTop = el.scrollHeight; });
  };
  scrollToEnd(document);

  // Refresh a running run's output while the tab is visible. A reader at the
  // end follows new lines; one who scrolled up stays put. When the run ends,
  // reload to show its result.
  const live = document.querySelector('#run-live[data-live]');
  if (live) {
    const update = async () => {
      if (document.hidden) return;
      try {
        const response = await fetch(live.dataset.live, { cache: 'no-store' });
        if (!response.ok) return;
        const fresh = new DOMParser().parseFromString(await response.text(), 'text/html').getElementById('run-live');
        if (!fresh) return;
        if (fresh.hasAttribute('data-finished')) {
          window.location.reload();
          return;
        }
        const viewer = live.querySelector('.log-viewer');
        const atEnd = !viewer || viewer.scrollHeight - viewer.scrollTop - viewer.clientHeight < 40;
        const scrollTop = viewer ? viewer.scrollTop : 0;
        live.innerHTML = fresh.innerHTML;
        const next = live.querySelector('.log-viewer');
        if (next) next.scrollTop = atEnd ? next.scrollHeight : scrollTop;
      } catch (_) { /* retry on the next poll */ }
    };
    setInterval(update, 5000);
    document.addEventListener('visibilitychange', () => { if (!document.hidden) update(); });
  }

  // Refresh the dashboard's jobs table and recent logs while the tab is visible.
  if (!document.getElementById('jobs-body')) return;
  const refresh = async () => {
    if (document.hidden) return;
    try {
      const response = await fetch('/partials/dashboard');
      if (!response.ok) return;
      const fresh = new DOMParser().parseFromString(await response.text(), 'text/html');
      for (const id of ['jobs-body', 'recent-logs']) {
        const next = fresh.getElementById(id);
        const current = document.getElementById(id);
        if (next && current) {
          current.replaceWith(next);
          scrollToEnd(next.parentElement || document);
        }
      }
    } catch (_) { /* retry on next poll */ }
  };
  setInterval(refresh, 10000);
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
})();
