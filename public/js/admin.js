// Staff page: login, daily schedule per room, booking actions and daily report.
// All user data is written with textContent, never innerHTML.

const $ = (sel) => document.querySelector(sel);
const REFRESH_MS = 30_000;

const state = {
  tz: "Asia/Jakarta",
  rooms: [],
  bookings: [],
  date: "",
  tab: "schedule",
  timer: 0,
};

// ---------- API ----------

class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

async function api(method, path, body) {
  const opts = { method, headers: {}, credentials: "same-origin" };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(path, opts);
  } catch {
    throw new ApiError(0, "Tidak bisa terhubung ke server. Cek koneksi internet.");
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    if (res.status === 401 && path !== "/api/login") showLogin();
    throw new ApiError(res.status, data.error || `Error ${res.status}`);
  }
  return data;
}

// ---------- Formatting (always in the business time zone) ----------

const rupiah = new Intl.NumberFormat("id-ID", { style: "currency", currency: "IDR", maximumFractionDigits: 0 });

function parts(date) {
  const p = new Intl.DateTimeFormat("en-GB", {
    timeZone: state.tz, year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23",
  }).formatToParts(date);
  return Object.fromEntries(p.map((x) => [x.type, x.value]));
}
const ymd = (d) => { const p = parts(d); return `${p.year}-${p.month}-${p.day}`; };
const hm = (d) => { const p = parts(d); return `${p.hour}:${p.minute}`; };
const localInput = (d) => `${ymd(d)}T${hm(d)}`;
const fmtDuration = (min) => {
  const h = Math.floor(min / 60), m = min % 60;
  return [h ? `${h} jam` : "", m ? `${m} menit` : ""].filter(Boolean).join(" ") || "0 menit";
};
const longDate = (s) => new Intl.DateTimeFormat("id-ID", { dateStyle: "full", timeZone: "UTC" }).format(new Date(`${s}T00:00:00Z`));

const STATUS_LABEL = { booked: "Booked", checked_in: "Check-in", finished: "Selesai", cancelled: "Batal" };

// ---------- Small DOM helper ----------

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else if (k.startsWith("on")) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v);
  }
  node.append(...children.filter((c) => c != null));
  return node;
}

function flash(text, kind = "ok") {
  const f = $("#flash");
  f.textContent = text;
  f.className = `msg ${kind}`;
  f.hidden = false;
  clearTimeout(flash.t);
  flash.t = setTimeout(() => { f.hidden = true; }, kind === "error" ? 8000 : 4000);
}

// ---------- Views ----------

function showLogin() {
  clearInterval(state.timer);
  $("#app-view").hidden = true;
  $("#login-view").hidden = false;
  $("#pin").focus();
}

async function showApp() {
  $("#login-view").hidden = true;
  $("#app-view").hidden = false;
  if (!state.date) state.date = ymd(new Date());
  $("#date").value = state.date;
  state.rooms = await api("GET", "/api/rooms");
  await refresh();
  clearInterval(state.timer);
  state.timer = setInterval(() => refresh().catch(() => {}), REFRESH_MS);
}

async function refresh() {
  if (state.tab === "schedule") {
    state.bookings = await api("GET", `/api/bookings?date=${state.date}`);
    renderRooms();
  } else {
    renderReport(await api("GET", `/api/report?date=${state.date}`));
  }
}

function renderRooms() {
  const now = Date.now();
  const isToday = state.date === ymd(new Date());
  const cards = state.rooms.filter((r) => r.active).map((room) => {
    const list = state.bookings.filter((b) => b.room_id === room.id);
    const card = el("article", { class: "room" },
      el("header", {},
        el("h2", { text: room.name || room.id }),
        el("span", { class: "rate", text: `${room.id} · ${rupiah.format(room.rate_per_hour)}/jam` })),
    );
    if (isToday) card.append(roomNow(list, now));
    card.append(list.length
      ? el("ol", {}, ...list.map(bookingItem))
      : el("p", { class: "empty", text: "Belum ada booking." }));
    return card;
  });
  $("#rooms").replaceChildren(...cards);
}

