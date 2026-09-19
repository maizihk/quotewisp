(function () {
	'use strict';
	var contentEl = document.getElementById('random-content');
	var metaEl = document.getElementById('random-meta');
	var statusEl = document.getElementById('random-status');
	var randomBtn = document.getElementById('random-btn');
	var copyBtn = document.getElementById('copy-btn');
	var requestNumber = 0;
	var controller = null;
	var currentText = '';
	var copyTimer = null;
	if (!contentEl || !randomBtn) return;

	function categoryNames() {
		var el = document.getElementById('category-names');
		if (!el) return {};
		try { return JSON.parse(el.textContent) || {}; } catch (error) { return {}; }
	}
	var names = categoryNames();
	function lengthClass(value) {
		var length = Array.from(value).length;
		if (length > 160) return 'xlong';
		if (length > 80) return 'long';
		if (length > 40) return 'medium';
		return 'short';
	}
	function setContent(value) {
		contentEl.dataset.length = lengthClass(value);
		contentEl.textContent = value;
		contentEl.scrollTop = 0;
	}
	function setStatus(message) { if (statusEl) statusEl.textContent = message; }
	function setLoading(loading) {
		randomBtn.disabled = loading;
		randomBtn.textContent = loading ? '寻找中…' : '换一句';
		copyBtn.disabled = loading || !currentText;
		if (loading) copyBtn.textContent = '复制';
	}
	function render(sentence) {
		if (copyTimer) window.clearTimeout(copyTimer);
		currentText = sentence.content;
		copyBtn.textContent = '复制';
		setContent(sentence.content);
		var credit = [];
		if (sentence.author) credit.push(sentence.author);
		if (sentence.source) credit.push('《' + sentence.source + '》');
		var details = [];
		if (credit.length) details.push('—— ' + credit.join(' · '));
		if (sentence.category) details.push(names[sentence.category] || sentence.category);
		metaEl.textContent = details.join('　');
		copyBtn.disabled = false;
		setStatus('');
	}
	function showError(message) {
		if (copyTimer) window.clearTimeout(copyTimer);
		currentText = '';
		copyBtn.textContent = '复制';
		setContent(message);
		metaEl.textContent = '';
		copyBtn.disabled = true;
	}
	function loadRandom() {
		requestNumber += 1;
		var thisRequest = requestNumber;
		if (controller) controller.abort();
		controller = typeof AbortController === 'function' ? new AbortController() : null;
		setLoading(true);
		setStatus('正在读取');
		var url = '/api/v1?max_length=1000';
		fetch(url, controller ? { signal: controller.signal } : {}).then(function (response) {
			if (response.status === 404) {
				var noMatch = new Error('empty'); noMatch.empty = true; throw noMatch;
			}
			if (!response.ok) throw new Error('request failed');
			return response.json();
		}).then(function (payload) {
			if (thisRequest !== requestNumber) return;
			var item = payload && payload.data;
			if (!item || typeof item.content !== 'string' || !item.content) throw new Error('invalid response');
			render(item);
		}).catch(function (error) {
			if (thisRequest !== requestNumber || error.name === 'AbortError') return;
			showError(error.empty ? '暂时还没有句子。' : '暂时没有读到，请稍后再试。');
			setStatus(error.empty ? '暂无句子' : '加载失败');
		}).then(function () {
			if (thisRequest === requestNumber) setLoading(false);
		});
	}
	function fallbackCopy(value) {
		var input = document.createElement('textarea');
		var active = document.activeElement;
		input.value = value;
		input.setAttribute('readonly', '');
		input.style.position = 'fixed'; input.style.opacity = '0';
		var copied = false;
		document.body.appendChild(input);
		try {
			input.select(); input.setSelectionRange(0, input.value.length);
			copied = document.execCommand('copy');
		} finally {
			document.body.removeChild(input);
			if (active && typeof active.focus === 'function') active.focus();
		}
		if (!copied) throw new Error('copy failed');
	}
	function copyCurrent() {
		if (!currentText) return;
		var textToCopy = currentText;
		var copyRequest = requestNumber;
		var task;
		if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
			try { task = navigator.clipboard.writeText(textToCopy); }
			catch (error) { task = Promise.reject(error); }
		}
		else {
			try { fallbackCopy(textToCopy); task = Promise.resolve(); }
			catch (error) { task = Promise.reject(error); }
		}
		task.then(function () {
			if (currentText !== textToCopy || requestNumber !== copyRequest) return;
			copyBtn.textContent = '已复制'; setStatus('句子已复制');
			copyTimer = window.setTimeout(function () { if (currentText === textToCopy && requestNumber === copyRequest) copyBtn.textContent = '复制'; }, 1600);
		}).catch(function () { if (currentText === textToCopy && requestNumber === copyRequest) setStatus('复制失败，请选中文字后手动复制'); });
	}
	randomBtn.addEventListener('click', loadRandom);
	copyBtn.addEventListener('click', copyCurrent);
	contentEl.addEventListener('keydown', function (event) {
		if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || contentEl.scrollHeight <= contentEl.clientHeight) return;
		if (event.key === 'End') { event.preventDefault(); contentEl.scrollTop = contentEl.scrollHeight; }
		if (event.key === 'Home') { event.preventDefault(); contentEl.scrollTop = 0; }
		if (event.key === 'PageDown') { event.preventDefault(); contentEl.scrollTop += contentEl.clientHeight; }
		if (event.key === 'PageUp') { event.preventDefault(); contentEl.scrollTop -= contentEl.clientHeight; }
	});
	loadRandom();
})();
