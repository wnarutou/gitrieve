(() => {
    const storageKey = 'gitrieve-theme';
    const systemTheme = window.matchMedia('(prefers-color-scheme: dark)');
    let preference = 'system';

    try {
        const saved = window.localStorage.getItem(storageKey);
        if (saved === 'light' || saved === 'dark') preference = saved;
    } catch (e) { /* Browser storage may be unavailable. */ }

    function applyTheme() {
        document.documentElement.dataset.theme = preference === 'system'
            ? (systemTheme.matches ? 'dark' : 'light')
            : preference;
    }

    // This script runs in the head so the first paint uses the selected theme.
    applyTheme();
    systemTheme.addEventListener('change', applyTheme);

    document.addEventListener('DOMContentLoaded', () => {
        const select = document.getElementById('theme-select');
        select.value = preference;
        select.addEventListener('change', () => {
            preference = select.value;
            applyTheme();
            try {
                window.localStorage.setItem(storageKey, preference);
            } catch (e) { /* Keep the selected theme for this page session. */ }
        });
    });
})();