function roomNow(list, now) {
  const cur = list.find((b) => b.status === "checked_in");
  if (cur) {
    const diff = Date.parse(cur.end) - now;
    if (diff <= 0) return el("p", { class: "now late", text: `⚠ Waktu habis, lewat ${fmtDuration(Math.floor(-diff / 60000))} — ${cur.customer_name}` });
    const left = Math.ceil(diff / 60000);
    return el("p", { class: `now ${left <= 5 ? "soon" : "busy"}`, text: `● Dipakai — sisa ${fmtDuration(left)} (${cur.customer_name})` });
  }
  return el("p", { class: "now", text: "○ Kosong" });
}

function bookingItem(b) {
  const start = new Date(b.start), end = new Date(b.end);
  const minutes = Math.round((end - start) / 60000);
  const actions = el("div", { class: "actions" });
  const act = (label, fn, cls = "") => el("button", { class: `btn small ${cls}`, type: "button", text: label, onclick: (e) => run(e.currentTarget, fn) });

  if (b.status === "booked") {
    actions.append(
      act("Check-in", () => doAction(b, "checkin", undefined, `Check-in ${b.customer_name}`)),
      act("Batal", () => confirm(`Batalkan booking ${b.customer_name} ${hm(start)}?`) && doAction(b, "cancel", undefined, "Booking dibatalkan"), "danger"),
    );
  } else if (b.status === "checked_in") {
    actions.append(
      act("+30 mnt", () => doAction(b, "extend", { minutes: 30 }, "Diperpanjang 30 menit")),
      act("+1 jam", () => doAction(b, "extend", { minutes: 60 }, "Diperpanjang 1 jam")),
      act("Check-out", () => confirm(`Check-out ${b.customer_name}? Alarm di TV akan berhenti.`) && doAction(b, "checkout", undefined, `Check-out ${b.customer_name}`), "primary"),
    );
  }

  const dayPrefix = ymd(start) !== state.date ? `${start.toLocaleDateString("id-ID", { day: "2-digit", month: "2-digit", timeZone: state.tz })} ` : "";
  return el("li", { class: `bk ${b.status}` },
    el("div", { class: "line1" },
      el("span", { class: "time", text: `${dayPrefix}${hm(start)}–${hm(end)}` }),
      el("span", { class: `badge ${b.status}`, text: STATUS_LABEL[b.status] || b.status })),
    el("div", { class: "who", text: b.customer_name + (b.phone ? ` · ${b.phone}` : "") }),
    el("div", { class: "meta", text: `${fmtDuration(minutes)} · ${rupiah.format(b.total_price)}${b.notes ? ` · ${b.notes}` : ""}` }),
    actions.childElementCount ? actions : null,
  );
}

async function doAction(b, action, body, okText) {
  await api("POST", `/api/bookings/${encodeURIComponent(b.id)}/${action}`, body ?? {});
  flash(okText);
  await refresh();
}

// run disables the button while fn runs and shows errors.
async function run(button, fn) {
  button.disabled = true;
  try {
    await fn();
  } catch (err) {
    flash(err.message, "error");
  } finally {
    button.disabled = false;
  }
}

function renderReport(rep) {
  $("#report-title").textContent = `Laporan harian — ${longDate(rep.date)}`;
  const tile = (label, value) => el("div", { class: "tile" }, el("div", { class: "label", text: label }), el("div", { class: "value", text: value }));
  $("#report-tiles").replaceChildren(
    tile("Pendapatan", rupiah.format(rep.revenue)),
    tile("Jam terpakai", fmtDuration(rep.minutes)),
    tile("Selesai", String(rep.finished)),
    tile("Sedang dipakai", String(rep.checked_in)),
    tile("Belum check-in", String(rep.booked)),
    tile("Batal", String(rep.cancelled)),
  );
  const td = (text, cls = "") => el("td", { class: cls, text });
  $("#report-rows").replaceChildren(...rep.rooms.map((r) => el("tr", {},
    td(r.room_name || r.room_id), td(String(r.bookings), "num"), td(fmtDuration(r.minutes), "num"), td(rupiah.format(r.revenue), "num"))));
  $("#report-total").replaceChildren(
    td("Total"), td(String(rep.finished + rep.checked_in), "num"), td(fmtDuration(rep.minutes), "num"), td(rupiah.format(rep.revenue), "num"));
}

