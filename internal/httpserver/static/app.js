(() => {
  // Show the end of logs first: that is where failures usually are.
  const scrollToEnd = (root) => {
    root.querySelectorAll('[data-scroll-end]').forEach((el) => { el.scrollTop = el.scrollHeight; });
  };
  scrollToEnd(document);

  const ago = (ms) => {
    const s = Math.max(0, Math.round(ms / 1000));
    if (s < 60) return `${s}s ago`;
    const m = Math.floor(s / 60);
    return m < 60 ? `${m}m ago` : `${Math.floor(m / 60)}h ${m % 60}m ago`;
  };
  const clock = (t) => t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });

  // poll calls refresh every interval while the tab is visible, and keeps
  // status saying when data last arrived. A failed refresh is shown, not
  // ignored, so old data never passes for current. refresh resolves to false
  // to stop polling, and throws when the server cannot be reached.
  const poll = (status, interval, refresh) => {
    let last = Date.now();
    let failing = false;
    let timer = null;
    const show = () => {
      if (!status) return;
      status.classList.toggle('is-stale', failing);
      status.textContent = failing
        ? `Can't reach CronWatch: showing data from ${clock(new Date(last))}, ${ago(Date.now() - last)}. Retrying.`
        : `Updated ${ago(Date.now() - last)}.`;
    };
    const tick = async () => {
      if (document.hidden) return;
      try {
        const more = await refresh();
        last = Date.now();
        failing = false;
        if (more === false) clearInterval(timer);
      } catch (_) {
        failing = true;
      }
      show();
    };
    timer = setInterval(tick, interval);
    setInterval(show, 1000);
    document.addEventListener('visibilitychange', () => { if (!document.hidden) tick(); });
    show();
  };

  // fetchPage fetches url and parses it, throwing unless it succeeded.
  const fetchPage = async (url) => {
    const response = await fetch(url, { cache: 'no-store' });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    return new DOMParser().parseFromString(await response.text(), 'text/html');
  };

  // A running run's output. A reader at the end follows new lines; one who
  // scrolled up stays put. When the run ends, reload to show its result.
  const live = document.querySelector('#run-live[data-live]');
  if (live) {
    poll(document.getElementById('refresh-status'), 5000, async () => {
      const fresh = (await fetchPage(live.dataset.live)).getElementById('run-live');
      if (!fresh) throw new Error('unexpected response');
      if (fresh.hasAttribute('data-finished')) {
        window.location.reload();
        return false;
      }
      const viewer = live.querySelector('.log-viewer');
      const atEnd = !viewer || viewer.scrollHeight - viewer.scrollTop - viewer.clientHeight < 40;
      const scrollTop = viewer ? viewer.scrollTop : 0;
      live.innerHTML = fresh.innerHTML;
      const next = live.querySelector('.log-viewer');
      if (next) next.scrollTop = atEnd ? next.scrollHeight : scrollTop;
      return true;
    });
  }

  // The dashboard's jobs table and recent logs.
  if (document.getElementById('jobs-body')) {
    poll(document.getElementById('refresh-status'), 10000, async () => {
      const fresh = await fetchPage('/partials/dashboard' + location.search);
      for (const id of ['jobs-body', 'recent-logs']) {
        const next = fresh.getElementById(id);
        const current = document.getElementById(id);
        if (next && current) {
          current.replaceWith(next);
          scrollToEnd(next.parentElement || document);
        }
      }
      return true;
    });
  }
})();
