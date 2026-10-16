"use strict";

const CATEGORIES = {
  food: "Еда",
  transport: "Транспорт",
  housing: "Жильё",
  entertainment: "Развлечения",
  health: "Здоровье",
  shopping: "Покупки",
  other: "Другое",
};

const $ = (id) => document.getElementById(id);
const rubles = new Intl.NumberFormat("ru-RU", { style: "currency", currency: "RUB", maximumFractionDigits: 0 });
const rublesKopecks = new Intl.NumberFormat("ru-RU", { style: "currency", currency: "RUB", minimumFractionDigits: 2 });

// money форматирует копейки: «350 ₽», но «1 299,90 ₽».
const money = (kopecks) => (kopecks % 100 === 0 ? rubles : rublesKopecks).format(kopecks / 100);
const dateFmt = new Intl.DateTimeFormat("ru-RU", { day: "numeric", month: "short", year: "numeric" });

const state = { users: [], expenses: [], editing: null };

// ---------- API ----------

async function api(path, options = {}) {
  const res = await fetch(path, {
    ...options,
    headers: options.body ? { "Content-Type": "application/json" } : undefined,
  });
  if (res.status === 204) return null;
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new Error(data?.error || `Ошибка ${res.status}`);
  return data;
}

// ---------- Даты ----------

function iso(d) {
  const pad = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

function presetRange(name) {
  const today = new Date();
  const y = today.getFullYear(), m = today.getMonth();
  switch (name) {
    case "week": {
      const monday = new Date(today);
      monday.setDate(today.getDate() - ((today.getDay() + 6) % 7));
      return [iso(monday), iso(today)];
    }
    case "month":
      return [iso(new Date(y, m, 1)), iso(today)];
    case "prev-month":
      return [iso(new Date(y, m - 1, 1)), iso(new Date(y, m, 0))];
    default:
      return ["", ""];
  }
}

function formatDate(s) {
  const [y, m, d] = s.split("-").map(Number);
  return dateFmt.format(new Date(y, m - 1, d));
}

// Суммы складываем в копейках: в JS 0.1 + 0.2 !== 0.3.
function toKopecks(amount) {
  const [rub, kop = ""] = amount.split(".");
  return Number(rub) * 100 + Number(kop.padEnd(2, "0").slice(0, 2));
}

// ---------- Отрисовка ----------

function userName(id) {
  const u = state.users.find((u) => u.id === id);
  if (!u) return String(id);
  return u.username ? `${u.first_name} (@${u.username})` : u.first_name;
}

function fillSelect(select, options, { keepFirst = false } = {}) {
  if (!keepFirst) select.replaceChildren();
  else select.replaceChildren(select.options[0]);
  for (const [value, label] of options) {
    select.append(new Option(label, value));
  }
}

function cell(text, className) {
  const td = document.createElement("td");
  td.textContent = text; // textContent, не innerHTML: комментарий пишет пользователь в Telegram
  if (className) td.className = className;
  return td;
}

function button(text, className, onClick) {
  const b = document.createElement("button");
  b.type = "button";
  b.className = className;
  b.textContent = text;
  b.addEventListener("click", onClick);
  return b;
}

function render() {
  const rows = state.expenses.map((e) => {
    const tr = document.createElement("tr");
    const actions = document.createElement("td");
    actions.className = "row-actions";
    actions.append(
      button("Изменить", "icon-btn", () => openForm(e)),
      button("Удалить", "icon-btn danger", () => remove(e)),
    );
    tr.append(
      cell(formatDate(e.spent_at), "date"),
      cell(userName(e.user_id), "user"),
      cell(CATEGORIES[e.category] || e.category, "category"),
      cell(e.comment || "", "comment"),
      cell(money(toKopecks(e.amount)), "num"),
      actions,
    );
    return tr;
  });
  $("rows").replaceChildren(...rows);

  const empty = $("empty");
  if (state.expenses.length === 0) {
    empty.textContent = state.users.length === 0
      ? "Пока никого нет: члены семьи появятся после первого сообщения боту."
      : "За выбранный период трат нет.";
    empty.hidden = false;
  } else {
    empty.hidden = true;
  }

  // Итоги по показанным тратам.
  const byCategory = new Map();
  let total = 0;
  for (const e of state.expenses) {
    const k = toKopecks(e.amount);
    total += k;
    byCategory.set(e.category, (byCategory.get(e.category) || 0) + k);
  }
  $("total").textContent = money(total);
  $("count").textContent = countLabel(state.expenses.length);

  const chips = [...byCategory.entries()]
    .sort((a, b) => b[1] - a[1])
    .map(([cat, sum]) => {
      const li = document.createElement("li");
      const b = document.createElement("b");
      b.textContent = money(sum);
      li.append(`${CATEGORIES[cat] || cat}: `, b);
      return li;
    });
  $("by-category").replaceChildren(...chips);
}

function countLabel(n) {
  const mod10 = n % 10, mod100 = n % 100;
  const word = mod10 === 1 && mod100 !== 11 ? "трата"
    : mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14) ? "траты" : "трат";
  return `${n} ${word}`;
}

