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
  pricing: [],
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

const STATUS_LABEL = {
  tentative: "Tentative", expired: "Kedaluwarsa", booked: "Confirm",
  checked_in: "Check-in", finished: "Selesai", cancelled: "Cancel",
};
// statusOf adds "expired" for tentative bookings past their hold time.
const statusOf = (b) => (b.status === "tentative" && Date.parse(b.hold_until) <= Date.now() ? "expired" : b.status);
// usageText tells how long a finished guest really stayed, and the billed time
// when that differs from the booking.
function usageText(b) {
  if (b.status !== "finished" || !(Date.parse(b.checked_in_at) > 0) || !(Date.parse(b.checked_out_at) > 0)) return "";
  const used = Math.max(0, Math.ceil((Date.parse(b.checked_out_at) - Date.parse(b.checked_in_at)) / 60000));
  return `Terpakai ${fmtDuration(used)}${b.billed_minutes ? ` · ditagih ${fmtDuration(b.billed_minutes)} (sesuai pemakaian)` : ""}`;
}

// priceText shows "Gratis (compliment)" for free bookings.
const priceText = (b) => (b.complimentary ? "Gratis (compliment)" : rupiah.format(b.total_price));
const statusText = (b) => {
  const st = statusOf(b);
  return st === "tentative" ? `Tentative s/d ${hm(new Date(b.hold_until))}` : STATUS_LABEL[st] || st;
};
const ROLE_LABEL = { staff: "Staff", supervisor: "Supervisor", admin: "Admin", accounting: "Accounting" };
const ACTION_LABEL = {
  "login": "Login",
  "pin.change": "Ganti PIN sendiri",
  "booking.create": "Booking baru",
  "booking.confirm": "Konfirmasi",
  "booking.checkin": "Check-in",
  "booking.extend": "Perpanjang",
  "booking.checkout": "Check-out",
  "booking.cancel": "Batal booking",
  "room.create": "Room baru",
  "room.update": "Ubah room",
  "pricing.update": "Ubah harga",
  "export": "Unduh CSV",
  "user.create": "User baru",
  "user.update": "Ubah user",
  "user.pin_reset": "Reset PIN user",
  "device.create": "Kode pairing TV dibuat",
  "device.pair": "TV dipasangkan",
  "device.revoke": "TV dicabut",
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
  stopMonitor();
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
  const roleText = ROLE_LABEL[me.user.role] || me.user.role;
  $("#who-am-i").textContent = `${me.user.name} · ${roleText}`;
  $("#who-name").textContent = me.user.name;
  $("#who-role").textContent = roleText;
  $("#avatar").textContent = me.user.name.split(/\s+/).filter(Boolean).slice(0, 2).map((w) => w[0].toUpperCase()).join("");
  // Hide the "Pengaturan" heading when this user has none of those menus.
  $("#nav-admin-heading").hidden = !["rooms.manage", "users.manage", "devices.manage"].some(can);
  document.querySelectorAll("[data-perm]").forEach((node) => { node.hidden = !can(node.dataset.perm); });
}

async function showApp(me) {
  applySession(me);
  $("#login-view").hidden = true;
  $("#app-view").hidden = false;
  if (!state.date) state.date = ymd(new Date());
  $("#date").value = state.date;
  [state.rooms, state.pricing] = await Promise.all([api("GET", "/api/rooms"), api("GET", "/api/pricing")]);
  selectTab("schedule");
  startMonitor();
  clearInterval(state.timer);
  state.timer = setInterval(() => { if (state.tab === "schedule") refresh().catch(() => {}); }, REFRESH_MS);
}

// ---------- Tabs ----------

const TABS = ["schedule", "list", "report", "activity", "rooms", "pricing", "users", "devices"];

function selectTab(tab) {
  state.tab = tab;
  for (const name of TABS) {
    $(`#tab-${name}`).setAttribute("aria-selected", String(name === tab));
    $(`#${name}-view`).hidden = name !== tab;
  }
  $("#page-title").textContent = $(`#tab-${tab} span`).textContent;
  setNav(false);
  // The top date picker only matters for these tabs; the list has its own range.
  $("#date").hidden = !["schedule", "activity"].includes(tab);
  refresh().catch((err) => flash(err.message, "error"));
}

async function refresh() {
  switch (state.tab) {
    case "schedule":
      state.bookings = await api("GET", `/api/bookings?date=${state.date}`);
      renderRooms();
      break;
    case "list":
      await loadList();
      break;
    case "report":
      await loadReport();
      break;
    case "activity":
      renderActivity(await api("GET", `/api/activity?date=${state.date}`));
      break;
    case "rooms":
      [state.rooms, state.pricing] = await Promise.all([api("GET", "/api/rooms"), api("GET", "/api/pricing")]);
      renderRoomAdmin();
      break;
    case "pricing":
      state.pricing = await api("GET", "/api/pricing");
      renderPricing(state.pricing.length ? state.pricing : DEFAULT_PRICING);
      break;
    case "users":
      renderUsers(await api("GET", "/api/users"));
      break;
    case "devices":
      state.rooms = await api("GET", "/api/rooms");
      renderDevices(await api("GET", "/api/devices"));
      break;
  }
}

// ---------- Schedule ----------

// ---------- Mobile menu ----------

function setNav(open) {
  const shell = $("#app-view");
  const wasOpen = shell.classList.contains("nav-open");
  shell.classList.toggle("nav-open", open);
  $("#sidebar-backdrop").hidden = !open;
  $("#menu-btn").setAttribute("aria-expanded", String(open));
  if (open) $("#sidebar .nav-item[aria-selected=true]")?.focus();
  else if (wasOpen) $("#menu-btn").focus();
}

// ---------- Schedule: timeline or cards ----------

const VIEW_KEY = "karaoke-schedule-view";
let scheduleView = (() => { try { return localStorage.getItem(VIEW_KEY) || "timeline"; } catch { return "timeline"; } })();

