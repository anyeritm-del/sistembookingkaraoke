// Staff page: login, daily schedule per room, booking actions, daily report,
// audit log, room and user management. What a user can see depends on the
// permissions the server sends; the server checks them again on every call.
// All user data is written with textContent, never innerHTML.

const $ = (sel) => document.querySelector(sel);
const REFRESH_MS = 30_000;

const state = {
  tz: "Asia/Jakarta",
  user: null,
  perms: new Set(),
  rooms: [],
  bookings: [],
  date: "",
  tab: "schedule",
  timer: 0,
};
const can = (perm) => state.perms.has(perm);

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
const hms = (d) => { const p = parts(d); return `${p.hour}:${p.minute}:${p.second}`; };
const localInput = (d) => `${ymd(d)}T${hm(d)}`;
const fmtDuration = (min) => {
  const h = Math.floor(min / 60), m = min % 60;
  return [h ? `${h} jam` : "", m ? `${m} menit` : ""].filter(Boolean).join(" ") || "0 menit";
};
const longDate = (s) => new Intl.DateTimeFormat("id-ID", { dateStyle: "full", timeZone: "UTC" }).format(new Date(`${s}T00:00:00Z`));

const STATUS_LABEL = { booked: "Booked", checked_in: "Check-in", finished: "Selesai", cancelled: "Batal" };
const ROLE_LABEL = { staff: "Staff", supervisor: "Supervisor", admin: "Admin" };
const ACTION_LABEL = {
  "login": "Login",
  "pin.change": "Ganti PIN sendiri",
  "booking.create": "Booking baru",
  "booking.checkin": "Check-in",
  "booking.extend": "Perpanjang",
  "booking.checkout": "Check-out",
  "booking.cancel": "Batal booking",
  "room.create": "Room baru",
  "room.update": "Ubah room",
  "user.create": "User baru",
  "user.update": "Ubah user",
  "user.pin_reset": "Reset PIN user",
};

// ---------- Small DOM helpers ----------

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

const td = (text, cls = "") => el("td", { class: cls, text });

function flash(text, kind = "ok") {
  const f = $("#flash");
  f.textContent = text;
  f.className = `msg ${kind}`;
  f.hidden = false;
  clearTimeout(flash.t);
  flash.t = setTimeout(() => { f.hidden = true; }, kind === "error" ? 8000 : 4000);
}

function showError(box, err) {
  box.textContent = err.message;
  box.hidden = false;
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

// submitDialog wires a dialog form: validate, call fn, close, or show the error.
function submitDialog(dialog, errorBox, fn) {
  const form = dialog.querySelector("form");
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (!form.reportValidity()) return;
    const btn = form.querySelector("button[type=submit]");
    btn.disabled = true;
    errorBox.hidden = true;
    try {
      await fn(form);
      dialog.close();
    } catch (err) {
      showError(errorBox, err);
    } finally {
      btn.disabled = false;
    }
  });
  dialog.querySelectorAll("[data-close]").forEach((b) => b.addEventListener("click", () => dialog.close()));
}

// ---------- Session ----------

function showLogin() {
  clearInterval(state.timer);
  state.user = null;
  state.perms = new Set();
  $("#app-view").hidden = true;
  $("#login-view").hidden = false;
  $("#username").focus();
}

function applySession(me) {
  state.user = me.user;
  state.perms = new Set(me.permissions || []);
  state.tz = me.timezone || state.tz;
  $("#who-am-i").textContent = `${me.user.name} · ${ROLE_LABEL[me.user.role] || me.user.role}`;
  document.querySelectorAll("[data-perm]").forEach((node) => { node.hidden = !can(node.dataset.perm); });
}

async function showApp(me) {
  applySession(me);
  $("#login-view").hidden = true;
  $("#app-view").hidden = false;
  if (!state.date) state.date = ymd(new Date());
  $("#date").value = state.date;
  state.rooms = await api("GET", "/api/rooms");
  selectTab("schedule");
  clearInterval(state.timer);
  state.timer = setInterval(() => { if (state.tab === "schedule") refresh().catch(() => {}); }, REFRESH_MS);
}

