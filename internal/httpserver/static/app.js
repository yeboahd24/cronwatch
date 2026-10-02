(() => {
  // Show the end of logs first: that is where failures usually are.
  const scrollToEnd = (root) => {
    root.querySelectorAll('[data-scroll-end]').forEach((el) => { el.scrollTop = el.scrollHeight; });
  };
  scrollToEnd(document);

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