function setScheduleView(view) {
  scheduleView = view;
  try { localStorage.setItem(VIEW_KEY, view); } catch { /* storage blocked */ }
  $("#view-timeline").setAttribute("aria-pressed", String(view === "timeline"));
  $("#view-cards").setAttribute("aria-pressed", String(view === "cards"));
  $("#timeline-wrap").hidden = view !== "timeline";
  $("#rooms").hidden = view !== "cards";
  if (state.user && state.date) renderRooms();
}

// tzOffset returns the business time zone's UTC offset in ms at instant t.
function tzOffset(t) {
  const p = parts(new Date(t));
  return Date.UTC(+p.year, +p.month - 1, +p.day, +p.hour, +p.minute, +p.second) - Math.floor(t / 1000) * 1000;
}

// dayStart returns the instant of 00:00 of a YYYY-MM-DD day in the business zone.
function dayStart(day) {
  const utc = Date.parse(`${day}T00:00:00Z`);
  return utc - tzOffset(utc);
}

const HOUR = 3_600_000;

function renderTimeline() {
  const start0 = dayStart(state.date);
  const now = Date.now();
  const shown = state.bookings.filter((b) => b.status !== "cancelled");
  // Default window 10:00 to 02:00 next day, widened to fit the bookings.
  let from = start0 + 10 * HOUR, to = start0 + 26 * HOUR;
  for (const b of shown) {
    from = Math.min(from, Date.parse(b.start));
    to = Math.max(to, Date.parse(b.end));
  }
  from = Math.max(start0 - 6 * HOUR, Math.floor((from - start0) / HOUR) * HOUR + start0);
  to = Math.min(start0 + 34 * HOUR, Math.ceil((to - start0) / HOUR) * HOUR + start0);
  const span = to - from, hours = span / HOUR;
  const pct = (t) => `${((Math.min(Math.max(t, from), to) - from) / span) * 100}%`;

  const head = el("div", { class: "tl-hours" });
  for (let h = 0; h < hours; h++) {
    const label = hm(new Date(from + h * HOUR));
    head.append(el("div", { class: "tl-hour", style: `left:${(h / hours) * 100}%`, text: label }));
  }
  const grid = el("div", { class: "tl-grid", style: `--hours:${hours}` }, el("div", { class: "tl-corner" }), head);

  for (const room of state.rooms.filter((r) => r.active)) {
    const track = el("div", { class: `tl-track${can("booking.create") ? " can-create" : ""}`, style: `--hours:${hours}`, "data-room": room.id });
    for (const b of shown.filter((x) => x.room_id === room.id)) {
      const bs = Date.parse(b.start), be = Date.parse(b.end);
      if (be <= from || bs >= to) continue;
      let st = statusOf(b);
      if (st === "checked_in" && be <= now) st = "late";
      const block = el("button", {
        type: "button", class: `tl-block ${st}${b.complimentary ? " comp" : ""}`,
        style: `left:${pct(bs)};width:calc(${pct(be)} - ${pct(bs)})`,
        title: `${b.customer_name} · ${hm(new Date(bs))}–${hm(new Date(be))} · ${statusText(b)}`,
        "aria-label": `${room.name}: ${b.customer_name}, ${hm(new Date(bs))} sampai ${hm(new Date(be))}, ${statusText(b)}`,
        onclick: (e) => { e.stopPropagation(); openDetail(b); },
      }, el("b", { text: b.customer_name }), el("span", { text: `${hm(new Date(bs))}–${hm(new Date(be))}` }));
      track.append(block);
    }
    if (now > from && now < to && state.date === ymd(new Date(now))) {
      track.append(el("div", { class: "tl-now", style: `left:${pct(now)}`, "aria-hidden": "true" }));
    }
    if (can("booking.create")) {
      // Click on an empty spot: new booking in this room at that half hour.
      track.addEventListener("click", (e) => {
        const r = track.getBoundingClientRect();
        const t = from + ((e.clientX - r.left) / r.width) * span;
        const half = Math.floor((t - start0) / (HOUR / 2)) * (HOUR / 2) + start0;
        openBookingDialog({ roomId: room.id, start: new Date(half) });
      });
    }
    grid.append(
      el("div", { class: "tl-room" }, el("b", { text: room.name || room.id }), el("span", { text: roomRateLabel(room) })),
      track);
  }
  $("#timeline").replaceChildren(grid);
}

function openDetail(b) {
  $("#detail-title").textContent = `${b.customer_name} · ${(state.rooms.find((r) => r.id === b.room_id) || {}).name || b.room_id}`;
  $("#detail-body").replaceChildren(bookingItem(b));
  $("#detail-dialog").showModal();
}

function renderRooms() {
  if (scheduleView === "timeline") {
    renderTimeline();
    return;
  }
  const now = Date.now();
  const isToday = state.date === ymd(new Date());
  const cards = state.rooms.filter((r) => r.active).map((room) => {
    const list = state.bookings.filter((b) => b.room_id === room.id);
    const card = el("article", { class: "room" },
      el("header", {},
        el("h2", { text: room.name || room.id }),
        el("span", { class: "rate", text: roomRateLabel(room) })),
    );
    if (isToday) card.append(roomNow(list, now));
    card.append(list.length
      ? el("ol", {}, ...list.map(bookingItem))
      : el("p", { class: "empty", text: "Belum ada booking." }));
    return card;
  });
  $("#rooms").replaceChildren(...cards);
}

// With a price table the rate depends on the start time, so cards show only the code.
const roomRateLabel = (room) => (state.pricing.length ? room.id : `${room.id} · ${rupiah.format(room.rate_per_hour)}/jam`);

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