// ---------- Tabs ----------

const TABS = ["schedule", "report", "activity", "rooms", "users"];

function selectTab(tab) {
  state.tab = tab;
  for (const name of TABS) {
    $(`#tab-${name}`).setAttribute("aria-selected", String(name === tab));
    $(`#${name}-view`).hidden = name !== tab;
  }
  // The date only matters for these tabs.
  $("#date").hidden = !["schedule", "report", "activity"].includes(tab);
  refresh().catch((err) => flash(err.message, "error"));
}

async function refresh() {
  switch (state.tab) {
    case "schedule":
      state.bookings = await api("GET", `/api/bookings?date=${state.date}`);
      renderRooms();
      break;
    case "report":
      renderReport(await api("GET", `/api/report?date=${state.date}`));
      break;
    case "activity":
      renderActivity(await api("GET", `/api/activity?date=${state.date}`));
      break;
    case "rooms":
      state.rooms = await api("GET", "/api/rooms");
      renderRoomAdmin();
      break;
    case "users":
      renderUsers(await api("GET", "/api/users"));
      break;
  }
}

// ---------- Schedule ----------

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
    if (can("booking.checkin")) actions.append(act("Check-in", () => doAction(b, "checkin", undefined, `Check-in ${b.customer_name}`)));
    if (can("booking.cancel")) {
      actions.append(act("Batal", () => confirm(`Batalkan booking ${b.customer_name} ${hm(start)}?`) && doAction(b, "cancel", undefined, "Booking dibatalkan"), "danger"));
    }
  } else if (b.status === "checked_in") {
    if (can("booking.extend")) {
      actions.append(
        act("+30 mnt", () => doAction(b, "extend", { minutes: 30 }, "Diperpanjang 30 menit")),
        act("+1 jam", () => doAction(b, "extend", { minutes: 60 }, "Diperpanjang 1 jam")),
      );
    }
    if (can("booking.checkout")) {
      actions.append(act("Check-out", () => confirm(`Check-out ${b.customer_name}? Alarm di TV akan berhenti.`) && doAction(b, "checkout", undefined, `Check-out ${b.customer_name}`), "primary"));
    }
  }

  const by = [
    b.created_by && `dibuat ${b.created_by}`,
    b.checked_in_by && `in ${b.checked_in_by}`,
    b.checked_out_by && `out ${b.checked_out_by}`,
    b.cancelled_by && `batal ${b.cancelled_by}`,
  ].filter(Boolean).join(", ");

  const dayPrefix = ymd(start) !== state.date ? `${start.toLocaleDateString("id-ID", { day: "2-digit", month: "2-digit", timeZone: state.tz })} ` : "";
  return el("li", { class: `bk ${b.status}` },
    el("div", { class: "line1" },
      el("span", { class: "time", text: `${dayPrefix}${hm(start)}–${hm(end)}` }),
      el("span", { class: `badge ${b.status}`, text: STATUS_LABEL[b.status] || b.status })),
    el("div", { class: "who", text: b.customer_name + (b.phone ? ` · ${b.phone}` : "") }),
    el("div", { class: "meta", text: `${fmtDuration(minutes)} · ${rupiah.format(b.total_price)}${b.notes ? ` · ${b.notes}` : ""}` }),
    by ? el("div", { class: "meta", text: by }) : null,
    actions.childElementCount ? actions : null,
  );
}

async function doAction(b, action, body, okText) {
  await api("POST", `/api/bookings/${encodeURIComponent(b.id)}/${action}`, body ?? {});
  flash(okText);
  await refresh();
}

