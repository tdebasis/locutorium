// export.mjs — render .excalidraw scenes to PNG with Excalidraw's own renderer.
// usage: node export.mjs <in.excalidraw> <out.png> [scale]
// Needs playwright-core (any copy: PLAYWRIGHT_CORE=<dir>, or a global install).
import { readFileSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { execSync } from 'node:child_process';
const require = createRequire(import.meta.url);
function findCore() {
  const tries = [process.env.PLAYWRIGHT_CORE, 'playwright-core'];
  try { const g = execSync('npm root -g', { encoding: 'utf8' }).trim();
        tries.push(`${g}/playwright-core`, `${g}/playwright/node_modules/playwright-core`,
                   `${g}/@playwright/mcp/node_modules/playwright-core`); } catch {}
  for (const t of tries.filter(Boolean)) { try { return require(t); } catch {} }
  throw new Error('playwright-core not found; set PLAYWRIGHT_CORE=<path to the package>');
}
const [inFile, outFile, scaleArg] = process.argv.slice(2);
const scale = Number(scaleArg || 2);
const scene = JSON.parse(readFileSync(inFile, 'utf8'));
const { chromium } = findCore();
// A stock Google Chrome (or Chromium) works; no Playwright browser download is needed.
const launch = { headless: true };
if (process.env.BROWSER_PATH) launch.executablePath = process.env.BROWSER_PATH; else launch.channel = process.env.BROWSER_CHANNEL || 'chrome';
const browser = await chromium.launch(launch);
const page = await browser.newPage();
await page.goto('about:blank');
const b64 = await page.evaluate(async ({ scene, scale }) => {
  const mod = await import('https://esm.sh/@excalidraw/excalidraw@0.18.1');
  const blob = await mod.exportToBlob({
    elements: scene.elements,
    appState: { ...scene.appState, exportBackground: true, viewBackgroundColor: '#ffffff', exportWithDarkMode: false },
    files: scene.files || {}, mimeType: 'image/png', exportPadding: 24,
    getDimensions: (w, h) => ({ width: w * scale, height: h * scale, scale }),
  });
  const buf = new Uint8Array(await blob.arrayBuffer());
  let s = ''; for (let i = 0; i < buf.length; i += 0x8000) s += String.fromCharCode.apply(null, buf.subarray(i, i + 0x8000));
  return btoa(s);
}, { scene, scale });
await browser.close();
writeFileSync(outFile, Buffer.from(b64, 'base64'));
console.log(`${outFile}: ${Buffer.from(b64, 'base64').length} bytes`);
