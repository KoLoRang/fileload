// FileLoad product page: EN/中文 toggle + download links from the latest release.
(() => {
  const REPO = "KoLoRang/fileload";
  const RELEASES = `https://github.com/${REPO}/releases/latest`;

  // English lives in the HTML; this is the Chinese translation (trusted markup).
  const zh = {
    "nav.claude": "Claude Code",
    "nav.features": "功能",
    "nav.faq": "常见问题",
    "hero.eyebrow": "免费开源 · Windows · macOS",
    "hero.title": '服务器上的 Claude Code<br><span class="grad">随时随地，随手就用</span>',
    "hero.lead": "在本机终端输入 <code>claude</code>，它就在服务器的项目目录里运行；同一个目录挂载在本地，Agent 改的每个文件都能用你自己的编辑器实时查看、审阅。",
    "cta.download": "下载",
    "cta.star": "★ 在 GitHub 上 Star",
    "cta.other": "其他平台：",
    "demo.modified": "刚刚修改",
    "claude.title": "Agent 跑在代码旁边，你在哪都行。",
    "claude.c1.t": "本地输入 <code>claude</code>",
    "claude.c1.p": "内置 SSH 代理自动登录（不输密码、不配密钥），直接在挂载的项目目录里启动 Claude Code；子目录对应远程子目录。",
    "claude.c2.t": "实时查看每处改动",
    "claude.c2.p": "目录挂载为本地盘符或文件夹，用 VS Code、Finder、资源管理器打开即可审阅 Agent 的修改；每次打开都会与服务器校验。",
    "claude.c3.t": "合上电脑也不怕",
    "claude.c3.p": "代码、编译和 Agent 都在服务器上。把命令设为 <code>tmux new -As claude claude</code>，断网后会话依然保留。",
    "claude.more": "codex、aider、gemini 等任意命令行工具同理——各加一条自定义命令即可。",
    "shots.cmd": "命令挂载：<code>rcd</code>、<code>claude</code>、<code>codex</code>，新开终端即可使用",
    "shots.mount": "目录挂载：连接服务器、选择远程目录、挂载",
    "features.title": "该有的都有，服务器端什么都不用装",
    "f1.t": "纯 SSH/SFTP",
    "f1.p": "服务器无需安装任何东西。Windows 使用 WinFsp，macOS 使用 FUSE-T（无需内核扩展）。",
    "f2.t": "本地缓存，秒开",
    "f2.p": "文件只下载一次，之后直接读本地；每次打开都会与服务器重新校验。",
    "f3.t": "写入自动回传",
    "f3.p": "保存先落本地、后台上传——即使崩溃或断电，下次也会继续上传。",
    "f4.t": "命令免密",
    "f4.p": "本地命令经回环代理复用 FileLoad 已认证的连接。",
    "f5.t": "稳定可靠",
    "f5.p": "断线自动重连、keepalive、挂载记录一键恢复、托盘/菜单栏常驻。",
    "f6.t": "自动更新",
    "f6.p": "新版本一键下载、安装并重启。",
    "steps.title": "一分钟上手",
    "steps.1": "<b>安装</b>——Windows 安装包（含 WinFsp）或 macOS dmg。",
    "steps.2": "<b>连接并挂载</b>——填写主机、用户、私钥或密码，选择远程目录。",
    "steps.3": "<b>添加 <code>claude</code></b>——在「命令挂载」页添加，然后新开终端输入即可。",
    "faq.title": "常见问题",
    "q1": "服务器上需要安装什么吗？",
    "a1": "只需要开启 SFTP 的 SSH 服务（OpenSSH 默认即开启），以及你想运行的命令行工具，例如 Claude Code。",
    "q2": "本地代理会暴露到局域网吗？",
    "a2": "不会。它只监听 127.0.0.1，并且只在挂载期间运行。",
    "q3": "macOS 提示无法打开应用？",
    "a3": "应用暂未经过公证。在「应用程序」里右键 FileLoad › 打开，只需一次。",
    "q4": "界面是什么语言？",
    "a4": "目前界面为简体中文，英文界面在计划中。",
    "q5": "免费吗？",
    "a5": "免费。FileLoad 采用 MIT 许可证；macOS 版内置的 FUSE-T 对非商业用途免费。",
    "footer.releases": "版本发布",
    "footer.note": "FileLoad 是独立的开源项目，与 Anthropic 无关。",
  };

  const nodes = [...document.querySelectorAll("[data-i18n],[data-i18n-html]")];
  const key = (el) => el.dataset.i18n || el.dataset.i18nHtml;
  const en = Object.fromEntries(nodes.map((el) => [key(el), el.innerHTML]));
  const titles = { en: document.title, zh: "FileLoad — 随时随地使用服务器上的 Claude Code" };
  let lang;

  function setLang(l) {
    lang = l;
    const dict = l === "zh" ? zh : en;
    for (const el of nodes) if (dict[key(el)] != null) el.innerHTML = dict[key(el)];
    document.documentElement.lang = l === "zh" ? "zh-CN" : "en";
    document.title = titles[l];
    document.getElementById("langBtn").textContent = l === "zh" ? "English" : "中文";
    try { localStorage.setItem("lang", l); } catch {}
    renderDownload();
  }

  // ---- downloads ----
  const ua = navigator.userAgent;
  const os = /Mac/i.test(ua) ? "mac" : /Win/i.test(ua) ? "win" : "";
  const links = { win: RELEASES, mac: RELEASES };
  let version = "";

  function renderDownload() {
    const label = { mac: "macOS", win: "Windows" }[os] || "";
    document.getElementById("dlPrimaryOs").textContent = label ? (lang === "zh" ? `（${label}）` : `for ${label}`) : "";
    document.getElementById("dlPrimary").href = os ? links[os] : RELEASES;
    document.getElementById("dlWin").href = links.win;
    document.getElementById("dlMac").href = links.mac;
    document.getElementById("dlVersion").textContent = version ? ` · ${version}` : "";
  }

  fetch(`https://api.github.com/repos/${REPO}/releases/latest`, { headers: { Accept: "application/vnd.github+json" } })
    .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
    .then((rel) => {
      version = rel.tag_name || "";
      for (const a of rel.assets || []) {
        const n = a.name.toLowerCase();
        if (n.endsWith(".dmg")) links.mac = a.browser_download_url;
        else if (n.endsWith(".exe") && n.includes("setup")) links.win = a.browser_download_url;
      }
      renderDownload();
    })
    .catch(() => {}); // no release yet or rate-limited: keep the releases page links

  document.getElementById("langBtn").addEventListener("click", () => setLang(lang === "zh" ? "en" : "zh"));
  const param = new URLSearchParams(location.search).get("lang"); // ?lang=en / ?lang=zh for shared links
  let saved = null;
  try { saved = localStorage.getItem("lang"); } catch {}
  const pick = (v) => (v === "zh" || v === "en" ? v : null);
  setLang(pick(param) || pick(saved) || (/^zh/i.test(navigator.language) ? "zh" : "en"));
})();
