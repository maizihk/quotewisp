(function () {
	var KEY = "color-scheme";
	var root = document.documentElement;

	function stored() {
		try {
			return localStorage.getItem(KEY);
		} catch (e) {
			return null;
		}
	}

	function apply(mode) {
		if (mode === "light" || mode === "dark") {
			root.setAttribute("data-theme", mode);
		} else {
			root.removeAttribute("data-theme");
		}
	}

	function effective() {
		var s = stored();
		if (s === "light" || s === "dark") {
			return s;
		}
		return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
	}

	function syncButtons() {
		var next = effective() === "dark" ? "light" : "dark";
		var label = next === "dark" ? "切换到深色" : "切换到浅色";
		var buttons = document.querySelectorAll("[data-theme-toggle]");
		for (var i = 0; i < buttons.length; i++) {
			buttons[i].setAttribute("aria-label", label);
		}
	}

	apply(stored());

	document.addEventListener("click", function (event) {
		var target = event.target;
		if (!target.closest || !target.closest("[data-theme-toggle]")) {
			return;
		}
		event.preventDefault();
		var next = effective() === "dark" ? "light" : "dark";
		try {
			localStorage.setItem(KEY, next);
		} catch (e) {}
		apply(next);
		syncButtons();
	});

	if (document.readyState === "loading") {
		document.addEventListener("DOMContentLoaded", syncButtons);
	} else {
		syncButtons();
	}
})();