// bookingActions returns the buttons this user may use on a booking.
function bookingActions(b) {
  const start = new Date(b.start);
  const actions = el("div", { class: "actions" });
  const act = (label, fn, cls = "") => el("button", { class: `btn small ${cls}`, type: "button", text: label, onclick: (e) => run(e.currentTarget, fn) });
  const cancelBtn = () => act("Batal", () => confirm(`Batalkan booking ${b.customer_name} ${hm(start)}?`) && doAction(b, "cancel", undefined, "Booking dibatalkan"), "danger");

  if (b.status === "tentative") {
    if (can("booking.confirm")) actions.append(act("Konfirmasi", () => doAction(b, "confirm", undefined, `Booking ${b.customer_name} dikonfirmasi`), "primary"));
    if (can("booking.cancel_tentative")) actions.append(cancelBtn());
  } else if (b.status === "booked") {
    if (can("booking.checkin")) actions.append(act("Check-in", () => doAction(b, "checkin", undefined, `Check-in ${b.customer_name}`)));
    if (can("booking.cancel")) actions.append(cancelBtn());
  } else if (b.status === "checked_in") {
    if (can("booking.extend")) {
      actions.append(
        act("+30 mnt", () => doAction(b, "extend", { minutes: 30 }, "Diperpanjang 30 menit")),
        act("+1 jam", () => doAction(b, "extend", { minutes: 60 }, "Diperpanjang 1 jam")),
      );
    }
    if (can("booking.checkout")) {
      actions.append(act("Check-out", () => checkoutFlow(b), "primary"));
    }
  }
  // WhatsApp to the guest, for people who serve guests (not view-only roles).
  if (b.status !== "cancelled" && can("booking.create")) {
    actions.append(el("button", {
      class: "btn small wa", type: "button", text: "WhatsApp",
      title: guestPhone(b) ? `Kirim WhatsApp ke ${guestPhone(b)}` : "Nomor HP tamu belum diisi",
      onclick: () => openWhatsApp(b),
    }));
  }
  return actions;
}

// ---------- Check-out (early check-out billing) ----------

const STEP = 30;
// billableMinutes mirrors booking.BillableMinutes on the server.
const billableMinutes = (used, booked) => Math.min(Math.max(Math.ceil(used / STEP) * STEP, 60), booked);

let checkoutBooking = null;

// checkoutFlow asks a plain confirmation, or for an early check-out lets
// staff choose to bill the booked time or (supervisor/admin) the time used.
async function checkoutFlow(b) {
  if ($("#detail-dialog").open) $("#detail-dialog").close();
  const now = Date.now() + monitor.offset;
  const end = Date.parse(b.end), start = Date.parse(b.start);
  const booked = Math.round((end - start) / 60000);
  if (b.complimentary || now >= end - 60_000) {
    if (confirm(`Check-out ${b.customer_name}? Alarm di TV akan berhenti.`)) {
      await doAction(b, "checkout", {}, `Check-out ${b.customer_name}`);
    }
    return;
  }
  const checkedIn = Date.parse(b.checked_in_at) > 0 ? Date.parse(b.checked_in_at) : start;
  const used = Math.max(0, Math.ceil((now - checkedIn) / 60000));
  const billed = billableMinutes(used, booked);
  const usagePrice = Math.round(b.rate_per_hour * billed / 60);
  checkoutBooking = b;
  $("#co-title").textContent = `Check-out lebih awal · ${b.customer_name}`;
  $("#co-facts").textContent = `Booking ${fmtDuration(booked)} (${hm(new Date(start))}–${hm(new Date(end))}), terpakai ${fmtDuration(used)} sejak check-in ${hm(new Date(checkedIn))}.`;
  $("#co-booked").textContent = `${fmtDuration(booked)} · ${rupiah.format(b.total_price)}`;
  $("#co-usage-text").textContent = billed >= booked
    ? `Sama dengan booking (${fmtDuration(booked)})`
    : `${fmtDuration(billed)} · ${rupiah.format(usagePrice)} (dibulatkan ke atas per 30 menit, minimal 1 jam)`;
  const allowed = can("booking.bill_by_usage") && billed < booked;
  $("#co-usage").disabled = !allowed;
  $("#co-usage-hint").textContent = can("booking.bill_by_usage") ? "" : "Tagih sesuai pemakaian hanya bisa oleh supervisor atau admin.";
  $("#checkout-form").reset();
  $("#checkout-dialog").showModal();
}

async function submitCheckout(e) {
  e.preventDefault();
  const b = checkoutBooking;
  const bill = $("#checkout-form").querySelector("input[name=bill]:checked").value;
  const btn = $("#checkout-form button[type=submit]");
  btn.disabled = true;
  try {
    await doAction(b, "checkout", { bill }, bill === "usage" ? `Check-out ${b.customer_name}, ditagih sesuai pemakaian` : `Check-out ${b.customer_name}`);
    $("#checkout-dialog").close();
  } catch (err) {
    flash(err.message, "error");
  } finally {
    btn.disabled = false;
  }
}

// ---------- WhatsApp to the guest ----------
// Opens WhatsApp (web, desktop or phone) with a ready message. Nothing is
// sent by this system: staff check the text and press send in WhatsApp.

const BRAND = "Hong Kong Karaoke";
const looksLikePhone = (s) => /^[+\d][\d\s\-.]{7,}$/.test((s || "").trim());

// waNumber turns 0812-3456-789 / +62 812... / 812... into 628123456789.
function waNumber(raw) {
  let d = (raw || "").replace(/\D/g, "");
  if (d.startsWith("0")) d = "62" + d.slice(1);
  else if (d.startsWith("8")) d = "62" + d;
  return d.length >= 10 && d.length <= 15 ? d : "";
}

// guestPhone uses the phone field, or the name when a number was typed there.
const guestPhone = (b) => (b.phone || (looksLikePhone(b.customer_name) ? b.customer_name : "")).trim();
const greetName = (b) => (looksLikePhone(b.customer_name) ? "Kak" : `Kak ${b.customer_name}`);

function defaultWaKind(b) {
  if (b.status === "checked_in") return "ending";
  if (b.status === "finished") return "thanks";
  return "confirm";
}

