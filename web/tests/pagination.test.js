const test = require('node:test');
const assert = require('node:assert/strict');

const { normalizePage, pageItems, paginationHTML } = require('../static/js/pagination.js');

test('pageItems keeps first, last, current, and neighboring pages for a long list', () => {
    assert.deepEqual(pageItems(6, 12), [1, 'ellipsis', 5, 6, 7, 'ellipsis', 12]);
});

test('pageItems shows every page when the list is short', () => {
    assert.deepEqual(pageItems(3, 5), [1, 2, 3, 4, 5]);
});

test('paginationHTML renders repository page buttons and marks the current page', () => {
    const html = paginationHTML(3, 5, 83, 'repos', true);

    assert.match(html, /Page 3 of 5 \(83 total\)/);
    assert.match(html, /role="group" aria-label="Page navigation"/);
    assert.match(html, /data-page="2"[^>]*>2<\/button>/);
    assert.match(html, /class="btn btn-sm pg-page active"[^>]*data-page="3"[^>]*aria-current="page"[^>]*disabled[^>]*>3<\/button>/);
    assert.match(html, /data-page="4"[^>]*>4<\/button>/);
    assert.match(html, /class="pg-pages"[\s\S]*id="pg-next-repos"[\s\S]*class="pg-info"/);
});

test('paginationHTML leaves page buttons out when they are not requested', () => {
    const html = paginationHTML(2, 5, 83, 'jobs', false);

    assert.doesNotMatch(html, /class="[^"]*pg-page/);
});

test('normalizePage moves an out-of-range repository page to the nearest valid page', () => {
    assert.equal(normalizePage(9, 4), 4);
    assert.equal(normalizePage(0, 4), 1);
});
