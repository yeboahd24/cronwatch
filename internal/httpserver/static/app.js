(() => {
  if (!document.getElementById('jobs-body')) return;
  setInterval(async () => {
    if (document.hidden) return;
    try {
      const response = await fetch('/partials/jobs');
      if (!response.ok) return;
      const documentFragment = new DOMParser().parseFromString(await response.text(), 'text/html');
      const updated = documentFragment.getElementById('jobs-body');
      if (updated) document.getElementById('jobs-body')?.replaceWith(updated);
    } catch (_) { /* retry on next poll */ }
  }, 10000);
})();
