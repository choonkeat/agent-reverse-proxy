(function() {
  document.getElementById('js-check').textContent = 'ASSETS_OK';
  var cookieOk = document.getElementById('cookie-status').textContent === 'ALL_COOKIES_OK';
  var assetsOk = document.getElementById('js-check').textContent === 'ASSETS_OK';
  var cssBg = getComputedStyle(document.getElementById('css-bg-check')).backgroundImage;
  var cssBgOk = cssBg !== 'none';
  if (cookieOk && assetsOk && cssBgOk) {
    document.getElementById('result').textContent = 'ALL STEPS PASSED';
    document.getElementById('result').style.color = 'green';
  } else {
    document.getElementById('result').textContent = 'FAILED: cookies=' + cookieOk + ' assets=' + assetsOk + ' cssBg=' + cssBgOk;
    document.getElementById('result').style.color = 'red';
  }
})();
