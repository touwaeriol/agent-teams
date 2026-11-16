// Screenshots (deviceScaleFactor 2) of the running v2 app after deploy/v2/e2e/run.mjs.
//   node screenshots.cjs <base-url> <out-dir> <project-id> <thread-no>
// Needs the playwright package (NODE_PATH) and a Chromium, e.g. the
// mcr.microsoft.com/playwright image with --network host.
const { chromium } = require("playwright");

const [base, out, pid, no] = process.argv.slice(2);
const SHOTS = [
  ["main", `/projects/${pid}`, { h: 900 }],
  ["thread", `/projects/${pid}/threads/${no}`, { h: 1000 }],
  ["thread-full", `/projects/${pid}/threads/${no}/full`, { h: 1000 }],
  ["office", `/projects/${pid}/office`, { w: 1600, h: 1000, wait: 5000 }],
  ["team", "/settings/team", { h: 1180 }],
  ["nodes", "/settings/nodes", { h: 900 }],
];

// One sign-in shared by every shot: /auth/login is rate limited per account.
let storage;
async function signIn(browser) {
  const ctx = await browser.newContext();
  const login = await ctx.request.post(`${base}/auth/login`, { data: { email: "e2e@example.com", password: "e2e-password-123456" } });
  if (!login.ok()) throw new Error(`login ${login.status()}`);
  storage = await ctx.storageState();
  await ctx.close();
}

async function shoot(browser, [name, path, o]) {
  const ctx = await browser.newContext({ viewport: { width: o.w ?? 1440, height: o.h }, deviceScaleFactor: 2, storageState: storage });
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(`pageerror: ${e.message}`));
  page.on("console", (m) => m.type() === "error" && errors.push(`console: ${m.text()}`));
  await page.goto(base + path, { waitUntil: "domcontentloaded" });
  await page.waitForTimeout(o.wait ?? 2000);
  await page.screenshot({ path: `${out}/e2e-${name}.png` });
  console.log("ok", name, errors.join(" | "));
  await ctx.close();
}

(async () => {
  const browser = await chromium.launch();
  await signIn(browser);
  for (const shot of SHOTS) await shoot(browser, shot);
  await browser.close();
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