function waMessage(kind, b) {
  const start = new Date(b.start), end = new Date(b.end);
  const room = (state.rooms.find((r) => r.id === b.room_id) || {}).name || b.room_id;
  const minutes = Math.round((end - start) / 60000);
  const date = longDate(ymd(start));
  const today = ymd(start) === ymd(new Date());
  switch (kind) {
    case "reminder":
      return `Halo ${greetName(b)}, kami dari ${BRAND} ingin mengingatkan booking Anda ${today ? "hari ini" : date} pukul ${hm(start)} di ${room} (${fmtDuration(minutes)}).\n\nKami tunggu kedatangannya. Jika ada perubahan, silakan balas pesan ini. Terima kasih!`;
    case "ending":
      return `Halo ${greetName(b)}, waktu bernyanyi Anda di ${room} akan selesai pukul ${hm(end)}.\n\nJika ingin menambah waktu (+30 menit atau +1 jam), silakan balas pesan ini atau hubungi kasir. Terima kasih!`;
    case "thanks":
      return `Terima kasih ${greetName(b)} sudah bernyanyi di ${BRAND}!\n\n• ${room}, ${date}\n• ${hm(start)}–${hm(end)} (${fmtDuration(minutes)})\n• Total: ${priceText(b)}\n\nSampai jumpa lagi 🎤`;
    default: {
      const lines = [
        `Halo ${greetName(b)}, terima kasih telah booking di ${BRAND}.`,
        "",
        "Detail booking:",
        `• Room: ${room}`,
        `• Tanggal: ${date}`,
        `• Jam: ${hm(start)}–${hm(end)} (${fmtDuration(minutes)})`,
        `• Total: ${priceText(b)}`,
        `• Kode booking: ${b.id}`,
        "",
      ];
      if (b.status === "tentative") {
        const hold = new Date(b.hold_until);
        lines.push(`Status: TENTATIVE. Slot kami tahan sampai ${ymd(hold) === ymd(start) ? "" : longDate(ymd(hold)) + " "}pukul ${hm(hold)}. Mohon balas pesan ini untuk konfirmasi.`);
      } else {
        lines.push("Status: TERKONFIRMASI.");
      }
      lines.push("", "Sampai jumpa!");
      return lines.join("\n");
    }
  }
}

let waBooking = null;

function openWhatsApp(b) {
  if ($("#detail-dialog").open) $("#detail-dialog").close();
  waBooking = b;
  $("#wa-error").hidden = true;
  $("#wa-title").textContent = `Kirim WhatsApp · ${looksLikePhone(b.customer_name) ? "tamu" : b.customer_name}`;
  $("#wa-phone").value = guestPhone(b);
  $("#wa-kind").value = defaultWaKind(b);
  $("#wa-text").value = waMessage($("#wa-kind").value, b);
  $("#wa-dialog").showModal();
  const first = $("#wa-phone").value ? $("#wa-text") : $("#wa-phone");
  first.focus();
  // Start reading the message from the top.
  $("#wa-text").setSelectionRange(0, 0);
  $("#wa-text").scrollTop = 0;
}

function sendWhatsApp(e) {
  e.preventDefault();
  const number = waNumber($("#wa-phone").value);
  if (!number) {
    showError($("#wa-error"), new Error("Nomor WhatsApp tidak valid. Contoh: 081234567890 atau 6281234567890."));
    $("#wa-phone").focus();
    return;
  }
  const text = $("#wa-text").value.trim();
  if (!text) return;
  window.open(`https://wa.me/${number}?text=${encodeURIComponent(text)}`, "_blank", "noopener");
  $("#wa-dialog").close();
  flash(`WhatsApp dibuka untuk ${number}. Periksa pesannya lalu tekan Kirim di WhatsApp.`);
}

const byText = (b) => [
  b.created_by && `dibuat ${b.created_by}`,
  b.confirmed_by && `konfirmasi ${b.confirmed_by}`,
  b.checked_in_by && `in ${b.checked_in_by}`,
  b.checked_out_by && `out ${b.checked_out_by}`,
  b.cancelled_by && `batal ${b.cancelled_by}`,
].filter(Boolean).join(", ");

function bookingItem(b) {
  const start = new Date(b.start), end = new Date(b.end);
  const minutes = Math.round((end - start) / 60000);
  const actions = bookingActions(b);
  const st = statusOf(b);
  const by = byText(b);
  const dayPrefix = ymd(start) !== state.date ? `${start.toLocaleDateString("id-ID", { day: "2-digit", month: "2-digit", timeZone: state.tz })} ` : "";
  return el("li", { class: `bk ${st}` },
    el("div", { class: "line1" },
      el("span", { class: "time", text: `${dayPrefix}${hm(start)}–${hm(end)}` }),
      el("span", { class: "badges" },
        b.complimentary ? el("span", { class: "badge comp", text: "Compliment" }) : null,
        el("span", { class: `badge ${st}`, text: statusText(b) }))),
    el("div", { class: "who", text: b.customer_name + (b.phone ? ` · ${b.phone}` : "") }),
    el("div", { class: "meta", text: `${fmtDuration(minutes)} · ${priceText(b)}${b.notes ? ` · ${b.notes}` : ""}` }),
    usageText(b) ? el("div", { class: "meta", text: usageText(b) }) : null,
    b.complimentary ? el("div", { class: "meta", text: `Alasan compliment: ${b.compliment_reason}${b.voucher_number ? ` · Voucher ${b.voucher_number}` : ""}` }) : null,
    by ? el("div", { class: "meta", text: by }) : null,
    actions.childElementCount ? actions : null,
  );
}

async function doAction(b, action, body, okText) {
  await api("POST", `/api/bookings/${encodeURIComponent(b.id)}/${action}`, body ?? {});
  if ($("#detail-dialog").open) $("#detail-dialog").close();
  flash(okText);
  await Promise.all([refresh(), loadMonitor().catch(() => {})]);
}

// ---------- Reminders ----------
// Watches today's checked-in bookings whatever tab is open. At 5 minutes
// left: an orange card and one warning sound. When time is up: a red card
// and a siren every few seconds until check-out, extension, or "Senyapkan".
// The TV in the room shows its own warning too.

const WARN_MS = 5 * 60 * 1000;
const MONITOR_MS = 15_000;
const ALARM_EVERY_MS = 2_500;
const Sound = window.KaraokeSound;

const monitor = {
  bookings: [],
  offset: 0, // server time minus this computer's time, in ms
  warned: new Set(), // "<id>|<end>" already warned with sound
  hidden: new Set(), // warning cards closed by staff
  muted: new Set(), // overtime alarms silenced by staff
  poll: 0,
  tick: 0,
  lastAlarm: 0,
  title: document.title,
};