function showAlert(message) {
  const el = $("alert");
  el.textContent = message;
  el.hidden = !message;
}

// ---------- Загрузка ----------

function currentFilter() {
  const params = new URLSearchParams();
  for (const [key, id] of [["user_id", "f-user"], ["category", "f-category"], ["from", "f-from"], ["to", "f-to"]]) {
    const v = $(id).value;
    if (v) params.set(key, v);
  }
  return params;
}

async function load() {
  try {
    state.expenses = await api(`/expenses?${currentFilter()}`);
    showAlert("");
  } catch (err) {
    state.expenses = [];
    showAlert(`Не удалось загрузить траты: ${err.message}`);
  }
  render();
}

async function loadUsers() {
  try {
    state.users = await api("/users");
  } catch (err) {
    showAlert(`Не удалось загрузить членов семьи: ${err.message}`);
  }
  const options = state.users.map((u) => [String(u.id), userName(u.id)]);
  const selected = $("f-user").value; // при обновлении списка не сбрасываем выбранный фильтр
  fillSelect($("f-user"), options, { keepFirst: true });
  $("f-user").value = selected;
  fillSelect($("e-user"), options);
  $("add-btn").disabled = state.users.length === 0;
}

const REFRESH_MS = 30_000;

async function refresh() {
  if (document.hidden || $("dialog").open) return; // не мешаем, пока редактируют трату
  await loadUsers();
  await load();
}

function applyPreset(name) {
  const [from, to] = presetRange(name);
  $("f-from").value = from;
  $("f-to").value = to;
  markPreset(name);
  load();
}

function markPreset(name) {
  for (const chip of document.querySelectorAll("[data-preset]")) {
    chip.setAttribute("aria-pressed", String(chip.dataset.preset === name));
  }
}

// ---------- Форма ----------

function openForm(expense = null) {
  state.editing = expense;
  $("form-title").textContent = expense ? "Изменить трату" : "Новая трата";
  $("e-user").value = String(expense ? expense.user_id : $("f-user").value || state.users[0]?.id || "");
  $("e-user").disabled = Boolean(expense); // владельца траты не меняем
  $("e-amount").value = expense ? expense.amount : "";
  $("e-date").value = expense ? expense.spent_at : iso(new Date());
  $("e-category").value = expense ? expense.category : "food";
  $("e-comment").value = expense?.comment || "";
  $("form-error").hidden = true;
  $("dialog").showModal();
  $("e-amount").focus();
}

async function save(event) {
  event.preventDefault();
  const body = {
    amount: $("e-amount").value,
    category: $("e-category").value,
    spent_at: $("e-date").value,
    comment: $("e-comment").value.trim(), // пустая строка удаляет комментарий
  };

  const saveBtn = $("save-btn");
  saveBtn.disabled = true;
  try {
    if (state.editing) {
      await api(`/expenses/${state.editing.id}`, { method: "PATCH", body: JSON.stringify(body) });
    } else {
      body.user_id = Number($("e-user").value);
      await api("/expenses", { method: "POST", body: JSON.stringify(body) });
    }
    $("dialog").close();
    await load();
  } catch (err) {
    const error = $("form-error");
    error.textContent = err.message;
    error.hidden = false;
  } finally {
    saveBtn.disabled = false;
  }
}

async function remove(expense) {
  const what = expense.comment ? `«${expense.comment}» ` : "";
  if (!confirm(`Удалить трату ${what}на ${money(toKopecks(expense.amount))}?`)) return;
  try {
    await api(`/expenses/${expense.id}`, { method: "DELETE" });
    await load();
  } catch (err) {
    showAlert(`Не удалось удалить: ${err.message}`);
  }
}

// ---------- Старт ----------

function init() {
  const categories = Object.entries(CATEGORIES);
  fillSelect($("f-category"), categories, { keepFirst: true });
  fillSelect($("e-category"), categories);

  for (const id of ["f-user", "f-category"]) $(id).addEventListener("change", load);
  for (const id of ["f-from", "f-to"]) {
    $(id).addEventListener("change", () => { markPreset(null); load(); });
  }
  for (const chip of document.querySelectorAll("[data-preset]")) {
    chip.addEventListener("click", () => applyPreset(chip.dataset.preset));
  }

  $("add-btn").addEventListener("click", () => openForm());
  $("cancel-btn").addEventListener("click", () => $("dialog").close());
  $("form").addEventListener("submit", save);

  loadUsers().then(() => applyPreset("month"));

  setInterval(refresh, REFRESH_MS);
  document.addEventListener("visibilitychange", refresh);
}

init();
