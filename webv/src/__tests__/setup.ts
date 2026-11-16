// Test setup. Under jsdom the app's same-origin paths ("/api/…") must resolve like in a
// browser, but Request is Node's, which rejects relative URLs: resolve them against the page.
// Runs before the test files import the API client (which captures Request).
if (typeof window !== "undefined" && typeof globalThis.Request === "function") {
  const NodeRequest = globalThis.Request;
  class PageRequest extends NodeRequest {
    constructor(input: RequestInfo | URL, init?: RequestInit) {
      super(typeof input === "string" ? new URL(input, window.location.href) : input, init);
    }
  }
  globalThis.Request = PageRequest as typeof Request;
}
