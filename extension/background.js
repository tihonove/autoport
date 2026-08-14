// autoport — фоновый скрипт расширения.
//
// Задача одна: убедиться, что MV3 действительно отдаёт нам упавшую main-frame
// навигацию и что из обработчика можно увести вкладку. Ничего полезного тут
// не происходит — вся логика будет на стороне сервера.

const SERVER = "http://127.0.0.1:8777";

// Пока хардкод, потом уедет в конфиг. Имена вроде local.tsunami.nebius.dev
// смаплены на 127.0.0.1 в /etc/hosts — для расширения это обычные хосты,
// про резолв оно ничего не знает, поэтому каждый такой домен приходится
// перечислять и здесь, и в host_permissions манифеста (иначе события по
// нему просто не придут).
const WATCH = [
  "http://localhost/*",
  "http://127.0.0.1/*",
  "https://local.tsunami.nebius.dev/*",
  "http://local.tsunami.nebius.dev/*",
];

const ERRORS = new Set(["net::ERR_CONNECTION_REFUSED"]);
const COOLDOWN = 10000;

// Живёт в памяти воркера и обнуляется, когда тот засыпает. Для спайка ок;
// в боевой версии — chrome.storage.session.
const recent = new Map();

function log(action, details, reason) {
  const mark = action === "перехват" ? "→" : "·";
  console.log(
    `${mark} ${action}`,
    reason ? `(${reason})` : "",
    "\n  url:", details.url,
    "\n  error:", details.error,
    "\n  tabId:", details.tabId,
  );
}

function handle(details) {
  if (details.tabId < 0) {
    log("пропуск", details, "запрос не из вкладки");
    return;
  }
  if (!ERRORS.has(details.error)) {
    log("пропуск", details, "код ошибки не наш");
    return;
  }
  // Без этой проверки, если сервер лежит, мы будем гонять вкладку по кругу.
  if (details.url.startsWith(SERVER)) {
    log("пропуск", details, "это наш собственный сервер");
    return;
  }

  const now = Date.now();
  const last = recent.get(details.url) || 0;
  if (now - last < COOLDOWN) {
    log("пропуск", details, `кулдаун, ${Math.round((COOLDOWN - (now - last)) / 1000)}с`);
    return;
  }
  recent.set(details.url, now);

  let port = "";
  let host = "";
  try {
    const u = new URL(details.url);
    // У https://local.tsunami.nebius.dev/ порт в URL не написан — берём
    // подразумеваемый по схеме, иначе на той стороне не понять, куда бинтить.
    port = u.port || (u.protocol === "https:" ? "443" : "80");
    host = u.hostname;
  } catch (e) {
    port = "";
  }

  const fix =
    `${SERVER}/fix?back=${encodeURIComponent(details.url)}` +
    `&error=${encodeURIComponent(details.error)}` +
    `&host=${encodeURIComponent(host)}` +
    `&port=${encodeURIComponent(port)}`;

  log("перехват", details);
  chrome.tabs.update(details.tabId, { url: fix }, () => {
    if (chrome.runtime.lastError) {
      console.warn("не смог увести вкладку:", chrome.runtime.lastError.message);
    }
  });
}

// Регистрация строго на верхнем уровне: воркер в MV3 засыпает и
// перезапускается, слушатель должен ставиться при каждом старте файла.
chrome.webRequest.onErrorOccurred.addListener(handle, {
  urls: WATCH,
  // main_frame — не оптимизация, а граница безопасности: иначе любая страница
  // дёргает нас через <img src="http://localhost:3000/">.
  types: ["main_frame"],
});

console.log("autoport поднялся. Слежу за:", WATCH.join(", "), "| сервер:", SERVER);
