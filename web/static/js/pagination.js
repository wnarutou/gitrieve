(function (root, factory) {
    const pagination = factory();
    if (typeof module === 'object' && module.exports) module.exports = pagination;
    else root.GitrievePagination = pagination;
}(typeof globalThis !== 'undefined' ? globalThis : this, function () {
    function normalizePage(page, pages) {
        return Math.min(Math.max(1, page), Math.max(1, pages));
    }

    function pageItems(page, pages) {
        if (pages <= 7) {
            return Array.from({ length: pages }, (_, index) => index + 1);
        }

        const visible = new Set([1, pages, page - 1, page, page + 1]);
        if (page <= 3) [2, 3, 4].forEach(value => visible.add(value));
        if (page >= pages - 2) [pages - 3, pages - 2, pages - 1].forEach(value => visible.add(value));

        const numbers = Array.from(visible)
            .filter(value => value >= 1 && value <= pages)
            .sort((a, b) => a - b);
        const items = [];
        numbers.forEach((value, index) => {
            if (index > 0 && value - numbers[index - 1] > 1) items.push('ellipsis');
            items.push(value);
        });
        return items;
    }

    function pageButtonsHTML(page, pages, idPrefix) {
        return pageItems(page, pages).map((item, index) => {
            if (item === 'ellipsis') {
                return `<span class="pg-ellipsis" aria-hidden="true">&hellip;</span>`;
            }
            const current = item === page;
            return `<button class="btn btn-sm pg-page${current ? ' active' : ''}" id="pg-page-${idPrefix}-${item}" data-page="${item}" aria-label="Page ${item}"${current ? ' aria-current="page" disabled' : ''}>${item}</button>`;
        }).join('');
    }

    function paginationHTML(page, pages, total, idPrefix, showPageNumbers) {
        const pageButtons = showPageNumbers
            ? `<span class="pg-pages" role="group" aria-label="Page navigation">${pageButtonsHTML(page, pages, idPrefix)}</span>`
            : '';
        return `
        <div class="pagination">
            <button class="btn btn-sm" id="pg-prev-${idPrefix}" ${page > 1 ? '' : 'disabled'}>Prev</button>
            ${pageButtons}
            <button class="btn btn-sm" id="pg-next-${idPrefix}" ${page < pages ? '' : 'disabled'}>Next</button>
            <span class="pg-info">Page ${page} of ${pages} (${total} total)</span>
        </div>`;
    }

    return { normalizePage, pageItems, paginationHTML };
}));
