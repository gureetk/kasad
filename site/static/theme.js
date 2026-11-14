// Runs before the page is drawn, so that a theme chosen on an earlier visit is
// in place from the first frame. With no choice stored, the page follows the
// system setting through CSS alone. It also marks the page as having scripts,
// so that controls which need them can be shown from the start.
(function () {
  var root = document.documentElement;
  root.classList.add('js');

  var choice = null;
  try {
    choice = localStorage.getItem('kasad-theme');
  } catch (e) {
    // Storage can be switched off. The page then follows the system.
  }
  if (choice !== 'light' && choice !== 'dark') {
    return;
  }
  root.setAttribute('data-theme', choice);
  var colour = choice === 'dark' ? '#111814' : '#EEF3EE';
  var metas = document.querySelectorAll('meta[name="theme-color"]');
  for (var i = 0; i < metas.length; i++) {
    metas[i].setAttribute('content', colour);
  }
})();