const reminderKey = (b) => `${b.id}|${b.end}`;
const mmss = (ms) => {
  const s = Math.max(0, Math.round(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
};

function startMonitor() {
  stopMonitor();
  loadMonitor().catch(() => {});
  monitor.poll = setInterval(() => loadMonitor().catch(() => {}), MONITOR_MS);
  monitor.tick = setInterval(renderReminders, 1000);
  updateSoundButton();
}

function stopMonitor() {
  clearInterval(monitor.poll);
  clearInterval(monitor.tick);
  monitor.bookings = [];
  $("#reminders").replaceChildren();
  document.title = monitor.title;
}

async function loadMonitor() {
  // Reminders are for people who can act on them (extend / check out).
  if (!can("booking.checkout")) return;
  const today = ymd(new Date(Date.now() + monitor.offset));
  const list = await api("GET", `/api/bookings?date=${today}`);
  monitor.bookings = list.filter((b) => b.status === "checked_in");
  renderReminders();
}

function renderReminders() {
  const now = Date.now() + monitor.offset;
  const due = monitor.bookings
    .map((b) => ({ b, left: Date.parse(b.end) - now }))
    .filter(({ b, left }) => left <= WARN_MS && !(left > 0 && monitor.hidden.has(reminderKey(b))))
    .sort((x, y) => x.left - y.left);

  let alarmNeeded = false;
  const cards = due.map(({ b, left }) => {
    const key = reminderKey(b);
    const over = left <= 0;
    const muted = over && monitor.muted.has(key);
    if (!over && !monitor.warned.has(key)) {
      monitor.warned.add(key);
      Sound.warning();
    }
    if (over && !muted) alarmNeeded = true;

    const room = state.rooms.find((r) => r.id === b.room_id);
    const actions = bookingActions(b);
    actions.append(el("button", {
      class: "btn small", type: "button", text: over ? (muted ? "Bunyikan lagi" : "Senyapkan") : "Tutup",
      onclick: () => {
        if (!over) monitor.hidden.add(key);
        else if (muted) monitor.muted.delete(key);
        else monitor.muted.add(key);
        renderReminders();
      },
    }));
    return el("section", { class: `reminder ${over ? "overtime" : "warning"}${muted ? " muted" : ""}` },
      el("div", { class: "r-title", text: `${over ? "⚠ WAKTU HABIS" : "⏰ Hampir habis"} · ${room ? room.name : b.room_id}` }),
      el("div", { class: "r-time", text: over ? `lewat ${mmss(-left)}` : `sisa ${mmss(left)}` }),
      el("div", { class: "r-guest", text: `${b.customer_name} · selesai ${hm(new Date(b.end))}` }),
      actions);
  });
  // Sound blocked while something needs attention: a loud notice on top.
  if (due.length && Sound.locked() && can("booking.checkout")) {
    cards.unshift(el("button", {
      type: "button", class: "sound-blocked",
      onclick: () => { Sound.unlock(() => { Sound.warning(); updateSoundButton(); renderReminders(); }); },
    },
    el("b", { text: "🔇 Alarm tidak berbunyi" }),
    el("span", { text: "Suara diblokir browser. Klik di sini untuk mengaktifkan." })));
  }
  $("#reminders").replaceChildren(...cards);

  if (alarmNeeded && now - monitor.lastAlarm >= ALARM_EVERY_MS) {
    monitor.lastAlarm = now;
    Sound.alarm();
  }
  // Blink the tab title so the cashier notices from another window.
  document.title = due.length && Math.floor(now / 1000) % 2
    ? `⏰ (${due.length}) ${due[0].left <= 0 ? "Waktu habis" : "Hampir habis"}`
    : monitor.title;
  updateSoundButton();
}

// The small header chip shows while sound is blocked; only for people who
// get alarms (accounting does not).
function updateSoundButton() {
  $("#sound-unlock").hidden = !(Sound.locked() && can("booking.checkout"));
}

// ---------- CSV download ----------

// download fetches a CSV with the session cookie and saves it, so errors
// show as a message instead of a JSON page.
async function download(path) {
  const res = await fetch(path, { credentials: "same-origin" });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error(data.error || `Error ${res.status}`);
  }
  const name = (/filename="([^"]+)"/.exec(res.headers.get("Content-Disposition") || "") || [])[1] || "export.csv";
  const url = URL.createObjectURL(await res.blob());
  const a = el("a", { href: url, download: name });
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10_000);
  flash(`${name} diunduh`);
}

// ---------- Report ----------

// Quick periods for the report, computed in the business time zone.
function reportPreset(name) {
  const today = ymd(new Date());
  const [y, m, d] = today.split("-").map(Number);
  const iso = (dt) => dt.toISOString().slice(0, 10);
  const day = (offset) => iso(new Date(Date.UTC(y, m - 1, d + offset)));
  switch (name) {
    case "yesterday": return [day(-1), day(-1)];
    case "7d": return [day(-6), today];
    case "month": return [iso(new Date(Date.UTC(y, m - 1, 1))), today];
    case "lastmonth": return [iso(new Date(Date.UTC(y, m - 2, 1))), iso(new Date(Date.UTC(y, m - 1, 0)))];
    default: return [today, today];
  }
}

function applyPreset() {
  const name = $("#rp-preset").value;
  if (name === "custom") return;
  const [from, to] = reportPreset(name);
  $("#rp-from").value = from;
  $("#rp-to").value = to;
}

const reportQuery = () => new URLSearchParams({ from: $("#rp-from").value, to: $("#rp-to").value });

async function loadReport() {
  if (!$("#rp-from").value) applyPreset();
  renderReport(await api("GET", `/api/report?${reportQuery()}`));
}

const shortDate = (s) => new Intl.DateTimeFormat("id-ID", { weekday: "short", day: "2-digit", month: "short", year: "numeric", timeZone: "UTC" }).format(new Date(`${s}T00:00:00Z`));

