// Room TV: shows the remaining time of the checked-in guest and plays alarms.
//   - 5 minutes before the end: orange banner + chime (once per end time).
//   - At the end: red flashing screen + repeating alarm until staff checks
//     out or extends in the admin page. "Snooze" mutes it for 2 minutes.
// Written in plain ES2017 (no ?. or ??) because Smart TV browsers are often old.
(function () {
  "use strict";

  var WARN_MS = 5 * 60 * 1000;
  var SNOOZE_MS = 2 * 60 * 1000;
  var POLL_MS = 20 * 1000; // normal polling
  var POLL_FAST_MS = 10 * 1000; // during warning / overtime, so check-out stops the alarm quickly
  var TZ = "Asia/Jakarta";

  var cfg = loadConfig();
  var clockOffset = 0; // server time minus device time, in ms
  var status = null; // last /api/tv response
  var mode = ""; // idle | running | warning | overtime
  var warnedFor = ""; // booking id + end time already warned
  var snoozeUntil = 0;
  var alarmTimer = 0;
  var pollTimer = 0;

  function $(id) { return document.getElementById(id); }
  function now() { return Date.now() + clockOffset; }

  // ---------- Config (URL first, then saved on this TV) ----------

  // A paired TV has its own token. The older way is a room code plus the
  // shared TV key, from the URL (?room=&key=) or saved on this TV.
  function loadConfig() {
    var q = new URLSearchParams(location.search);
    var saved = {};
    try { saved = JSON.parse(localStorage.getItem("karaoke-tv") || "{}"); } catch (e) { /* storage blocked */ }
    var c = { token: saved.token || "", room: q.get("room") || saved.room || "", key: q.get("key") || saved.key || "" };
    if (q.get("setup") === "1") { c.token = ""; c.room = ""; }
    return c;
  }

  function configured() {
    return Boolean(cfg.token || (cfg.room && cfg.key));
  }

  function saveConfig(c) {
    try { localStorage.setItem("karaoke-tv", JSON.stringify(c)); } catch (e) { /* ignore */ }
  }

  function showSetup(message) {
    clearTimeout(pollTimer);
    stopAlarm();
    $("screen").hidden = true;
    $("setup").hidden = false;
    $("s-room").value = cfg.room;
    $("s-key").value = cfg.key;
    $("s-code").value = "";
    $("setup-error").textContent = message || "";
    $("s-code").focus();
  }

  function pair(code) {
    $("setup-error").textContent = "Memasangkan…";
    fetch("/api/tv/pair", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code: code }),
      cache: "no-store",
    })
      .then(function (res) {
        return res.json().catch(function () { return {}; }).then(function (data) {
          if (!res.ok) throw new Error(data.error || "HTTP " + res.status);
          return data;
        });
      })
      .then(function (data) {
        cfg = { token: data.token, room: "", key: "" };
        saveConfig(cfg);
        unlockAudio();
        start();
      })
      .catch(function (err) {
        $("setup-error").textContent = err.message || "Gagal terhubung ke server";
        $("s-code").value = "";
        $("s-code").focus();
      });
  }

  // ---------- Server ----------

  function poll() {
    clearTimeout(pollTimer);
    var sent = Date.now();
    var url = "/api/tv", headers = {};
    if (cfg.token) {
      headers["X-TV-Token"] = cfg.token;
    } else {
      url += "?room=" + encodeURIComponent(cfg.room);
      headers["X-TV-Key"] = cfg.key;
    }
    fetch(url, { headers: headers, cache: "no-store" })
      .then(function (res) {
        return res.json().catch(function () { return {}; }).then(function (data) {
          if (res.status === 401 || res.status === 404) {
            if (cfg.token) { cfg.token = ""; saveConfig(cfg); } // revoked: pair again
            showSetup(data.error || "Room atau TV key salah");
            throw null; // stop polling until setup is saved
          }
          if (!res.ok) throw new Error(data.error || "HTTP " + res.status);
          return data;
        });
      })
      .then(function (data) {
        var rtt = Date.now() - sent;
        clockOffset = Date.parse(data.server_time) + rtt / 2 - Date.now();
        status = data;
        setNet(true);
        render();
        schedulePoll();
      })
      .catch(function (err) {
        if (err === null) return;
        setNet(false); // keep counting down with the last data
        schedulePoll();
      });
  }

  function schedulePoll() {
    pollTimer = setTimeout(poll, mode === "warning" || mode === "overtime" ? POLL_FAST_MS : POLL_MS);
  }

  function setNet(online) {
    var n = $("net");
    n.textContent = online ? "" : "⚠ Offline — timer tetap berjalan, mencoba menyambung…";
    n.className = online ? "net" : "net offline";
  }

  // ---------- Rendering (every second) ----------

  function fmtClock(ms) {
    return new Intl.DateTimeFormat("id-ID", { timeZone: TZ, hour: "2-digit", minute: "2-digit", hourCycle: "h23" }).format(new Date(ms));
  }

  function fmtLeft(ms) {
    var s = Math.max(0, Math.ceil(ms / 1000));
    var h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
    var pad = function (n) { return (n < 10 ? "0" : "") + n; };
    return (h > 0 ? h + ":" + pad(m) : pad(m)) + ":" + pad(sec);
  }

  function render() {
    var t = now();
    $("clock").textContent = fmtClock(t);
    if (!status) return;
    $("room-name").textContent = status.room.name || status.room.id;

    var cur = status.current;
    if (!cur) {
      setMode("idle");
      var next = status.next;
      $("idle-next").textContent = next
        ? "Booking berikutnya: " + fmtClock(Date.parse(next.start)) + " – " + fmtClock(Date.parse(next.end))
        : "";
      return;
    }

    var start = Date.parse(cur.start), end = Date.parse(cur.end);
    var left = end - t;
    $("guest").textContent = "Selamat bernyanyi, " + cur.customer_name;
    $("countdown").textContent = fmtLeft(left);
    $("end-at").textContent = "Selesai pukul " + fmtClock(end);
    var pct = Math.max(0, Math.min(100, (left / (end - start)) * 100));
    $("progress-bar").style.width = pct + "%";

    var key = cur.id + "|" + cur.end;
    if (left <= 0) {
      setMode("overtime");
      $("banner-title").textContent = "WAKTU HABIS";
      $("banner-text").textContent = "Lewat " + fmtLeft(-left) + ". Silakan hubungi staf untuk perpanjang atau check-out.";
      var snoozed = t < snoozeUntil;
      document.body.classList.toggle("snoozed", snoozed);
      $("snooze").hidden = snoozed;
      if (!snoozed) startAlarm();
      else stopAlarm();
    } else if (left <= WARN_MS) {
      setMode("warning");
      $("banner-title").textContent = "Waktu tinggal " + Math.ceil(left / 60000) + " menit";
      $("banner-text").textContent = "Hubungi staf jika ingin menambah waktu.";
      $("snooze").hidden = true;
      if (warnedFor !== key) {
        warnedFor = key;
        playWarning();
      }
    } else {
      setMode("running");
    }
  }

  function setMode(m) {
    if (m === mode) return;
    var wasAlarm = mode === "warning" || mode === "overtime";
    mode = m;
    document.body.className = m;
    $("idle-view").hidden = m !== "idle";
    $("run-view").hidden = m === "idle";
    $("banner").hidden = !(m === "warning" || m === "overtime");
    if (m !== "overtime") {
      stopAlarm();
      snoozeUntil = 0;
    }
    if (m === "overtime") $("snooze").focus();
    // Poll faster as soon as we enter an alarm state.
    if (!wasAlarm && (m === "warning" || m === "overtime")) {
      clearTimeout(pollTimer);
      schedulePoll();
    }
  }

  // ---------- Sound (see sound.js) ----------

  var playWarning = window.KaraokeSound.warning;
  var playAlarm = window.KaraokeSound.alarm;

  function startAlarm() {
    if (alarmTimer) return;
    playAlarm();
    alarmTimer = setInterval(playAlarm, 2500);
  }

  function stopAlarm() {
    clearInterval(alarmTimer);
    alarmTimer = 0;
  }

  // Autoplay rules: show a button if the audio context is not running yet.
  function checkAudioUnlocked() {
    var locked = window.KaraokeSound.locked();
    $("unlock").hidden = !locked;
    if (locked) $("unlock-btn").focus();
  }

  function unlockAudio() {
    window.KaraokeSound.unlock(checkAudioUnlocked);
  }

  // ---------- Keep the screen on ----------

  var wakeLock = null;
  function keepAwake() {
    if (!("wakeLock" in navigator) || document.visibilityState !== "visible") return;
    navigator.wakeLock.request("screen").then(function (l) { wakeLock = l; }, function () { /* not allowed */ });
  }

  // ---------- Start ----------

  function start() {
    $("setup").hidden = true;
    $("screen").hidden = false;
    setMode("idle");
    poll();
    checkAudioUnlocked();
    keepAwake();
  }

  document.addEventListener("DOMContentLoaded", function () {
    $("setup-form").addEventListener("submit", function (e) {
      e.preventDefault();
      cfg = { token: "", room: $("s-room").value.trim().toUpperCase(), key: $("s-key").value.trim() };
      saveConfig(cfg);
      unlockAudio();
      start();
    });
    $("pair-form").addEventListener("submit", function (e) {
      e.preventDefault();
      pair($("s-code").value.trim());
    });
    $("show-legacy").addEventListener("click", function () {
      $("setup-form").hidden = false;
      $("s-room").focus();
    });
    $("unlock-btn").addEventListener("click", unlockAudio);
    $("test-sound").addEventListener("click", function () { unlockAudio(); playWarning(); });
    $("snooze").addEventListener("click", function () {
      snoozeUntil = now() + SNOOZE_MS;
      stopAlarm();
      render();
    });
    // Any remote key press counts as a user gesture for audio.
    document.addEventListener("keydown", function () { if (window.KaraokeSound.locked()) unlockAudio(); });
    document.addEventListener("visibilitychange", function () {
      if (document.visibilityState === "visible") { keepAwake(); poll(); }
    });

    setInterval(render, 1000);
    if (!configured()) showSetup();
    else { saveConfig(cfg); start(); }
  });
})();
