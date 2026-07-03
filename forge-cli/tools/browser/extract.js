// extract.js — readability-lite content extraction for browser_extract.
//
// Evaluated as `(function(mode, selector){...})("text", "")`. Returns a plain
// string: markdown-ish text, deduplicated links, or selector-scoped HTML.
// Pagination (offset/max_chars) happens on the Go side. This file is a bare
// function expression, not a standalone script.
function (mode, selector) {
	function clean(s) {
		return (s || '').replace(/[ \t ]+/g, ' ').replace(/\s*\n\s*/g, '\n').trim();
	}

	if (mode === 'links') {
		var out = [];
		var seen = {};
		var anchors = document.querySelectorAll('a[href]');
		for (var i = 0; i < anchors.length; i++) {
			var a = anchors[i];
			var href = a.href;
			if (!href || href.indexOf('javascript:') === 0) continue;
			var text = clean(a.innerText).replace(/\n/g, ' ') || href;
			if (text.length > 100) text = text.slice(0, 97) + '...';
			var key = text + '|' + href;
			if (seen[key]) continue;
			seen[key] = 1;
			out.push('[' + text + '](' + href + ')');
		}
		return out.join('\n');
	}

	if (mode === 'html') {
		var target = selector ? document.querySelector(selector) : document.documentElement;
		if (!target) return '';
		return target.outerHTML;
	}

	// mode === 'text': structured walk emitting markdown-ish blocks; falls
	// back to innerText when the walk yields nearly nothing (canvas apps etc).
	var SKIP = { script: 1, style: 1, noscript: 1, template: 1, svg: 1, nav: 1, header: 1, footer: 1, aside: 1, iframe: 1, frame: 1 };
	var root = selector ? document.querySelector(selector) : document.body;
	if (!root) return '';

	var blocks = [];

	function push(s) {
		s = clean(s);
		if (s) blocks.push(s);
	}

	function visible(el) {
		try {
			if (el.checkVisibility && !el.checkVisibility()) return false;
		} catch (e) { /* older engines */ }
		return true;
	}

	function tableToText(t) {
		var rows = [];
		for (var r = 0; r < t.rows.length && r < 100; r++) {
			var cells = [];
			for (var c = 0; c < t.rows[r].cells.length; c++) {
				cells.push(clean(t.rows[r].cells[c].innerText).replace(/\n/g, ' '));
			}
			rows.push('| ' + cells.join(' | ') + ' |');
			if (r === 0 && t.rows[r].querySelector('th')) {
				rows.push('|' + new Array(cells.length + 1).join(' --- |'));
			}
		}
		return rows.join('\n');
	}

	function visit(el) {
		if (!el || el.nodeType !== 1) return;
		var tag = el.tagName.toLowerCase();
		if (SKIP[tag] || !visible(el)) return;

		var m = /^h([1-6])$/.exec(tag);
		if (m) {
			push(new Array(+m[1] + 1).join('#') + ' ' + el.innerText);
			return;
		}
		switch (tag) {
			case 'p':
			case 'blockquote':
				push(tag === 'blockquote' ? '> ' + clean(el.innerText).replace(/\n/g, '\n> ') : el.innerText);
				return;
			case 'li':
				push('- ' + clean(el.innerText).replace(/\n/g, ' '));
				return;
			case 'pre':
				push('```\n' + (el.innerText || '').replace(/\s+$/, '') + '\n```');
				return;
			case 'table':
				push(tableToText(el));
				return;
			case 'dt':
				push('**' + clean(el.innerText).replace(/\n/g, ' ') + '**');
				return;
			case 'dd':
				push(': ' + clean(el.innerText).replace(/\n/g, ' '));
				return;
		}

		if (el.shadowRoot) {
			for (var s = 0; s < el.shadowRoot.children.length; s++) visit(el.shadowRoot.children[s]);
		}
		for (var i = 0; i < el.children.length; i++) visit(el.children[i]);
	}

	visit(root);
	var structured = blocks.join('\n\n');
	var fallback = clean(root.innerText);
	// The structured walk misses div-soup sites that put text in bare divs;
	// prefer it only when it captured a reasonable share of the page text.
	if (structured.length >= fallback.length / 2) return structured;
	return fallback;
}