function renderReport(rep) {
  $("#report-title").textContent = rep.from === rep.to
    ? `Laporan — ${longDate(rep.from)}`
    : `Laporan — ${shortDate(rep.from)} s/d ${shortDate(rep.to)} (${rep.days.length} hari)`;
  const tile = (label, value) => el("div", { class: "tile" }, el("div", { class: "label", text: label }), el("div", { class: "value", text: value }));
  $("#report-tiles").replaceChildren(
    tile("Pendapatan", rupiah.format(rep.revenue)),
    tile("Jam terpakai", fmtDuration(rep.minutes)),
    tile("Selesai", String(rep.finished)),
    tile("Sedang dipakai", String(rep.checked_in)),
    tile("Belum check-in", String(rep.booked)),
    tile("Tentative", String(rep.tentative)),
    tile("Compliment", rep.compliments ? `${rep.compliments} · ${rupiah.format(rep.compliment_value)}` : "0"),
    tile("Batal", String(rep.cancelled)),
  );
  $("#report-rows").replaceChildren(...rep.rooms.map((r) => el("tr", {},
    td(r.room_name || r.room_id), td(String(r.bookings), "num"), td(fmtDuration(r.minutes), "num"), td(rupiah.format(r.revenue), "num"))));
  $("#report-total").replaceChildren(
    td("Total"), td(String(rep.finished + rep.checked_in), "num"), td(fmtDuration(rep.minutes), "num"), td(rupiah.format(rep.revenue), "num"));
  // A one-day report needs no per-day table.
  $("#report-days-block").hidden = rep.days.length < 2;
  $("#report-days").replaceChildren(...rep.days.map((d) => el("tr", {},
    td(shortDate(d.date)), td(String(d.bookings), "num"), td(fmtDuration(d.minutes), "num"), td(rupiah.format(d.revenue), "num"))));
}

// ---------- Booking list ----------

