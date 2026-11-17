/* Scríobh: the conflict comparison. Fetched by doc.js the first time Compare is pressed.
   A read-only, word-level diff of the saved text against yours: no library, no markup built
   from text (everything goes in as text nodes). */
(function () {
  "use strict";
  var doc = document;
  var S = (window.scriobh = window.scriobh || {});
  function el(tag, cls, text) { var e = doc.createElement(tag); if (cls) e.className = cls; if (text != null) e.textContent = text; return e; }
  function diff(a, b, coarse) {
    var p = 0, n = a.length, m = b.length;
    while (p < n && p < m && a[p] === b[p]) p++;
    while (n > p && m > p && a[n - 1] === b[m - 1]) { n--; m--; }
    var A = a.slice(p, n), B = b.slice(p, m), out = [];
    var head = a.slice(0, p).join(""), tail = a.slice(n).join("");
    if (head) out.push([0, head]);
    if (A.length * B.length > 4e6) {
      if (!coarse) {
        var para = function (t) { return t.join("").split(/(\n\s*\n)/); };
        return head ? [[0, head]].concat(diff(para(A), para(B), true), tail ? [[0, tail]] : []) : diff(para(A), para(B), true).concat(tail ? [[0, tail]] : []);
      }
      if (A.length) out.push([-1, A.join("")]);
      if (B.length) out.push([1, B.join("")]);
    } else {
      var w = B.length + 1, t = new Uint16Array((A.length + 1) * w), i, j;
      for (i = A.length - 1; i >= 0; i--) for (j = B.length - 1; j >= 0; j--) t[i * w + j] = A[i] === B[j] ? t[(i + 1) * w + j + 1] + 1 : Math.max(t[(i + 1) * w + j], t[i * w + j + 1]);
      i = 0; j = 0;
      while (i < A.length || j < B.length) {
        if (i < A.length && j < B.length && A[i] === B[j]) { push(out, 0, A[i]); i++; j++; }
        else if (j < B.length && (i === A.length || t[i * w + j + 1] >= t[(i + 1) * w + j])) { push(out, 1, B[j]); j++; }
        else { push(out, -1, A[i]); i++; }
      }
    }
    if (tail) out.push([0, tail]);
    return out;
  }
  function push(out, op, text) {
    var l = out[out.length - 1];
    if (l && l[0] === op) l[1] += text; else out.push([op, text]);
  }
  // theirs: the saved text; mine: the text in the editor; host: the notices container.
  S.compare = function (theirs, mine, host) {
    var tok = function (s) { return s.split(/(\s+)/).filter(Boolean); };
    var ops = diff(tok(theirs), tok(mine));
    var box = el("div", "compare");
    box.id = "nb-compare";
    box.setAttribute("role", "region");
    box.setAttribute("aria-label", "Comparison of your text with the saved text");
    box.tabIndex = 0;
    box.appendChild(el("p", "compare-key", "Green is only in your text. Struck through is only in the saved text."));
    ops.forEach(function (o) {
      if (o[0] === 0) box.appendChild(doc.createTextNode(o[1]));
      else box.appendChild(el(o[0] > 0 ? "ins" : "del", "", o[1]));
    });
    host.insertBefore(box, host.firstChild);
    if (!ops.some(function (o) { return o[0] !== 0; })) box.appendChild(el("p", "compare-key", "The two texts are the same."));
  };
})();
