// Screenshots (deviceScaleFactor 2) after deploy/v2/e2e/run_multi.mjs: New Project with two
// repositories picked, Team page with the designer's skills, Deliverables + the viewer showing
// the published HTML, and the thread with one PR per repository.
//   node screenshots_multi.cjs <base-url> <out-dir> <project-id> <thread-no> <deliverable-id>
// Needs the playwright package (NODE_PATH) and a Chromium, e.g. the
// mcr.microsoft.com/playwright image with --network host.
const { chromium } = require("playwright");

const [base, out, pid, no, did] = process.argv.slice(2);

async function newContext(browser, w, h) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: 2 });
  const login = await ctx.request.post(`${base}/auth/login`, { data: { email: "e2e@example.com", password: "e2e-password-123456" } });
  if (!login.ok()) throw new Error(`login ${login.status()}`);
  return ctx;
}

async function open(browser, path, { w = 1440, h = 1000, wait = 2500 } = {}) {
  const ctx = await newContext(browser, w, h);
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(`pageerror: ${e.message}`));
  page.on("console", (m) => m.type() === "error" && errors.push(`console: ${m.text()}`));
  await page.goto(base + path, { waitUntil: "domcontentloaded" });
  await page.waitForTimeout(wait);
  return { ctx, page, errors };
}

async function shot({ ctx, page, errors }, name) {
  await page.screenshot({ path: `${out}/v21-${name}.png` });
  console.log("ok", name, errors.join(" | "));
  await ctx.close();
}

async function pickRepo(page, slug) {
  const input = page.getByPlaceholder("搜索仓库");
  await input.click();
  await input.fill(slug);
  await page.waitForTimeout(800);
  await page.getByRole("option", { name: new RegExp(slug) }).first().click();
  await page.waitForTimeout(500);
}

async function newProject(browser) {
  const s = await open(browser, "/projects/new", { h: 1100 });
  await s.page.getByPlaceholder("例如：agent team").fill("商城");
  await s.page.locator("textarea").first().fill("前端 local/web，后端 local/api。\n仓库 local/api 的基准分支是 develop。");
  await pickRepo(s.page, "local/web");
  await pickRepo(s.page, "local/api");
  await shot(s, "new-project");
}

async function team(browser) {
  const s = await open(browser, "/settings/team", { h: 1300 });
  const card = s.page.getByText("设计师").first();
  await card.scrollIntoViewIfNeeded();
  await s.page.evaluate(() => window.scrollBy(0, -120));
  await s.page.waitForTimeout(500);
  await shot(s, "team-skills");
}

(async () => {
  const browser = await chromium.launch();
  await newProject(browser);
  await team(browser);
  await shot(await open(browser, `/projects/${pid}/deliverables`), "deliverables");
  await shot(await open(browser, `/projects/${pid}/deliverables/${did}`, { wait: 4000 }), "design-viewer");
  await shot(await open(browser, `/projects/${pid}/threads/${no}`, { h: 1100 }), "thread");
  const t = await open(browser, `/projects/${pid}/threads/${no}`, { h: 1100 });
  await t.page.getByText("交付", { exact: true }).first().click();
  await t.page.waitForTimeout(1500);
  await shot(t, "thread-prs");
  await browser.close();
})().catch((e) => {
  console.error(e);
  process.exit(1);
});
