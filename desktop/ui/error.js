// Fills the error page from the address the shell opened it with. Everything is set as text, never as markup,
// and the page works without this file: the heading, the explanation and both buttons are in the HTML.
(function () {
  "use strict";
  var params = new URLSearchParams(location.hash.slice(1));
  var kinds = {
    start: ["Scríobh could not start", "The part of Scríobh that keeps your writing did not start. Nothing you saved is lost: your writing is in the data folder on this computer."],
    timeout: ["Scríobh is taking too long to start", "The part of Scríobh that keeps your writing did not become ready in 30 seconds. Nothing you saved is lost: your writing is in the data folder on this computer."],
    notserver: ["Scríobh could not tell its own server apart", "Something answered at the address the server was started on, but it could not show that it is the part of Scríobh that was just started, so nothing was sent to it and the app did not open. Choose Restart. If this keeps happening, quit other programs that may be using local network ports, or restart the computer. Your writing is not affected."],
    missing: ["Scríobh is incomplete", "The server program that belongs inside this app could not be started. Installing Scríobh again should put it back. Your writing is not affected."],
    keychain: ["Scríobh could not read its key", "The data folder holds your writing, and the key that protects saved AI and Git credentials is kept in the system keychain, which could not be read just now (it may be locked, or access may have been refused). Nothing was started and nothing was changed. Unlock the keychain or allow access, then choose Restart. If macOS asked for your login password just now and you chose Deny, choose Restart and then Always Allow. Your writing is not affected."],
    datafolder: ["Scríobh cannot look in its data folder", "The data folder could not be read, so Scríobh did not start: it cannot tell whether your writing is there. Nothing was started and nothing was changed. Check that the folder (named below) is there and that you may open it, then choose Restart.", "The folder"],
    stopped: ["Scríobh has stopped", "The part of Scríobh that keeps your writing stopped while the app was open. What you typed in the last moments may not have been saved; everything saved before that is in the data folder on this computer."]
  };
  var kind = kinds[params.get("kind")] || kinds.start;
  document.title = kind[0];
  document.getElementById("title").textContent = kind[0];
  document.getElementById("lead").textContent = kind[1];
  var tail = params.get("tail");
  if (kind[2]) document.getElementById("details-title").textContent = kind[2];
  if (tail) {
    document.getElementById("tail").textContent = tail;
    document.getElementById("details").hidden = false;
  }
  document.getElementById("restart").focus();
})();
