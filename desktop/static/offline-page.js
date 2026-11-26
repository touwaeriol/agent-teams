// Goes back to the page that failed (?url=, http(s) only) on 重试 or when the network comes back.
(function () {
  var params = new URLSearchParams(location.search);
  var target = params.get("url") || "";
  var ok = /^https?:\/\//i.test(target);
  document.getElementById("detail").textContent = [params.get("error"), ok ? target : ""].filter(Boolean).join(" · ");
  function retry() {
    if (ok) location.replace(target);
  }
  document.getElementById("retry").addEventListener("click", retry);
  window.addEventListener("online", retry);
})();