// ---------- Report ----------

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
  $("#report-rows").replaceChildren(...rep.rooms.map((r) => el("tr", {},
    td(r.room_name || r.room_id), td(String(r.bookings), "num"), td(fmtDuration(r.minutes), "num"), td(rupiah.format(r.revenue), "num"))));
  $("#report-total").replaceChildren(
    td("Total"), td(String(rep.finished + rep.checked_in), "num"), td(fmtDuration(rep.minutes), "num"), td(rupiah.format(rep.revenue), "num"));
}

// ---------- Activity ----------

function renderActivity(list) {
  $("#activity-title").textContent = `Aktivitas — ${longDate(state.date)}`;
  const rows = list.map((a) => el("tr", {},
    td(hms(new Date(a.time))), td(a.username), td(ACTION_LABEL[a.action] || a.action),
    td(a.room_id), td(a.booking_id), td(a.detail, "wrap")));
  $("#activity-rows").replaceChildren(...(rows.length ? rows : [el("tr", {}, el("td", { colspan: "6", class: "empty", text: "Belum ada aktivitas di tanggal ini." }))]));
}

// ---------- Rooms (admin) ----------

let editingRoom = null;

function renderRoomAdmin() {
  $("#room-rows").replaceChildren(...state.rooms.map((r) => el("tr", {},
    td(r.id), td(r.name), td(rupiah.format(r.rate_per_hour), "num"), td(r.active ? "Aktif" : "Nonaktif"),
    el("td", {}, el("div", { class: "actions" },
      el("button", { class: "btn small", type: "button", text: "Ubah", onclick: () => openRoomDialog(r) }))))));
}

function openRoomDialog(room) {
  editingRoom = room;
  const form = $("#room-form");
  form.reset();
  $("#room-error").hidden = true;
  $("#room-dialog-title").textContent = room ? `Ubah room ${room.id}` : "Room baru";
  $("#r-id").value = room ? room.id : "";
  $("#r-id").readOnly = Boolean(room);
  $("#r-name").value = room ? room.name : "";
  $("#r-rate").value = room ? String(room.rate_per_hour) : "";
  $("#r-active").checked = room ? room.active : true;
  $("#room-dialog").showModal();
  (room ? $("#r-name") : $("#r-id")).focus();
}

async function saveRoom() {
  const body = {
    id: $("#r-id").value.trim().toUpperCase(),
    name: $("#r-name").value.trim(),
    rate_per_hour: Number($("#r-rate").value),
    active: $("#r-active").checked,
  };
  if (editingRoom) {
    await api("PUT", `/api/rooms/${encodeURIComponent(editingRoom.id)}`, body);
    flash(`Room ${editingRoom.id} disimpan`);
  } else {
    await api("POST", "/api/rooms", body);
    flash(`Room ${body.id} ditambahkan`);
  }
  await refresh();
}

// ---------- Users (admin) ----------

let editingUser = null;

function renderUsers(users) {
  $("#user-rows").replaceChildren(...users.map((u) => el("tr", {},
    td(u.username), td(u.name), td(ROLE_LABEL[u.role] || u.role), td(u.active ? "Aktif" : "Nonaktif"),
    el("td", {}, el("div", { class: "actions" },
      el("button", { class: "btn small", type: "button", text: "Ubah", onclick: () => openUserDialog(u) }),
      el("button", { class: "btn small", type: "button", text: "Reset PIN", onclick: () => openPinDialog(u) }))))));
}

function openUserDialog(user) {
  editingUser = user;
  const form = $("#user-form");
  form.reset();
  $("#user-error").hidden = true;
  $("#user-dialog-title").textContent = user ? `Ubah user ${user.username}` : "User baru";
  $("#u-username").value = user ? user.username : "";
  $("#u-username").readOnly = Boolean(user);
  $("#u-name").value = user ? user.name : "";
  $("#u-role").value = user ? user.role : "staff";
  $("#u-active").checked = user ? user.active : true;
  // PIN is set on create; later changes go through "Reset PIN".
  $("#u-pin-field").hidden = Boolean(user);
  $("#u-pin").required = !user;
  $("#u-active-field").hidden = !user;
  $("#user-dialog").showModal();
  (user ? $("#u-name") : $("#u-username")).focus();
}

