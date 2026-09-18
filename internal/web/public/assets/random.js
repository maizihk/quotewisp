(function () {
	var contentEl = document.getElementById('random-content');
	var metaEl = document.getElementById('random-meta');
	var btn = document.getElementById('random-btn');
	if (!contentEl) {
		return;
	}

	function showHint() {
		contentEl.textContent = '暂时无法加载随机语句，请稍后再试或通过接口文档查看用法。';
		if (metaEl) {
			metaEl.textContent = '';
		}
	}

	function render(data) {
		contentEl.textContent = data.content || '';
		if (!metaEl) {
			return;
		}
		var parts = [];
		if (data.source) {
			parts.push('出处：' + data.source);
		}
		if (data.author) {
			parts.push('作者：' + data.author);
		}
		if (data.category) {
			parts.push('分类：' + data.category);
		}
		metaEl.textContent = parts.join(' · ');
	}

	function loadRandom() {
		fetch('/api/v1/sentences/random')
			.then(function (resp) {
				if (!resp.ok) {
					throw new Error('bad status');
				}
				return resp.json();
			})
			.then(function (payload) {
				if (!payload || !payload.data) {
					throw new Error('bad payload');
				}
				render(payload.data);
			})
			.catch(showHint);
	}

	if (btn) {
		btn.addEventListener('click', loadRandom);
	}
	loadRandom();
})();
