// Alarm sounds made with Web Audio, so no sound files are needed.
// Shared by the room TV page and the staff page. Plain ES5 on purpose:
// Smart TV browsers are often old. Exposes window.KaraokeSound.
(function () {
  "use strict";

  var audio = null;

  function ctx() {
    if (!audio) {
      var AC = window.AudioContext || window.webkitAudioContext;
      if (!AC) return null;
      audio = new AC();
    }
    return audio;
  }

  function tone(freq, startAt, dur, vol) {
    var a = ctx();
    if (!a) return;
    var osc = a.createOscillator(), gain = a.createGain();
    osc.type = "square";
    osc.frequency.value = freq;
    var t0 = a.currentTime + startAt;
    gain.gain.setValueAtTime(0.0001, t0);
    gain.gain.exponentialRampToValueAtTime(vol, t0 + 0.02);
    gain.gain.exponentialRampToValueAtTime(0.0001, t0 + dur);
    osc.connect(gain).connect(a.destination);
    osc.start(t0);
    osc.stop(t0 + dur + 0.05);
  }

  window.KaraokeSound = {
    // Three rising notes, played twice: "5 minutes left".
    warning: function () {
      [0, 1.2].forEach(function (offset) {
        tone(880, offset, 0.25, 0.35);
        tone(1109, offset + 0.3, 0.25, 0.35);
        tone(1319, offset + 0.6, 0.45, 0.35);
      });
    },
    // Two-tone siren, about 1.6 s: "time is up".
    alarm: function () {
      for (var i = 0; i < 4; i++) {
        tone(988, i * 0.4, 0.2, 0.5);
        tone(740, i * 0.4 + 0.2, 0.2, 0.5);
      }
    },
    // Browsers block sound until the user presses a key or clicks once.
    locked: function () {
      var a = ctx();
      return Boolean(a && a.state !== "running");
    },
    // unlock must run inside a click or key handler; done() runs afterwards.
    unlock: function (done) {
      var a = ctx();
      var cb = done || function () {};
      if (a && a.resume) a.resume().then(cb, cb);
      else cb();
    },
  };
})();
