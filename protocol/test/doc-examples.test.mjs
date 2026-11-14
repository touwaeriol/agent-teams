import { test } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { ROOT } from "../scripts/lib.mjs";

const SCRIPT = join(ROOT, "scripts/check-doc-examples.mjs");
const DOC = join(ROOT, "../docs/v2/protocol.md");

function check(docPath) {
  return spawnSync(process.execPath, [SCRIPT, docPath], { encoding: "utf8" });
}

function checkMutated(from, to) {
  const original = readFileSync(DOC, "utf8");
  assert.ok(original.includes(from), `fixture text not found: ${from}`);
  const dir = mkdtempSync(join(tmpdir(), "proto-doc-"));
  try {
    const path = join(dir, "protocol.md");
    writeFileSync(path, original.replace(from, to));
    return check(path);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

test("every example in protocol.md validates", () => {
  const res = check(DOC);
  assert.equal(res.status, 0, res.stderr + res.stdout);
});

const MUTATIONS = [
  ["json block field type", `"last_seq": 1841`, `"last_seq": "1841"`],
  ["json block enum alternative", `"priority": "now|next|later"`, `"priority": "now|soon|later"`],
  ["inline shorthand field", "session.delivered {session_id, msg_id, turn_id?}", "session.delivered {session_id, msg_id, turn?}"],
  ["inline literal value", `session.state {session_id, state:"parked"}`, `session.state {session_id, state:"napping"}`],
  ["event table field", "| `compacting` | `on: bool` |", "| `compacting` | `active: bool` |"],
  ["node table field", "`deadline_s`", "`deadline`"],
];

for (const [name, from, to] of MUTATIONS) {
  test(`checker catches a broken example: ${name}`, () => {
    assert.notEqual(checkMutated(from, to).status, 0);
  });
}
