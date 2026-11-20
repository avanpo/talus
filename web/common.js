// Shared status polling, manual sync, and navigation helpers for Talus
async function talusUpdateStatus() {
    try {
        const res = await fetch('/api/status');
        if (!res.ok) return;
        const data = await res.json();
        const dot = document.getElementById('statusDot');
        const text = document.getElementById('statusText');
        const btn = document.getElementById('btnSync');
        if (!dot || !text) return;

        if (data.is_syncing) {
            dot.className = 'status-dot syncing';
            text.innerText = 'Syncing...';
            text.title = 'Synchronization in progress';
            if (btn) {
                btn.disabled = true;
                btn.innerText = 'Syncing...';
            }
        } else {
            dot.className = data.last_sync_error ? 'status-dot error' : 'status-dot';
            text.innerText = data.last_sync_ago ? 'Synced ' + data.last_sync_ago : 'Not synced yet';
            text.title = data.last_sync_error ? 'Last sync failed: ' + data.last_sync_error : (data.last_sync_summary || '');
            if (btn) {
                btn.disabled = false;
                btn.innerText = 'Sync Now';
            }
        }
    } catch (e) {
        console.error(e);
    }
}

async function talusTriggerSync() {
    const btn = document.getElementById('btnSync');
    if (btn) {
        btn.disabled = true;
        btn.innerText = 'Starting...';
    }
    try {
        await fetch('/api/sync', { method: 'POST' });
        setTimeout(talusUpdateStatus, 500);
    } catch (e) {
        alert('Failed to trigger sync: ' + e);
        talusUpdateStatus();
    }
}

function openActivityDetail(activityId) {
    if (!activityId) return;
    window.location.href = `/activities?id=${encodeURIComponent(activityId)}`;
}
