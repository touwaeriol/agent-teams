// Renders web/public/favicon.svg into desktop/build/icon.png (1024×1024, ~10% margin as
// macOS icons have). electron-builder turns it into icon.icns (Mac) and icon.ico (Windows).
// Run with `npm run icon` after the favicon changes; the PNG is committed.
import { Resvg } from "@resvg/resvg-js";
import { readFileSync, writeFileSync } from "node:fs";

const svg = readFileSync(new URL("../../web/public/favicon.svg", import.meta.url), "utf8")
  // The favicon is 26×26 with its squares at 1..25; widen the view box for the margin.
  .replace(/viewBox="[^"]*"/, 'viewBox="-2 -2 30 30"');
const png = new Resvg(svg, { fitTo: { mode: "width", value: 1024 }, background: "rgba(0,0,0,0)" }).render().asPng();
writeFileSync(new URL("../build/icon.png", import.meta.url), png);
console.log(`build/icon.png: ${png.length} bytes`);
