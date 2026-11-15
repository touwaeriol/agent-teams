// UserPromptSubmit hook: leaves a marker next to the plugin so the smoke test can see hooks fire.
import { writeFileSync } from "node:fs";
writeFileSync(new URL("../hook-ran", import.meta.url), "yes");
