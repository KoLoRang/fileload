// FileLoad UI. Talks to the Go service desktop.App (internal/ui/desktop)
// through the Wails runtime: Call.ByName for methods, Events for pushed state.
import { Call, Events, Clipboard } from "/wails/runtime.js";

const api = (method, ...args) => Call.ByName(`fileload/internal/ui/desktop.App.${method}`, ...args);
const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => (
  { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const errText = (e) => (e && (e.message || e.toString())) || "未知错误";

const ui = {
  snap: null,
  view: "mount",
  step: 1,
  path: "/",
  root: "",          // remote dir chosen in step 2
  activity: [],
  editCmd: "",      // custom command being edited
};

// ---------------- helpers ----------------

function setMsg(el, text, kind) {
  el.textContent = text || "";
  el.classList.toggle("err", kind === "err");
  el.classList.toggle("ok", kind === "ok");
}

let toastTimer;
function toast(text, kind) {
  const t = $("toast");
  t.textContent = text;
  t.classList.toggle("err", kind === "err");
  t.classList.remove("hidden");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.add("hidden"), 3200);
}

async function busy(btn, fn) {
  btn.disabled = true;
  btn.classList.add("busy");
  try {
    return await fn();
  } finally {
    btn.classList.remove("busy");
    btn.disabled = false;
    if (ui.snap) render(ui.snap); // re-apply state-driven disabled flags
  }
}

const isMounted = () => ui.snap && ui.snap.mount.phase !== "idle";
const isMac = () => ui.snap?.platform === "darwin";
const isDrive = () => ui.snap?.targetKind !== "dir";
// shortTarget shows "Z:" or the last folder name of a mount directory
const shortTarget = (t) => isDrive() ? t : (String(t || "").split("/").filter(Boolean).pop() || t);
const prettyPath = (t) => String(t || "").replace(/^\/Users\/[^/]+/, "~");

// ---------------- navigation ----------------

function showView(view) {
  ui.view = view;
  document.querySelectorAll(".nav-item").forEach((b) =>
    b.classList.toggle("active", b.dataset.view === view));
  $("view-mount").classList.toggle("hidden", view !== "mount");
  $("view-rcc").classList.toggle("hidden", view !== "rcc");
  $("view-about").classList.toggle("hidden", view !== "about");
  document.querySelector(".content").scrollTop = 0;
}

function showStep(n) {
  ui.step = n;
  for (const i of [1, 2, 3]) $("step" + i).classList.toggle("hidden", i !== n);
  document.querySelectorAll("#stepper li").forEach((li) => {
    const s = Number(li.dataset.step);
    li.classList.toggle("active", s === n);
    li.classList.toggle("done", s < n);
  });
  if (n === 2) browse(ui.path);
  if (n === 3) prepareStep3();
}

// ---------------- render (state pushed from Go) ----------------

function render(s) {
  ui.snap = s;
  const m = s.mount;
  const mounted = m.phase !== "idle";

  // sidebar status
  const ns = $("navStatus");
  ns.classList.toggle("on", m.phase === "mounted");
  ns.classList.toggle("busy", m.phase === "mounting" || m.phase === "unmounting");
  $("navStatusTitle").textContent = {
    idle: "未挂载", mounting: "正在挂载 " + shortTarget(m.drive), mounted: "已挂载 " + shortTarget(m.drive), unmounting: "正在卸载",
  }[m.phase] || "未挂载";
  $("navStatusSub").textContent = {
    off: "命令未启用", starting: "命令启动中", ready: "命令已就绪", error: "命令启动失败",
  }[s.rcc.phase] || "";

  // current mount card
  $("currentCard").classList.toggle("hidden", !mounted);
  if (mounted) {
    $("curDrive").textContent = shortTarget(m.drive);
    $("curDrive").title = m.drive;
    $("curDrive").classList.toggle("dir", !isDrive());
    $("curRoot").textContent = m.root;
    $("curRoot").title = m.root;
    $("curServer").textContent = isDrive() ? m.server : `${m.server} · ${prettyPath(m.drive)}`;
    $("curCache").textContent = "本地缓存：" + m.cacheDir;
    $("curCache").title = m.cacheDir;
    const chip = $("curChip");
    chip.className = "chip " + ({ mounted: "ok", mounting: "busy", unmounting: "busy" }[m.phase] || "");
    chip.textContent = { mounting: "挂载中", mounted: "已挂载", unmounting: "卸载中" }[m.phase];
    $("openDriveBtn").disabled = m.phase !== "mounted";
    $("unmountBtn").disabled = m.phase !== "mounted";
  }
  $("depsBar").classList.toggle("hidden", !s.depsError);
  $("depsMessage").textContent = s.depsInstallable
    ? "需要先安装文件系统驱动 FUSE-T 才能挂载目录。FileLoad 已内置安装包，点击右侧按钮并输入电脑密码即可完成安装。"
    : (s.depsError || "");
  $("installDepsBtn").classList.toggle("hidden", !s.depsInstallable);

  const mm = $("mountMessage");
  mm.textContent = m.message || "";
  mm.classList.toggle("hidden", !m.message || m.phase === "mounting");
  mm.classList.toggle("err", /失败/.test(m.message || ""));

  renderHistory(s, mounted);
  renderServers(s);

  $("connectedAs").textContent = s.connected ? "已连接 " + s.server : "";
  $("toStep2Btn").disabled = !s.connected;
  $("mountBtn").disabled = mounted;
  if (ui.step === 3) {
    setMsg($("mountMsg"), mounted ? "当前已有挂载，卸载后才能新建挂载" : "", "");
  }

  renderRcc(s);
}

function renderHistory(s, mounted) {
  const box = $("historyList");
  if (!s.mounts || s.mounts.length === 0) {
    box.innerHTML = `<div class="empty">暂无挂载记录，完成一次挂载后会出现在这里。</div>`;
    return;
  }
  box.innerHTML = s.mounts.map((r) => `
    <div class="list-row">
      <div class="badge${isDrive() ? "" : " dir"}" title="${esc(r.drive)}">${esc(shortTarget(r.drive))}</div>
      <div class="row-main">
        <div class="row-title" title="${esc(r.root)}"><span class="mono">${esc(r.root)}</span></div>
        <div class="row-sub">${esc(r.user)}@${esc(r.host)}:${r.port}${isDrive() ? "" : " · " + esc(prettyPath(r.drive))}</div>
      </div>
      <button class="btn primary" data-remount="${r.index}" ${mounted ? "disabled" : ""}>挂载</button>
      <button class="btn subtle" data-delmount="${r.index}" title="删除记录">删除</button>
    </div>`).join("");
}

function renderServers(s) {
  const sel = $("savedSel");
  const keep = sel.value;
  sel.innerHTML = `<option value="">（不使用已保存配置）</option>` +
    (s.servers || []).map((p) => `<option value="${p.index}">${esc(p.name)} (${esc(p.host)})</option>`).join("");
  if ([...sel.options].some((o) => o.value === keep)) sel.value = keep;
}

const ICONS = {
  off: `<svg viewBox="0 0 24 24"><rect x="3.5" y="4.5" width="17" height="15" rx="2.5"/><path d="m7.5 9.5 3 2.5-3 2.5M12.5 15h4"/></svg>`,
  ok: `<svg viewBox="0 0 24 24"><path d="m6 12.5 4 4 8-9"/></svg>`,
  busy: `<svg viewBox="0 0 24 24"><path d="M12 7v5l3 2"/><circle cx="12" cy="12" r="8"/></svg>`,
  err: `<svg viewBox="0 0 24 24"><path d="M12 8v5M12 16h.01"/><circle cx="12" cy="12" r="8"/></svg>`,
};

function renderRcc(s) {
  const r = s.rcc;
  const mounted = s.mount.phase === "mounted";
  const kind = { off: "off", starting: "busy", ready: "ok", error: "err" }[r.phase] || "off";
  const icon = $("rccIcon");
  icon.className = "status-icon " + (kind === "off" ? "" : kind);
  icon.innerHTML = ICONS[kind];

  const texts = {
    off: ["未启用", "命令随目录挂载自动启用，卸载或退出时自动移除。"],
    busy: ["正在启动", "正在启动本地代理并注入命令..."],
    ok: ["已就绪", "新开一个终端输入 rcd 或自定义命令即可。"],
    err: ["启动失败", r.error || ""],
  }[kind];
  $("rccTitle").textContent = texts[0];
  $("rccDesc").textContent = texts[1];
  $("gotoMountBtn").classList.toggle("hidden", s.mount.phase !== "idle");

  $("rccDetailCard").classList.toggle("hidden", !mounted);
  if (mounted) {
    $("rccDrive").textContent = isDrive() ? r.drive + "\\" : r.drive;
    $("rccRoot").textContent = r.root;
    $("rccProxy").textContent = r.proxyAddr || "—";
    $("rccBin").textContent = r.binDir;
  }
  const where = isDrive() ? r.drive + "\\" : prettyPath(r.drive);
  $("rccHint").textContent = mounted
    ? `新开一个终端窗口后生效；默认进入 ${r.root}，在 ${where} 的子目录下执行时进入对应的远程子目录。`
    : "挂载后新开一个终端窗口即可使用；默认进入挂载目录，在挂载位置的子目录下执行时进入对应的远程子目录。";

  renderCommands(s.commands || []);
}

function renderCommands(cmds) {
  $("cmdList").innerHTML = cmds.map((c) => {
    const sub = c.builtin
      ? (c.command ? `等同于在挂载目录下运行 ${c.command}` : "登录后停在挂载目录，可继续 cd 到子目录")
      : `在挂载目录下运行：${c.command}`;
    const ops = c.builtin ? "" : `
      <button class="btn subtle" data-editcmd="${esc(c.name)}">编辑</button>
      <button class="btn subtle" data-delcmd="${esc(c.name)}">删除</button>`;
    return `
    <div class="cmd-row">
      <code class="cmd" title="${esc(c.name)}">${esc(c.name)}</code>
      <div class="cmd-desc"><div>${esc(c.desc || (c.builtin ? "" : "自定义命令"))}</div><div class="muted small mono-sub" title="${esc(sub)}">${esc(sub)}</div></div>
      ${ops}
      <button class="btn subtle" data-copy="${esc(c.name)}">复制</button>
    </div>`;
  }).join("");
}

// ---------------- custom commands ----------------

function editCommand(name) {
  const c = (ui.snap?.commands || []).find((x) => x.name === name);
  ui.editCmd = c ? c.name : "";
  $("cmdName").value = c ? c.name : "";
  $("cmdLine").value = c ? c.command : "";
  $("cmdDesc").value = c ? c.desc : "";
  $("cmdFormTitle").textContent = c ? `编辑自定义命令 ${c.name}` : "添加自定义命令";
  $("cmdSaveBtn").textContent = c ? "保存" : "添加";
  $("cmdCancelBtn").classList.toggle("hidden", !c);
  setMsg($("cmdMsg"), "");
  if (c) $("cmdName").focus();
}

async function saveCommand() {
  const name = $("cmdName").value.trim();
  await busy($("cmdSaveBtn"), async () => {
    try {
      await api("SaveCommand", ui.editCmd || "", name, $("cmdLine").value, $("cmdDesc").value);
      editCommand("");
      const live = ui.snap?.rcc.phase === "ready";
      toast(`已保存命令 ${name}${live ? "，新开终端即可使用" : "，挂载后可用"}`);
    } catch (e) {
      setMsg($("cmdMsg"), errText(e), "err");
    }
  });
}

// ---------------- step 1: connect ----------------

function readForm() {
  return {
    host: $("host").value.trim(),
    port: parseInt($("port").value, 10) || 22,
    user: $("user").value.trim(),
    password: $("password").value,
    keyPath: $("keyPath").value.trim(),
  };
}

function fillForm(p) {
  $("host").value = p.host || "";
  $("port").value = p.port || 22;
  $("user").value = p.user || "";
  $("password").value = p.password || "";
  $("keyPath").value = p.keyPath || "";
}

async function connect() {
  const p = readForm();
  if (!p.host || !p.user) return setMsg($("connMsg"), "请填写主机和用户名", "err");
  if (!p.password && !p.keyPath) return setMsg($("connMsg"), "请填写密码或私钥路径", "err");
  setMsg($("connMsg"), `正在连接 ${p.user}@${p.host}:${p.port} ...`);
  await busy($("connectBtn"), async () => {
    try {
      await api("Connect", p);
      setMsg($("connMsg"), "已连接，配置已自动保存", "ok");
      ui.path = "/";
      showStep(2);
    } catch (e) {
      setMsg($("connMsg"), errText(e), "err");
    }
  });
}

// ---------------- step 2: browse ----------------

const FOLDER = `<svg viewBox="0 0 24 24"><path d="M4 7.5A2.5 2.5 0 0 1 6.5 5H10l2 2h5.5A2.5 2.5 0 0 1 20 9.5v7a2.5 2.5 0 0 1-2.5 2.5h-11A2.5 2.5 0 0 1 4 16.5v-9Z"/></svg>`;
const FILE = `<svg viewBox="0 0 24 24"><path d="M7 4h7l4 4v11a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1Z"/><path d="M14 4v4h4"/></svg>`;
let browseSeq = 0;

async function browse(path) {
  const seq = ++browseSeq;
  $("pathInput").value = path;
  setMsg($("browseMsg"), "正在读取...");
  try {
    const res = await api("ListDir", path);
    if (seq !== browseSeq) return; // user moved on
    ui.path = res.path;
    $("pathInput").value = res.path;
    setMsg($("browseMsg"), "");
    const list = $("fileList");
    if (!res.entries.length) {
      list.innerHTML = `<div class="empty">此目录为空</div>`;
      return;
    }
    list.innerHTML = res.entries.map((e) => e.isDir
      ? `<div class="file dir" data-enter="${esc(e.path)}">${FOLDER}<span class="name">${esc(e.name)}</span>
           <button class="btn pick" data-pick="${esc(e.path)}">选此目录</button></div>`
      : `<div class="file">${FILE}<span class="name">${esc(e.name)}</span></div>`).join("");
    list.scrollTop = 0;
  } catch (e) {
    if (seq !== browseSeq) return;
    setMsg($("browseMsg"), errText(e), "err");
  }
}

function parentOf(p) {
  const cur = p.replace(/\/+$/, "");
  const i = cur.lastIndexOf("/");
  return i > 0 ? cur.slice(0, i) : "/";
}

function pickRoot(path) {
  ui.root = path;
  showStep(3);
}

// ---------------- step 3: mount ----------------

async function prepareStep3() {
  $("sumServer").textContent = ui.snap?.server || "";
  $("sumRoot").textContent = ui.root;
  setMsg($("mountMsg"), isMounted() ? "当前已有挂载，卸载后才能新建挂载" : "");
  const drive = isDrive();
  $("targetLabel").textContent = drive ? "本地盘符" : "挂载目录";
  $("driveSel").classList.toggle("hidden", !drive);
  $("dirInput").classList.toggle("hidden", drive);
  const targets = await api("Targets", ui.root);
  if (!drive) {
    $("dirInput").value = targets[0]?.label || "";
    return;
  }
  const sel = $("driveSel");
  const keep = sel.value || "Y:";
  sel.innerHTML = targets.map((d) =>
    `<option value="${d.value}" ${d.used ? "disabled" : ""}>${d.label}${d.used ? "（已占用）" : ""}</option>`).join("");
  const free = targets.filter((d) => !d.used).map((d) => d.value);
  sel.value = free.includes(keep) ? keep : (free[0] || "");
}

const chosenTarget = () => (isDrive() ? $("driveSel").value : $("dirInput").value.trim());

async function mount() {
  await busy($("mountBtn"), async () => {
    try {
      await api("Mount", chosenTarget(), ui.root);
      setMsg($("mountMsg"), "");
      showStep(1);
      document.querySelector(".content").scrollTop = 0;
    } catch (e) {
      setMsg($("mountMsg"), errText(e), "err");
    }
  });
}

// ---------------- about / update ----------------

async function loadAbout() {
  ui.about = await api("About");
  $("aboutVersion").textContent = "v" + ui.about.version;
  $("releasesUrl").textContent = ui.about.releasesUrl;
}

function renderUpdate(u) {
  $("aboutBadge").classList.toggle("hidden", !u?.hasUpdate);
  if (!u?.hasUpdate) return $("updateCard").classList.add("hidden");
  $("updateTitle").textContent = `发现新版本 v${u.latest}${u.name && u.name !== "v" + u.latest ? " · " + u.name : ""}`;
  $("updateDate").textContent = u.releasedAt ? "发布于 " + u.releasedAt : "";
  $("updateNotes").textContent = u.notes || "";
  $("updateNotes").classList.toggle("hidden", !u.notes);
  $("updateInstallBtn").classList.toggle("hidden", !u.assetUrl);
  $("updateCard").classList.remove("hidden");
}

function showUpdateMsg(text, err) {
  const msg = $("updateMsg");
  msg.textContent = text;
  msg.classList.toggle("err", !!err);
  msg.classList.toggle("hidden", !text);
}

// silent: automatic check at startup — only a found update is shown
async function checkUpdate(silent) {
  if (ui.updating) return;
  if (!silent) showUpdateMsg("");
  const run = async () => {
    try {
      const u = await api("CheckUpdate");
      ui.update = u;
      renderUpdate(u);
      if (u.hasUpdate) {
        if (silent) toast(`发现新版本 v${u.latest}，可在「关于」页一键更新`);
        else if (!u.assetUrl) showUpdateMsg("该版本没有适用于本系统的安装包，请打开发布页手动下载", true);
      } else if (!silent) {
        showUpdateMsg(`已是最新版本（v${u.current}）`);
      }
    } catch (e) {
      if (!silent) showUpdateMsg(errText(e), true);
    }
  };
  if (silent) return run();
  await busy($("checkUpdateBtn"), run);
}

async function installUpdate() {
  const u = ui.update;
  if (!u?.hasUpdate) return;
  ui.updating = true;
  showUpdateMsg("");
  $("updateProgress").classList.remove("hidden");
  setUpdateProgress(0, u.installerSize);
  for (const id of ["updateInstallBtn", "checkUpdateBtn"]) $(id).disabled = true;
  try {
    await api("InstallUpdate");
    showUpdateMsg("安装程序已启动，FileLoad 即将退出并自动更新…");
  } catch (e) {
    showUpdateMsg(errText(e), true);
    $("updateProgress").classList.add("hidden");
    for (const id of ["updateInstallBtn", "checkUpdateBtn"]) $(id).disabled = false;
    ui.updating = false;
  }
}

function setUpdateProgress(done, total) {
  const pct = total > 0 ? Math.min(100, Math.floor((done * 100) / total)) : 0;
  $("updateBar").style.width = pct + "%";
  const mb = (n) => (n / 1048576).toFixed(1);
  $("updatePct").textContent = total > 0 ? `${mb(done)} / ${mb(total)} MB` : `${mb(done)} MB`;
}

const openURL = (u) => api("OpenURL", u).catch((e) => toast(errText(e), "err"));

// ---------------- activity log ----------------

function addActivity(msgs) {
  const t = new Date().toTimeString().slice(0, 8);
  for (const msg of [].concat(msgs)) ui.activity.unshift({ t, msg });
  ui.activity.length = Math.min(ui.activity.length, 200);
  $("activityCount").textContent = `（${ui.activity.length}）`;
  $("activityList").innerHTML = ui.activity
    .map((a) => `<li><time>${a.t}</time><span>${esc(a.msg)}</span></li>`).join("");
}

// ---------------- platform wording ----------------

function applyPlatformTexts() {
  if (!isMac()) return;
  $("revealLabel").textContent = "在 Finder 中打开";
  $("closeHint").textContent = "菜单栏图标菜单里随时可以「退出」";
  $("closeTrayBtn").textContent = "隐藏到菜单栏";
  $("closeText").textContent = "隐藏到菜单栏后程序继续运行，挂载和挂载命令保持可用；退出会先上传未同步的文件，再卸载。";
  $("closeSel").querySelector('option[value="tray"]').textContent = "隐藏到菜单栏（挂载保持）";
}

// ---------------- wiring ----------------

function wire() {
  document.addEventListener("click", async (ev) => {
    const t = ev.target.closest("[data-view],[data-nav],[data-goto],[data-remount],[data-delmount],[data-enter],[data-pick],[data-copy],[data-editcmd],[data-delcmd]");
    if (!t) return;
    if (t.dataset.view) return showView(t.dataset.view);
    if (t.dataset.nav) { ev.preventDefault(); return showView(t.dataset.nav); }
    if (t.dataset.goto) return showStep(Number(t.dataset.goto));
    if (t.dataset.pick) { ev.stopPropagation(); return pickRoot(t.dataset.pick); }
    if (t.dataset.enter) return browse(t.dataset.enter);
    if (t.dataset.copy) {
      await Clipboard.SetText(t.dataset.copy);
      return toast(`已复制「${t.dataset.copy}」，在新终端里粘贴运行`);
    }
    if (t.dataset.editcmd) return editCommand(t.dataset.editcmd);
    if (t.dataset.delcmd) {
      const name = t.dataset.delcmd;
      try {
        await api("DeleteCommand", name);
        if (ui.editCmd === name) editCommand("");
        toast("已删除命令 " + name);
      } catch (e) { toast(errText(e), "err"); }
      return;
    }
    if (t.dataset.remount) {
      return busy(t, async () => {
        try { await api("Remount", Number(t.dataset.remount)); }
        catch (e) { toast(errText(e), "err"); }
      });
    }
    if (t.dataset.delmount) {
      try { await api("DeleteMount", Number(t.dataset.delmount)); toast("已删除挂载记录"); }
      catch (e) { toast(errText(e), "err"); }
    }
  });

  $("savedSel").addEventListener("change", async () => {
    const v = $("savedSel").value;
    if (v === "") return;
    try { fillForm(await api("GetServer", Number(v))); setMsg($("connMsg"), ""); }
    catch (e) { setMsg($("connMsg"), errText(e), "err"); }
  });
  $("delServerBtn").addEventListener("click", async () => {
    const v = $("savedSel").value;
    if (v === "") return setMsg($("connMsg"), "请先在下拉框选择一个已保存的配置", "err");
    const name = $("savedSel").selectedOptions[0].textContent;
    try { await api("DeleteServer", Number(v)); $("savedSel").value = ""; toast("已删除配置 " + name); }
    catch (e) { setMsg($("connMsg"), errText(e), "err"); }
  });
  $("connectBtn").addEventListener("click", connect);
  for (const id of ["host", "port", "user", "password", "keyPath"]) {
    $(id).addEventListener("keydown", (e) => { if (e.key === "Enter") connect(); });
  }
  $("toStep2Btn").addEventListener("click", () => showStep(2));

  $("upBtn").addEventListener("click", () => browse(parentOf(ui.path)));
  $("refreshBtn").addEventListener("click", () => browse(ui.path));
  $("pathInput").addEventListener("keydown", (e) => {
    if (e.key === "Enter") browse($("pathInput").value.trim() || "/");
  });
  $("pickCurrentBtn").addEventListener("click", () => pickRoot(ui.path));

  $("mountBtn").addEventListener("click", mount);
  $("installDepsBtn").addEventListener("click", (ev) => busy(ev.currentTarget, async () => {
    try {
      await api("InstallDeps");
      toast("FUSE-T 驱动已安装，现在可以挂载目录了");
    } catch (e) {
      const msg = errText(e);
      toast(msg, /取消/.test(msg) ? "" : "err");
    }
  }));
  $("dirInput").addEventListener("keydown", (e) => { if (e.key === "Enter") mount(); });
  $("openDriveBtn").addEventListener("click", () => api("OpenDrive").catch((e) => toast(errText(e), "err")));
  $("unmountBtn").addEventListener("click", (ev) => busy(ev.currentTarget, async () => {
    try { await api("Unmount"); } catch (e) { toast(errText(e), "err"); }
  }));

  $("cmdSaveBtn").addEventListener("click", saveCommand);
  $("cmdCancelBtn").addEventListener("click", () => editCommand(""));
  for (const id of ["cmdName", "cmdLine", "cmdDesc"]) {
    $(id).addEventListener("keydown", (e) => { if (e.key === "Enter") saveCommand(); });
  }

  $("checkUpdateBtn").addEventListener("click", () => checkUpdate(false));
  $("updatePageBtn").addEventListener("click", () => openURL(ui.update?.pageUrl || ui.about.releasesUrl));
  $("updateInstallBtn").addEventListener("click", installUpdate);
  $("openReleasesBtn").addEventListener("click", () => openURL(ui.about.releasesUrl));

  $("closeSel").addEventListener("change", () => api("SetCloseAction", $("closeSel").value));
  const answerClose = (action) => {
    const remember = $("rememberClose").checked;
    $("closeModal").classList.add("hidden");
    if (remember) $("closeSel").value = action;
    api("CloseChoice", action, remember);
  };
  $("closeTrayBtn").addEventListener("click", () => answerClose("tray"));
  $("closeQuitBtn").addEventListener("click", () => answerClose("quit"));
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") $("closeModal").classList.add("hidden");
  });

  Events.On("state", (ev) => render(ev.data));
  Events.On("activity", (ev) => addActivity(ev.data));
  Events.On("update-progress", (ev) => setUpdateProgress(ev.data.done, ev.data.total));
  Events.On("close-requested", () => {
    $("rememberClose").checked = false;
    $("closeModal").classList.remove("hidden");
    $("closeTrayBtn").focus();
  });
}

async function init() {
  wire();
  showStep(1);
  showView("mount");

  const snap = await api("Snapshot");
  document.documentElement.classList.toggle("mica", !!snap.translucent);
  document.documentElement.classList.toggle("mac", snap.platform === "darwin");
  ui.snap = snap;
  applyPlatformTexts();
  render(snap);

  const last = await api("LastProfile");
  if (last) {
    fillForm(last);
    setMsg($("connMsg"), `已填入上次使用的服务器 ${last.user}@${last.host}，点击「连接」即可`);
  }
  $("closeSel").value = await api("GetCloseAction");
  await loadAbout();
  setTimeout(() => checkUpdate(true), 3000); // automatic check, silent unless an update exists
}

init().catch((e) => toast(errText(e), "err"));