async function loadList() {
  const f = $("#list-filter");
  if (!$("#l-from").value) {
    $("#l-from").value = state.date;
    const to = new Date(`${state.date}T00:00:00Z`);
    to.setUTCDate(to.getUTCDate() + 30);
    $("#l-to").value = to.toISOString().slice(0, 10);
  }
  const params = new URLSearchParams(new FormData(f));
  const page = await api("GET", `/api/bookings/list?${params}`);
  const roomName = (id) => (state.rooms.find((r) => r.id === id) || {}).name || id;
  const rows = page.bookings.map((b) => {
    const start = new Date(b.start), end = new Date(b.end);
    const st = statusOf(b);
    return el("tr", {},
      td(new Intl.DateTimeFormat("id-ID", { weekday: "short", day: "2-digit", month: "short", timeZone: state.tz }).format(start)),
      td(`${hm(start)}–${hm(end)}`),
      td(roomName(b.room_id)),
      td(b.customer_name, "wrap"),
      td(b.phone),
      td(priceText(b), "num"),
      el("td", {}, b.complimentary ? el("span", { class: "badge comp", text: b.voucher_number ? `Compliment · ${b.voucher_number}` : "Compliment" }) : null, " ", el("span", { class: `badge ${st}`, text: statusText(b) })),
      td(byText(b), "wrap"),
      el("td", {}, bookingActions(b)));
  });
  $("#list-rows").replaceChildren(...(rows.length ? rows : [el("tr", {}, el("td", { colspan: "9", class: "empty", text: "Tidak ada booking dengan filter ini." }))]));
  $("#list-summary").textContent = `${page.summary.count} booking · total ${rupiah.format(page.summary.total_price)}`;
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
  $("#rooms-pricing-note").hidden = !state.pricing.length;
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

// ---------- Pricing (admin) ----------

// Shown as a starting point when the table is still empty.
const DEFAULT_PRICING = [
  { day_type: "weekday", start: "11:00", end: "17:00", rate_per_hour: 60000 },
  { day_type: "weekday", start: "17:00", end: "11:00", rate_per_hour: 120000 },
  { day_type: "weekend", start: "11:00", end: "17:00", rate_per_hour: 85000 },
  { day_type: "weekend", start: "17:00", end: "11:00", rate_per_hour: 170000 },
];

function pricingRow(rule) {
  const day = el("select", { "aria-label": "Hari" },
    el("option", { value: "weekday", text: "Senin – Jumat" }),
    el("option", { value: "weekend", text: "Sabtu – Minggu" }));
  day.value = rule.day_type;
  const start = el("input", { type: "time", required: "", "aria-label": "Jam mulai dari", value: rule.start });
  const end = el("input", { type: "time", required: "", "aria-label": "Sampai sebelum", value: rule.end });
  const rate = el("input", { inputmode: "numeric", required: "", pattern: "[0-9]{1,9}", "aria-label": "Harga per jam", value: String(rule.rate_per_hour || "") });
  const tr = el("tr", { class: "pricing-row" },
    el("td", {}, day), el("td", {}, start), el("td", {}, end), el("td", { class: "num" }, rate),
    el("td", {}, el("button", { class: "btn small danger", type: "button", text: "Hapus", onclick: () => tr.remove() })));
  tr.read = () => ({ day_type: day.value, start: start.value, end: end.value, rate_per_hour: Number(rate.value) });
  return tr;
}

function renderPricing(rules) {
  $("#pricing-error").hidden = true;
  $("#pricing-rows").replaceChildren(...rules.map(pricingRow));
}

async function savePricing(e) {
  e.preventDefault();
  const form = e.currentTarget;
  if (!form.reportValidity()) return;
  const btn = form.querySelector("button[type=submit]");
  btn.disabled = true;
  $("#pricing-error").hidden = true;
  try {
    const rules = [...document.querySelectorAll("#pricing-rows .pricing-row")].map((tr) => tr.read());
    state.pricing = await api("PUT", "/api/pricing", { rules });
    renderPricing(state.pricing);
    flash("Harga disimpan. Berlaku untuk booking baru.");
  } catch (err) {
    showError($("#pricing-error"), err);
  } finally {
    btn.disabled = false;
  }
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

// ---------- TVs (admin) ----------

const DEVICE_STATUS = { pending: "Menunggu pairing", active: "Aktif", revoked: "Dicabut" };

function renderDevices(list) {
  const roomName = (id) => (state.rooms.find((r) => r.id === id) || {}).name || id;
  const now = Date.now();
  const rows = list.map((d) => {
    const expired = d.status === "pending" && Date.parse(d.pair_expires) <= now;
    const status = expired ? "Kode kedaluwarsa" : DEVICE_STATUS[d.status] || d.status;
    const when = d.status === "active" || d.status === "revoked"
      ? (Date.parse(d.paired_at) > 0 ? `${ymd(new Date(d.paired_at))} ${hm(new Date(d.paired_at))}` : "")
      : `kode s/d ${hm(new Date(d.pair_expires))}`;
    const actions = el("div", { class: "actions" });
    if (d.status !== "revoked") {
      actions.append(el("button", {
        class: "btn small danger", type: "button", text: d.status === "pending" ? "Batalkan kode" : "Cabut",
        onclick: (e) => run(e.currentTarget, async () => {
          if (!confirm(`Cabut ${d.name} (${roomName(d.room_id)})? TV ini harus dipairing ulang untuk dipakai lagi.`)) return;
          await api("POST", `/api/devices/${encodeURIComponent(d.id)}/revoke`, {});
          flash(`${d.name} dicabut`);
          await refresh();
        }),
      }));
    }
    return el("tr", {},
      td(d.name), td(roomName(d.room_id)),
      el("td", {}, el("span", { class: `badge ${expired ? "revoked" : d.status}`, text: status })),
      td(when), td(d.created_by + (d.revoked_by ? `, dicabut ${d.revoked_by}` : "")),
      el("td", {}, actions));
  });
  $("#device-rows").replaceChildren(...(rows.length ? rows : [el("tr", {}, el("td", { colspan: "6", class: "empty", text: "Belum ada TV yang dipasangkan." }))]));
}

function openDeviceDialog() {
  $("#device-form").reset();
  $("#device-error").hidden = true;
  $("#device-step1").hidden = false;
  $("#device-step2").hidden = true;
  $("#d-room").replaceChildren(...state.rooms.filter((r) => r.active).map((r) => el("option", { value: r.id, text: `${r.name} (${r.id})` })));
  const suggest = () => { $("#d-name").value = `TV ${$("#d-room").selectedOptions[0]?.textContent.replace(/ \(.*\)$/, "") || ""}`; };
  $("#d-room").onchange = suggest;
  suggest();
  $("#device-dialog").showModal();
  $("#d-room").focus();
}

async function createPairing(e) {
  e.preventDefault();
  const form = e.currentTarget;
  if (!form.reportValidity()) return;
  const btn = form.querySelector("#device-step1 button[type=submit]");
  btn.disabled = true;
  $("#device-error").hidden = true;
  try {
    const pc = await api("POST", "/api/devices", { room_id: $("#d-room").value, name: $("#d-name").value.trim() });
    $("#d-show-room").textContent = $("#d-room").selectedOptions[0].textContent;
    $("#d-code").textContent = pc.code;
    $("#d-expires").textContent = `Berlaku sampai ${hm(new Date(pc.expires))} WIB (15 menit), sekali pakai.`;
    $("#device-step1").hidden = true;
    $("#device-step2").hidden = false;
    if (state.tab === "devices") refresh().catch(() => {});
  } catch (err) {
    showError($("#device-error"), err);
  } finally {
    btn.disabled = false;
  }
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

// openBookingDialog opens the form; the timeline passes a room and start time.
function openBookingDialog(prefill = {}) {
  const form = $("#booking-form");
  form.reset();
  $("#f-kind-hint").hidden = true;
  $("#f-comp-field").hidden = true;
  $("#f-voucher-field").hidden = true;
  $("#f-comp-reason").required = false;
  $("#f-total").classList.remove("dialog-total-comp");
  $("#booking-error").hidden = true;
  $("#f-room").replaceChildren(...state.rooms.filter((r) => r.active).map((r) =>
    el("option", { value: r.id, text: state.pricing.length ? `${r.name || r.id}` : `${r.name || r.id} — ${rupiah.format(r.rate_per_hour)}/jam` })));
  const durations = [];
  for (let m = 30; m <= 720; m += 30) durations.push(el("option", { value: String(m), text: fmtDuration(m) }));
  $("#f-duration").replaceChildren(...durations);
  $("#f-duration").value = "60";

  // Default start: the clicked time, else now on today, or 19:00 on another day.
  const now = new Date();
  if (prefill.roomId) $("#f-room").value = prefill.roomId;
  $("#f-start").value = prefill.start ? localInput(prefill.start)
    : state.date === ymd(now) ? localInput(now) : `${state.date}T19:00`;
  updateTotal();
  $("#booking-dialog").showModal();
  $("#f-name").focus();
}

// updateTotal asks the server for the price, so the form always shows what
// will be charged (the rate depends on day and start time).
let quoteSeq = 0;
async function updateTotal() {
  const room = $("#f-room").value, start = $("#f-start").value, minutes = $("#f-duration").value;
  if (!room || !start || !minutes) return;
  const seq = ++quoteSeq;
  try {
    const q = await api("GET", `/api/bookings/quote?${new URLSearchParams({ room_id: room, start, duration_minutes: minutes })}`);
    if (seq !== quoteSeq) return; // a newer request is on its way
    const comp = $("input[name=kind][value=compliment]").checked;
    $("#f-total").classList.toggle("dialog-total-comp", comp);
    $("#f-total").textContent = comp
      ? "Total: Rp 0 · Compliment"
      : `Total: ${rupiah.format(q.total_price)} (${rupiah.format(q.rate_per_hour)}/jam)`;
  } catch (err) {
    if (seq === quoteSeq) $("#f-total").textContent = `Harga belum bisa dihitung: ${err.message}`;
  }
}

async function submitBooking(e) {
  e.preventDefault();
  const form = e.currentTarget;
  // Keep names and phone numbers in their own fields (WhatsApp needs the phone).
  const name = $("#f-name");
  name.setCustomValidity(looksLikePhone(name.value) ? "Ini terlihat seperti nomor HP. Isi nama tamu di sini, dan nomor HP di kolom No. HP." : "");
  if (!form.reportValidity()) return;
  const data = Object.fromEntries(new FormData(form));
  data.duration_minutes = Number(data.duration_minutes);
  data.tentative = data.kind === "tentative";
  data.complimentary = data.kind === "compliment";
  if (!data.complimentary) {
    delete data.compliment_reason;
    delete data.voucher_number;
  }
  delete data.kind;
  const btn = $("#booking-submit");
  btn.disabled = true;
  try {
    const b = await api("POST", "/api/bookings", data);
    $("#booking-dialog").close();
    const kind = b.complimentary ? "compliment" : b.status === "tentative" ? `tentative, ditahan s/d ${hm(new Date(b.hold_until))}` : "confirm";
    const price = b.complimentary ? `gratis${b.voucher_number ? `, voucher ${b.voucher_number}` : ""}` : priceText(b);
    flash(`Booking ${b.customer_name} ${hm(new Date(b.start))}–${hm(new Date(b.end))} tersimpan (${kind}, ${price})`);
    const day = ymd(new Date(b.start));
    if (day !== state.date) { state.date = day; $("#date").value = day; }
    await refresh();
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
  $("#report-export").addEventListener("click", (e) => {
    if (!$("#report-filter").reportValidity()) return;
    run(e.currentTarget, () => download(`/api/export/report.csv?${reportQuery()}`));
  });
  $("#rp-preset").addEventListener("change", () => {
    applyPreset();
    if ($("#rp-preset").value !== "custom") loadReport().catch((err) => flash(err.message, "error"));
  });
  for (const id of ["#rp-from", "#rp-to"]) $(id).addEventListener("input", () => { $("#rp-preset").value = "custom"; });
  $("#report-filter").addEventListener("submit", (e) => {
    e.preventDefault();
    if (e.currentTarget.reportValidity()) loadReport().catch((err) => flash(err.message, "error"));
  });
  $("#list-export").addEventListener("click", (e) => {
    const f = $("#list-filter");
    if (!f.reportValidity()) return;
    run(e.currentTarget, () => download(`/api/export/bookings.csv?${new URLSearchParams(new FormData(f))}`));
  });
  $("#new-booking").addEventListener("click", () => openBookingDialog());
  $("#menu-btn").addEventListener("click", () => setNav(!$("#app-view").classList.contains("nav-open")));
  $("#sidebar-backdrop").addEventListener("click", () => setNav(false));
  document.addEventListener("keydown", (e) => { if (e.key === "Escape" && $("#app-view").classList.contains("nav-open")) setNav(false); });
  $("#view-timeline").addEventListener("click", () => setScheduleView("timeline"));
  $("#view-cards").addEventListener("click", () => setScheduleView("cards"));
  $("#detail-close").addEventListener("click", () => $("#detail-dialog").close());
  setScheduleView(scheduleView);
  $("#booking-cancel").addEventListener("click", () => $("#booking-dialog").close());
  $("#booking-form").addEventListener("submit", submitBooking);
  $("#f-room").addEventListener("change", updateTotal);
  $("#f-duration").addEventListener("change", updateTotal);
  $("#f-start").addEventListener("change", updateTotal);
  $("#f-name").addEventListener("input", () => $("#f-name").setCustomValidity(""));
  $("#wa-form").addEventListener("submit", sendWhatsApp);
  $("#checkout-form").addEventListener("submit", submitCheckout);
  $("#checkout-dialog").querySelectorAll("[data-close]").forEach((x) => x.addEventListener("click", () => $("#checkout-dialog").close()));
  $("#wa-kind").addEventListener("change", () => { if (waBooking) $("#wa-text").value = waMessage($("#wa-kind").value, waBooking); });
  $("#wa-dialog").querySelectorAll("[data-close]").forEach((x) => x.addEventListener("click", () => $("#wa-dialog").close()));
  $("#pricing-add").addEventListener("click", () => $("#pricing-rows").append(pricingRow({ day_type: "weekday", start: "11:00", end: "17:00", rate_per_hour: "" })));
  $("#pricing-form").addEventListener("submit", savePricing);
  for (const r of document.querySelectorAll("input[name=kind]")) {
    r.addEventListener("change", () => {
      const kind = $("input[name=kind]:checked").value;
      $("#f-kind-hint").hidden = kind !== "tentative";
      $("#f-comp-field").hidden = kind !== "compliment";
      $("#f-voucher-field").hidden = kind !== "compliment";
      $("#f-comp-reason").required = kind === "compliment";
      updateTotal();
    });
  }
  $("#list-filter").addEventListener("submit", (e) => {
    e.preventDefault();
    if (e.currentTarget.reportValidity()) loadList().catch((err) => flash(err.message, "error"));
  });
  $("#new-room").addEventListener("click", () => openRoomDialog(null));
  $("#new-user").addEventListener("click", () => openUserDialog(null));
  $("#new-device").addEventListener("click", openDeviceDialog);
  $("#device-form").addEventListener("submit", createPairing);
  $("#device-dialog").querySelectorAll("[data-close]").forEach((b) => b.addEventListener("click", () => {
    $("#device-dialog").close();
    if (state.tab === "devices") refresh().catch(() => {});
  }));
  $("#change-pin").addEventListener("click", () => openPinDialog(null));
  submitDialog($("#room-dialog"), $("#room-error"), saveRoom);
  submitDialog($("#user-dialog"), $("#user-error"), saveUser);
  submitDialog($("#pin-dialog"), $("#pin-error"), savePin);

  // Browsers allow sound only after a click; any click on the page unlocks it.
  document.addEventListener("click", () => { if (Sound.locked()) Sound.unlock(updateSoundButton); });
  // Play the chime once so staff hear that sound now works.
  $("#sound-unlock").addEventListener("click", () => { Sound.unlock(() => { Sound.warning(); updateSoundButton(); renderReminders(); }); });

  try {
    const me = await api("GET", "/api/me");
    monitor.offset = Date.parse(me.now) - Date.now();
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
