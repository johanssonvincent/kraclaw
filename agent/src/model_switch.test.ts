import test from "node:test";
import assert from "node:assert/strict";

import { applySetModel } from "./model_switch.js";

test("applySetModel updates model and clears session", () => {
  const next = applySetModel(
    {
      currentModel: "claude-3-5-sonnet-20241022",
      sessionId: "sess-old",
    },
    "claude-opus-4-1"
  );

  assert.equal(next.currentModel, "claude-opus-4-1");
  assert.equal(next.sessionId, undefined);
});

test("applySetModel keeps session cleared when already empty", () => {
  const next = applySetModel(
    {
      currentModel: "claude-3-5-sonnet-20241022",
      sessionId: undefined,
    },
    "claude-opus-4-1"
  );

  assert.equal(next.currentModel, "claude-opus-4-1");
  assert.equal(next.sessionId, undefined);
});