// ---------- Booking dialog ----------

function openBookingDialog() {
  const form = $("#booking-form");
  form.reset();
  $("#booking-error").hidden = true;
  $("#f-room").replaceChildren(...state.rooms.filter((r) => r.active).map((r) =>
    el("option", { value: r.id, text: `${r.name || r.id} — ${rupiah.format(r.rate_per_hour)}/jam` })));
  const durations = [];
  for (let m = 30; m <= 720; m += 30) durations.push(el("option", { value: String(m), text: fmtDuration(m) }));
  $("#f-duration").replaceChildren(...durations);
  $("#f-duration").value = "60";

  // Default start: now on today, or 19:00 on another day.
  const now = new Date();
  $("#f-start").value = state.date === ymd(now) ? localInput(now) : `${state.date}T19:00`;
  updateTotal();
  $("#booking-dialog").showModal();
  $("#f-name").focus();
}

function updateTotal() {
  const room = state.rooms.find((r) => r.id === $("#f-room").value);
  const minutes = Number($("#f-duration").value);
  if (!room || !minutes) return;
  const total = Math.round(room.rate_per_hour * minutes / 60);
  $("#f-total").textContent = `Total: ${rupiah.format(total)}`;
}

async function submitBooking(e) {
  e.preventDefault();
  const form = e.currentTarget;
  if (!form.reportValidity()) return;
  const data = Object.fromEntries(new FormData(form));
  data.duration_minutes = Number(data.duration_minutes);
  const btn = $("#booking-submit");
  btn.disabled = true;
  try {
    const b = await api("POST", "/api/bookings", data);
    $("#booking-dialog").close();
    flash(`Booking ${b.customer_name} ${hm(new Date(b.start))}–${hm(new Date(b.end))} tersimpan (${rupiah.format(b.total_price)})`);
    const day = ymd(new Date(b.start));
    if (day !== state.date) { state.date = day; $("#date").value = day; }
    await refresh();
  } catch (err) {
    const box = $("#booking-error");
    box.textContent = err.message;
    box.hidden = false;
  } finally {
    btn.disabled = false;
  }
}

// ---------- Wiring ----------

function selectTab(tab) {
  state.tab = tab;
  for (const name of ["schedule", "report"]) {
    $(`#tab-${name}`).setAttribute("aria-selected", String(name === tab));
    $(`#${name}-view`).hidden = name !== tab;
  }
  refresh().catch((err) => flash(err.message, "error"));
}

function tickClock() {
  const p = parts(new Date());
  $("#clock").textContent = `${p.hour}:${p.minute}:${p.second} WIB`;
}

async function init() {
  $("#login-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const err = $("#login-error");
    err.hidden = true;
    try {
      await api("POST", "/api/login", { pin: $("#pin").value });
      $("#pin").value = "";
      await showApp();
    } catch (ex) {
      err.textContent = ex.message;
      err.hidden = false;
    }
  });
  $("#logout").addEventListener("click", async () => {
    await api("POST", "/api/logout", {}).catch(() => {});
    showLogin();
  });
  $("#date").addEventListener("change", (e) => {
    state.date = e.target.value || ymd(new Date());
    refresh().catch((err) => flash(err.message, "error"));
  });
  $("#tab-schedule").addEventListener("click", () => selectTab("schedule"));
  $("#tab-report").addEventListener("click", () => selectTab("report"));
  $("#print-report").addEventListener("click", () => window.print());
  $("#new-booking").addEventListener("click", openBookingDialog);
  $("#booking-cancel").addEventListener("click", () => $("#booking-dialog").close());
  $("#booking-form").addEventListener("submit", submitBooking);
  $("#f-room").addEventListener("change", updateTotal);
  $("#f-duration").addEventListener("change", updateTotal);

  try {
    const me = await api("GET", "/api/me");
    state.tz = me.timezone || state.tz;
    tickClock();
    setInterval(tickClock, 1000);
    if (me.logged_in) await showApp();
    else showLogin();
  } catch (err) {
    showLogin();
    const box = $("#login-error");
    box.textContent = err.message;
    box.hidden = false;
  }
}

init();
