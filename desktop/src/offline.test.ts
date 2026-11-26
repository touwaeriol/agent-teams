import { expect, it } from "vitest";
import { showsOfflinePage } from "./offline";

it("shows the offline page only for real main-frame failures", () => {
  expect(showsOfflinePage(-106, true)).toBe(true); // ERR_INTERNET_DISCONNECTED
  expect(showsOfflinePage(-105, true)).toBe(true); // ERR_NAME_NOT_RESOLVED
  expect(showsOfflinePage(-3, true)).toBe(false); // ERR_ABORTED
  expect(showsOfflinePage(-106, false)).toBe(false); // an iframe
});