async function saveUser() {
  if (editingUser) {
    await api("PUT", `/api/users/${encodeURIComponent(editingUser.username)}`, {
      name: $("#u-name").value.trim(), role: $("#u-role").value, active: $("#u-active").checked,
    });
    flash(`User ${editingUser.username} disimpan`);
  } else {
    const u = await api("POST", "/api/users", {
      username: $("#u-username").value.trim().toLowerCase(), name: $("#u-name").value.trim(),
      role: $("#u-role").value, pin: $("#u-pin").value,
    });
    flash(`User ${u.username} ditambahkan`);
  }
  await refresh();
}

// ---------- PIN ----------

// pinTarget is null for "change my own PIN", or the user whose PIN an admin resets.
let pinTarget = null;

function openPinDialog(user) {
  pinTarget = user;
  $("#pin-form").reset();
  $("#pin-error").hidden = true;
  $("#pin-dialog-title").textContent = user ? `Reset PIN ${user.username}` : "Ganti PIN saya";
  $("#p-old-field").hidden = Boolean(user);
  $("#p-old").required = !user;
  $("#pin-dialog").showModal();
  (user ? $("#p-new") : $("#p-old")).focus();
}

async function savePin() {
  const newPin = $("#p-new").value;
  if (newPin !== $("#p-confirm").value) throw new Error("PIN baru dan ulangan tidak sama.");
  if (pinTarget) {
    await api("POST", `/api/users/${encodeURIComponent(pinTarget.username)}/pin`, { pin: newPin });
    flash(`PIN ${pinTarget.username} direset. Beri tahu user secara langsung, jangan lewat chat grup.`);
  } else {
    await api("POST", "/api/me/pin", { old_pin: $("#p-old").value, new_pin: newPin });
    flash("PIN Anda sudah diganti.");
  }
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
    if (state.tab !== "schedule") selectTab("schedule");
    else await refresh();
  } catch (err) {
    showError($("#booking-error"), err);
  } finally {
    btn.disabled = false;
  }
}

// ---------- Wiring ----------

function tickClock() {
  const p = parts(new Date());
  $("#clock").textContent = `${p.hour}:${p.minute}:${p.second} WIB`;
}

async function init() {
  $("#login-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const err = $("#login-error");
    err.hidden = true;
    const btn = e.currentTarget.querySelector("button[type=submit]");
    btn.disabled = true;
    try {
      const me = await api("POST", "/api/login", { username: $("#username").value.trim(), pin: $("#pin").value });
      $("#pin").value = "";
      await showApp(me);
    } catch (ex) {
      showError(err, ex);
    } finally {
      btn.disabled = false;
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
  for (const name of TABS) $(`#tab-${name}`).addEventListener("click", () => selectTab(name));
  $("#print-report").addEventListener("click", () => window.print());
  $("#new-booking").addEventListener("click", openBookingDialog);
  $("#booking-cancel").addEventListener("click", () => $("#booking-dialog").close());
  $("#booking-form").addEventListener("submit", submitBooking);
  $("#f-room").addEventListener("change", updateTotal);
  $("#f-duration").addEventListener("change", updateTotal);
  $("#new-room").addEventListener("click", () => openRoomDialog(null));
  $("#new-user").addEventListener("click", () => openUserDialog(null));
  $("#change-pin").addEventListener("click", () => openPinDialog(null));
  submitDialog($("#room-dialog"), $("#room-error"), saveRoom);
  submitDialog($("#user-dialog"), $("#user-error"), saveUser);
  submitDialog($("#pin-dialog"), $("#pin-error"), savePin);

  try {
    const me = await api("GET", "/api/me");
    state.tz = me.timezone || state.tz;
    tickClock();
    setInterval(tickClock, 1000);
    if (me.logged_in) await showApp(me);
    else showLogin();
  } catch (err) {
    showLogin();
    showError($("#login-error"), err);
  }
}

init();
