// snapshot.js — indexed interactive-element digest builder.
//
// Evaluated by the Go side as `(function(opts){...})({maxEls, maxText, gen})`.
// Walks the DOM (including shadow roots and same-origin iframes), filters to
// visible interactive elements, stores live element references in
// window.__forge_els so tools can act by index, and returns a compact
// serializable snapshot. This file is a bare function expression, not a
// standalone script.
function (opts) {
	opts = opts || {};
	var maxEls = opts.maxEls || 100;
	var maxText = opts.maxText || 1200;
	var gen = opts.gen || 0;

	var els = [];
	var infos = [];

	var SKIP_TAGS = { script: 1, style: 1, noscript: 1, template: 1, meta: 1, link: 1, head: 1 };
	var INTERACTIVE_ROLES = {
		button: 1, link: 1, tab: 1, menuitem: 1, checkbox: 1, radio: 1,
		combobox: 1, switch: 1, option: 1, textbox: 1, searchbox: 1, slider: 1
	};
	var PROTECTED_AUTOCOMPLETE = /^cc-|^(current|new)-password$/;

	function isVisible(el) {
		try {
			if (el.checkVisibility && !el.checkVisibility()) return false;
		} catch (e) { /* fall through to rect check */ }
		var r = el.getBoundingClientRect();
		return r.width > 0 && r.height > 0;
	}

	function isInteractive(el) {
		var tag = el.tagName.toLowerCase();
		if (tag === 'a') return el.hasAttribute('href');
		if (tag === 'button' || tag === 'select' || tag === 'textarea' || tag === 'summary') return true;
		if (tag === 'input') return (el.getAttribute('type') || '').toLowerCase() !== 'hidden';
		var ce = el.getAttribute('contenteditable');
		if (ce !== null && ce !== 'false') return true;
		if (el.hasAttribute('onclick')) return true;
		return !!INTERACTIVE_ROLES[(el.getAttribute('role') || '').toLowerCase()];
	}

	function isProtected(el) {
		if (el.tagName.toLowerCase() !== 'input') return false;
		if ((el.getAttribute('type') || '').toLowerCase() === 'password') return true;
		return PROTECTED_AUTOCOMPLETE.test((el.getAttribute('autocomplete') || '').toLowerCase());
	}

	function clean(s, cap) {
		s = (s || '').replace(/\s+/g, ' ').trim();
		if (s.length > cap) s = s.slice(0, cap - 3) + '...';
		return s;
	}

	function accessibleName(el) {
		var name = el.getAttribute('aria-label') || '';
		if (!name) {
			var lb = el.getAttribute('aria-labelledby');
			if (lb) {
				name = lb.split(/\s+/).map(function (id) {
					var n = el.ownerDocument.getElementById(id);
					return n ? (n.innerText || n.textContent || '') : '';
				}).join(' ');
			}
		}
		if (!name) {
			var tag = el.tagName.toLowerCase();
			if (tag === 'input' || tag === 'textarea') {
				name = (el.labels && el.labels.length ? el.labels[0].innerText : '') ||
					el.getAttribute('placeholder') || el.getAttribute('name') || '';
			} else if (tag === 'select') {
				name = (el.labels && el.labels.length ? el.labels[0].innerText : '') ||
					el.getAttribute('name') || '';
			} else if (tag === 'img') {
				name = el.getAttribute('alt') || '';
			} else {
				name = el.innerText || el.textContent || '';
			}
		}
		if (!name) name = el.getAttribute('title') || el.getAttribute('value') || '';
		return clean(name, 80);
	}

	function roleOf(el) {
		var tag = el.tagName.toLowerCase();
		var role = (el.getAttribute('role') || '').toLowerCase();
		if (tag === 'a') return 'link';
		if (tag === 'input') return 'input';
		if (tag === 'button' || tag === 'select' || tag === 'textarea' || tag === 'summary') return tag;
		var ce = el.getAttribute('contenteditable');
		if (ce !== null && ce !== 'false') return 'editable';
		return role || tag;
	}

	// viewport-relative center of an element, accounting for same-origin
	// iframe nesting (rects inside a frame are relative to that frame).
	function viewportCenter(el) {
		var r = el.getBoundingClientRect();
		var x = r.left + r.width / 2;
		var y = r.top + r.height / 2;
		try {
			var win = el.ownerDocument.defaultView;
			while (win && win.frameElement) {
				var fr = win.frameElement.getBoundingClientRect();
				x += fr.left;
				y += fr.top;
				win = win.parent;
			}
		} catch (e) { /* cross-origin parent: keep local coords */ }
		return { x: x, y: y };
	}

	function record(el) {
		var idx = els.length;
		els.push(el);
		var c = viewportCenter(el);
		var info = {
			i: idx,
			tag: el.tagName.toLowerCase(),
			role: roleOf(el),
			name: accessibleName(el),
			protected: isProtected(el),
			cx: Math.round(c.x * 10) / 10,
			cy: Math.round(c.y * 10) / 10
		};
		if (info.tag === 'a') {
			info.href = clean(el.getAttribute('href') || '', 120);
		}
		if (info.tag === 'input') {
			info.inputType = (el.getAttribute('type') || 'text').toLowerCase();
			if (info.inputType === 'checkbox' || info.inputType === 'radio') {
				info.checked = !!el.checked;
			}
		}
		if (info.tag === 'select') {
			var options = [];
			for (var i = 0; i < el.options.length && options.length < 10; i++) {
				options.push(clean(el.options[i].text, 40));
			}
			if (el.options.length > 10) options.push('… +' + (el.options.length - 10) + ' more');
			info.options = options;
			if (el.selectedOptions && el.selectedOptions.length) {
				info.value = clean(el.selectedOptions[0].text, 40);
			}
		}
		infos.push(info);
	}

	function visit(el) {
		if (!el || el.nodeType !== 1) return;
		var tag = el.tagName.toLowerCase();
		if (SKIP_TAGS[tag]) return;

		if (tag === 'iframe' || tag === 'frame') {
			try {
				var doc = el.contentDocument;
				if (doc && doc.body) visit(doc.body);
			} catch (e) { /* cross-origin iframe: skip */ }
			return;
		}

		if (isInteractive(el) && isVisible(el)) record(el);

		if (el.shadowRoot) {
			for (var s = 0; s < el.shadowRoot.children.length; s++) visit(el.shadowRoot.children[s]);
		}
		for (var i = 0; i < el.children.length; i++) visit(el.children[i]);
	}

	visit(document.body || document.documentElement);

	// Side effects: the index → element map and helpers the interaction
	// tools use. A navigation wipes these, which is exactly the staleness
	// signal (window.__forge_gen becomes undefined).
	window.__forge_els = els;
	window.__forge_gen = gen;
	window.__forge_center = function (i) {
		var el = window.__forge_els[i];
		if (!el || !el.isConnected) return null;
		try { el.scrollIntoView({ block: 'center', inline: 'nearest', behavior: 'instant' }); } catch (e) { el.scrollIntoView(); }
		return viewportCenter(el);
	};
	window.__forge_protected = function (i) {
		var el = window.__forge_els[i];
		return !el || !el.isConnected || isProtected(el);
	};
	window.__forge_select_all = function (i) {
		var el = window.__forge_els[i];
		if (!el || !el.isConnected) return false;
		el.focus();
		if (el.select) {
			el.select();
		} else {
			var range = el.ownerDocument.createRange();
			range.selectNodeContents(el);
			var sel = el.ownerDocument.defaultView.getSelection();
			sel.removeAllRanges();
			sel.addRange(range);
		}
		return true;
	};
	window.__forge_select_option = function (i, label) {
		var el = window.__forge_els[i];
		if (!el || el.tagName.toLowerCase() !== 'select') return false;
		for (var j = 0; j < el.options.length; j++) {
			var o = el.options[j];
			if (o.text.trim() === label || o.value === label) {
				el.selectedIndex = j;
				el.dispatchEvent(new Event('input', { bubbles: true }));
				el.dispatchEvent(new Event('change', { bubbles: true }));
				return true;
			}
		}
		return false;
	};
	window.__forge_dispatch_change = function (i) {
		var el = window.__forge_els[i];
		if (!el || !el.isConnected) return false;
		el.dispatchEvent(new Event('change', { bubbles: true }));
		return true;
	};

	var bodyText = clean(document.body ? document.body.innerText : '', 10000000);
	return {
		url: location.href,
		title: document.title || '',
		gen: gen,
		els: infos.slice(0, maxEls),
		totalEls: infos.length,
		text: bodyText.slice(0, maxText),
		textLen: bodyText.length
	};
}
