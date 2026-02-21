// Playwright conformance test for the reverse proxy example app.
// Usage: TARGET_URL=http://localhost:9876 node test.mjs
//
// When BROWSER_WS_ENDPOINT is set, connects to an existing browser instance
// via CDP instead of launching a local one. In that case, localhost URLs in
// TARGET_URL are rewritten to use the container's IP so the remote browser
// can reach the server.

import { chromium } from 'playwright';
import { networkInterfaces } from 'os';

function getContainerIP() {
  const nets = networkInterfaces();
  for (const name of Object.keys(nets)) {
    for (const iface of nets[name]) {
      if (iface.family === 'IPv4' && !iface.internal) {
        return iface.address;
      }
    }
  }
  return 'localhost';
}

async function getCDPEndpoint(baseUrl) {
  const resp = await fetch(baseUrl.replace('ws://', 'http://').replace('wss://', 'https://') + '/json/version');
  const info = await resp.json();
  return info.webSocketDebuggerUrl;
}

const wsEndpoint = process.env.BROWSER_WS_ENDPOINT || '';
let TARGET_URL = process.env.TARGET_URL || 'http://localhost:9876';

// When using a remote browser, replace localhost with container IP
if (wsEndpoint) {
  const containerIP = getContainerIP();
  TARGET_URL = TARGET_URL.replace('localhost', containerIP).replace('127.0.0.1', containerIP);
  console.log(`Using remote browser at ${wsEndpoint}`);
  console.log(`Rewritten TARGET_URL: ${TARGET_URL}`);
}

async function run() {
  let browser;
  if (wsEndpoint) {
    const cdpUrl = await getCDPEndpoint(wsEndpoint);
    console.log(`CDP endpoint: ${cdpUrl}`);
    browser = await chromium.connectOverCDP(cdpUrl);
  } else {
    browser = await chromium.launch();
  }
  const context = await browser.newContext();
  const page = await context.newPage();

  page.on('pageerror', (err) => {
    console.error(`[PAGE ERROR] ${err.message}`);
  });

  try {
    // Step 1: Relative CSS & Links
    console.log('Step 1: Relative CSS & Links');
    await page.goto(TARGET_URL + '/');
    await page.waitForSelector('#step-info');
    const stepInfo = await page.textContent('#step-info');
    if (!stepInfo.includes('relative CSS works')) {
      throw new Error(`Step 1 failed: unexpected content "${stepInfo}"`);
    }
    await page.click('#next');

    // Step 2: Relative JS
    console.log('Step 2: Relative JS');
    await page.waitForFunction(() => {
      const el = document.getElementById('js-status');
      return el && el.textContent === 'JS_LOADED';
    }, { timeout: 10000 });
    await page.click('#next');

    // Step 3: Relative Fetch
    console.log('Step 3: Relative Fetch');
    await page.waitForFunction(() => {
      const el = document.getElementById('fetch-status');
      return el && el.textContent === 'RELATIVE_FETCH_OK';
    }, { timeout: 10000 });
    await page.click('#next');

    // Step 4: Absolute Path Fetch
    console.log('Step 4: Absolute Path Fetch');
    await page.waitForFunction(() => {
      const el = document.getElementById('fetch-status');
      return el && el.textContent === 'ABSOLUTE_FETCH_OK';
    }, { timeout: 10000 });
    await page.click('#next');

    // Step 5: Set Cookie
    console.log('Step 5: Set Cookie');
    await page.waitForSelector('#cookie-info');
    await page.click('#next');

    // Step 6: Read Cookie
    console.log('Step 6: Read Cookie');
    await page.waitForSelector('#cookie-status');
    const cookieStatus = await page.textContent('#cookie-status');
    if (cookieStatus !== 'COOKIE_OK') {
      throw new Error(`Step 6 failed: cookie status is "${cookieStatus}"`);
    }
    await page.click('#next');

    // Step 7: POST + Redirect (click the submit button)
    console.log('Step 7: POST + Redirect');
    await page.waitForSelector('#next');
    await page.click('#next');

    // Step 8: Final Verification
    console.log('Step 8: Final Verification');
    await page.waitForFunction(() => {
      const el = document.getElementById('result');
      return el && el.textContent !== 'PENDING';
    }, { timeout: 10000 });

    const result = await page.textContent('#result');
    if (result !== 'ALL STEPS PASSED') {
      const cookieSt = await page.textContent('#cookie-status');
      const jsSt = await page.textContent('#js-check');
      throw new Error(`Step 8 failed: result="${result}" cookie-status="${cookieSt}" js-check="${jsSt}"`);
    }

    console.log('ALL STEPS PASSED');
    await context.close();
    if (!wsEndpoint) await browser.close();
    process.exit(0);
  } catch (err) {
    console.error(`FAILED: ${err.message}`);
    try {
      await page.screenshot({ path: 'failure-screenshot.png' });
      console.error('Screenshot saved to cmd/example/failure-screenshot.png');
    } catch (_) {}
    await context.close();
    if (!wsEndpoint) await browser.close();
    process.exit(1);
  }
}

run();
