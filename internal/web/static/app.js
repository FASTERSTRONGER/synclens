const state = {
  pairs: [],
  items: [],
  itemMap: {},
  treeFolders: [],
  treeFiles: [],
  actions: {},     // rel_path -> action string
  folderActions: {},
  checked: new Set(),
  checkedFolders: new Set(),
  filter: "DIFF",  // DIFF = all non-identical
  search: "",
  viewMode: "tree", // "tree" (File Manager) or "flat"
  currentDir: "",
  connected: false,
  browseSide: "local",
  browseTargetInput: "localPathInput",
  browseCurrentPath: "",
  localSpace: null,
  peerSpace: null,
};

const el = (id) => document.getElementById(id);

function formatETA(sec) {
  if (sec === undefined || sec === null || sec < 0) return "Menghitung...";
  if (sec < 60) return `~${sec}d`;
  if (sec < 3600) return `~${Math.floor(sec / 60)}m ${sec % 60}d`;
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  return `~${h}j ${m}m`;
}

function formatBytes(bytes) {
  if (!bytes) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = bytes;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

function formatTime(ns) {
  if (!ns) return "—";
  const d = new Date(ns / 1e6);
  return d.toLocaleString("id-ID", { dateStyle: "short", timeStyle: "medium" });
}

function statusLabel(s) {
  const map = {
    NEW_LOCAL: "Hanya di Lokal",
    NEW_PEER: "Hanya di Peer",
    NEWER_LOCAL: "Lokal Lebih Baru",
    NEWER_PEER: "Peer Lebih Baru",
    SIZE_DIFF: "Ukuran Beda",
    HASH_DIFF: "Isi Beda (Hash)",
    IDENTICAL: "Identik",
  };
  return map[s] || s;
}

async function parseResponseJSON(res) {
  const text = await res.text();
  if (!res.ok) {
    throw new Error(text || `HTTP ${res.status}`);
  }
  if (!text) {
    throw new Error(`Respons server kosong (HTTP ${res.status})`);
  }
  try {
    return JSON.parse(text);
  } catch (e) {
    throw new Error(`JSON tidak valid: ${text.slice(0, 120)}`);
  }
}

function applyConnectionState(data) {
  state.connected = true;
  el("peerDot").className = "dot online";
  el("peerStatusText").textContent = `Terhubung: ${data.peer.device_name} (${data.peer.ip})`;

  const warnings = [];
  if (data.local_processes?.["Antigravity.exe"]) warnings.push("Antigravity.exe sedang berjalan di PC ini.");
  if (data.peer_processes?.["Antigravity.exe"]) warnings.push(`Antigravity.exe sedang berjalan di ${data.peer.device_name}.`);
  const alertBox = el("processAlert");
  if (warnings.length > 0) {
    alertBox.textContent = "Peringatan: " + warnings.join(" ") + " Tutup aplikasi sebelum menyinkronkan .gemini.";
    alertBox.classList.remove("hidden");
  } else {
    alertBox.classList.add("hidden");
  }
}

async function loadPairs() {
  const res = await fetch("/api/local/pairs");
  const data = await res.json();
  el("localDeviceLabel").textContent = `Perangkat Ini: ${data.device_name}`;
  state.pairs = data.pairs || [];
  renderPairSelect();
  if (data.connected && data.peer) {
    applyConnectionState(data);
  }
}

function renderPairSelect() {
  const sel = el("pairSelect");
  sel.innerHTML = "";
  state.pairs.forEach((p, idx) => {
    const opt = document.createElement("option");
    opt.value = p.id;
    opt.textContent = p.name;
    if (idx === 0) opt.selected = true;
    sel.appendChild(opt);
  });
  fillPairInputs(state.pairs[0]);
}

function fillPairInputs(pair) {
  if (!pair) return;
  el("localPathInput").value = pair.local_path || "";
  el("remotePathInput").value = pair.remote_path || "";
  el("excludesInput").value = (pair.excludes || []).join(", ");
}

el("pairSelect").addEventListener("change", () => {
  const p = state.pairs.find((x) => x.id === el("pairSelect").value);
  fillPairInputs(p);
});

el("newPairBtn").addEventListener("click", () => {
  const sel = el("pairSelect");
  const id = `pair-${Date.now()}`;
  const newPair = {
    id,
    name: "Pasangan Baru",
    local_path: "D:\\",
    remote_path: "D:\\",
    excludes: ["*.tmp"],
  };
  state.pairs.push(newPair);
  renderPairSelect();
  sel.value = id;
  fillPairInputs(newPair);
});

el("savePairBtn").addEventListener("click", async () => {
  const curId = el("pairSelect").value;
  const existing = state.pairs.find((x) => x.id === curId) || { id: curId };
  const payload = {
    ...existing,
    name: existing.name === "Pasangan Baru" ? el("localPathInput").value : existing.name,
    local_path: el("localPathInput").value.trim(),
    remote_path: el("remotePathInput").value.trim(),
    excludes: el("excludesInput").value.split(",").map((s) => s.trim()).filter(Boolean),
  };
  const res = await fetch("/api/local/pairs", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  if (res.ok) {
    await loadPairs();
    el("pairSelect").value = payload.id;
    fillPairInputs(payload);
  }
});

el("connectBtn").addEventListener("click", async () => {
  const pin = el("pinInput").value.trim();
  const ip = el("ipInput").value.trim();
  if (!pin) {
    alert("Masukkan 6-digit Pairing PIN yang tampil di Peer.");
    return;
  }
  el("connectBtn").disabled = true;
  el("connectBtn").textContent = "Mencari Peer...";
  try {
    const res = await fetch("/api/local/connect", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ip, pin }),
    });
    if (!res.ok) {
      throw new Error(await res.text());
    }
    const data = await res.json();
    applyConnectionState(data);
  } catch (err) {
    alert("Koneksi gagal: " + err.message);
  } finally {
    el("connectBtn").disabled = false;
    el("connectBtn").textContent = "Hubungkan ke Peer";
  }
});

let activeAbort = null;

async function cancelActiveOp() {
  if (activeAbort) {
    activeAbort.abort();
  }
  try {
    await fetch("/api/local/cancel", { method: "POST" });
  } catch (_) {}
}

el("cancelScanBtn").addEventListener("click", cancelActiveOp);
el("cancelExecBtn").addEventListener("click", cancelActiveOp);

el("scanBtn").addEventListener("click", async () => {
  if (!state.connected) {
    alert("Hubungkan ke Peer terlebih dahulu.");
    return;
  }
  el("scanBtn").disabled = true;
  el("scanBtn").textContent = "Memindai...";
  el("scanSectionTitle").textContent = "Memindai File Secara Live...";
  el("cancelScanBtn").classList.remove("hidden");
  el("scanProgressSection").classList.remove("hidden");
  el("scanLocalStats").textContent = "0 file (0 B)";
  el("scanLocalCurrent").textContent = "Memulai pemindaian...";
  el("scanPeerStats").textContent = "0 file (0 B)";
  el("scanPeerCurrent").textContent = "Memulai pemindaian...";

  activeAbort = new AbortController();
  const evtSrc = new EventSource("/api/local/events");
  evtSrc.onmessage = (ev) => {
    const p = JSON.parse(ev.data);
    if (p.type !== "scan_progress") return;
    if (p.side === "local") {
      el("scanLocalStats").textContent = `${p.count.toLocaleString("id-ID")} file (${formatBytes(p.bytes)})`;
      el("scanLocalCurrent").textContent = p.current || "Selesai memindai.";
    } else if (p.side === "peer") {
      el("scanPeerStats").textContent = `${p.count.toLocaleString("id-ID")} file (${formatBytes(p.bytes)})`;
      el("scanPeerCurrent").textContent = p.current || "Selesai memindai.";
    }
  };

  try {
    const payload = {
      id: el("pairSelect").value,
      local_path: el("localPathInput").value.trim(),
      remote_path: el("remotePathInput").value.trim(),
      excludes: el("excludesInput").value.split(",").map((s) => s.trim()).filter(Boolean),
    };
    const res = await fetch("/api/local/compare", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      signal: activeAbort.signal,
    });
    const data = await parseResponseJSON(res);
    applyCompareResult(data);
    el("scanSectionTitle").textContent = "Pemindaian Selesai";
    el("scanLocalCurrent").textContent = "Selesai memindai.";
    el("scanPeerCurrent").textContent = "Selesai memindai.";
    el("resultsSection").classList.remove("hidden");
  } catch (err) {
    if (err.name === "AbortError") {
      el("scanSectionTitle").textContent = "Pemindaian Dibatalkan";
      el("scanLocalCurrent").textContent = "Pemindaian dibatalkan.";
      el("scanPeerCurrent").textContent = "Pemindaian dibatalkan.";
    } else {
      el("scanSectionTitle").textContent = "Pemindaian Gagal";
      alert("Gagal memindai: " + err.message);
    }
  } finally {
    activeAbort = null;
    evtSrc.close();
    el("cancelScanBtn").classList.add("hidden");
    el("scanBtn").disabled = false;
    el("scanBtn").textContent = "Scan & Bandingkan";
  }
});

function registerItems(list, autoCheck) {
  for (const it of (list || [])) {
    state.itemMap[it.rel_path] = it;
    if (!(it.rel_path in state.actions)) {
      state.actions[it.rel_path] = it.default_action;
    }
    if (autoCheck && it.default_action !== "skip") {
      state.checked.add(it.rel_path);
    }
  }
}

function applyActionToItem(it, val) {
  if (!it) return;
  if (val === "default") {
    state.actions[it.rel_path] = it.default_action;
  } else if (val === "push" && it.has_local) {
    state.actions[it.rel_path] = "push";
  } else if (val === "pull" && it.has_peer) {
    state.actions[it.rel_path] = "pull";
  } else if (val === "delete_local" && it.has_local) {
    state.actions[it.rel_path] = "delete_local";
  } else if (val === "delete_peer" && it.has_peer) {
    state.actions[it.rel_path] = "delete_peer";
  } else if (val === "skip") {
    state.actions[it.rel_path] = "skip";
  }
}

function applyCompareResult(data) {
  state.items = data.items || [];
  state.itemMap = {};
  state.currentDir = "";
  state.actions = {};
  state.folderActions = {};
  state.checked.clear();
  state.checkedFolders.clear();
  if (data.local_space !== undefined) state.localSpace = data.local_space;
  if (data.peer_space !== undefined) state.peerSpace = data.peer_space;
  registerItems(state.items, true);
  const s = data.summary || {};
  const diffCount = (s.new_local || 0) + (s.new_peer || 0) + (s.newer_local || 0) + (s.newer_peer || 0) + (s.size_diff || 0) + (s.hash_diff || 0);
  el("countDiff").textContent = diffCount;
  el("countNewerLocal").textContent = s.newer_local || 0;
  el("countNewerPeer").textContent = s.newer_peer || 0;
  el("countNewLocal").textContent = s.new_local || 0;
  el("countNewPeer").textContent = s.new_peer || 0;
  el("countIdentical").textContent = s.identical || 0;

  const textCount = data.text_verify_count || 0;
  if (textCount > 0) {
    el("verifyHintText").textContent =
      `Hint: Ditemukan ${textCount.toLocaleString("id-ID")} file teks/kode berukuran byte sama dengan tanggal berbeda. Klik tombol di kanan jika ingin verifikasi SHA-256.`;
    el("verifyHintBar").classList.remove("hidden");
  } else {
    el("verifyHintBar").classList.add("hidden");
  }
  refreshView();
}

async function refreshView() {
  if (state.viewMode === "tree" && !state.search.trim()) {
    el("breadcrumbBar").classList.remove("hidden");
    const params = new URLSearchParams({ dir: state.currentDir, filter: state.filter });
    const res = await fetch(`/api/local/tree?${params.toString()}`);
    const data = await parseResponseJSON(res);
    state.treeFolders = data.folders || [];
    state.treeFiles = data.files || [];
    registerItems(state.treeFiles, false);
    renderBreadcrumb();
    renderTreeTable();
    return;
  }

  el("breadcrumbBar").classList.add("hidden");
  const params = new URLSearchParams({ filter: state.filter, q: state.search });
  const res = await fetch(`/api/local/items?${params.toString()}`);
  const data = await parseResponseJSON(res);
  state.items = data.items || [];
  registerItems(state.items, false);
  renderFlatTable();
}

function renderBreadcrumb() {
  const bar = el("breadcrumbBar");
  bar.innerHTML = "";
  const rootBtn = document.createElement("button");
  rootBtn.className = "crumb";
  rootBtn.textContent = "Root";
  rootBtn.onclick = () => { state.currentDir = ""; refreshView(); };
  bar.appendChild(rootBtn);

  if (!state.currentDir) return;
  const parts = state.currentDir.split("/");
  let acc = "";
  for (const part of parts) {
    acc = acc ? `${acc}/${part}` : part;
    const sep = document.createElement("span");
    sep.className = "crumb-sep";
    sep.textContent = "/";
    bar.appendChild(sep);

    const target = acc;
    const btn = document.createElement("button");
    btn.className = "crumb";
    btn.textContent = part;
    btn.onclick = () => { state.currentDir = target; refreshView(); };
    bar.appendChild(btn);
  }
}

function renderTreeTable() {
  const tbody = el("diffTableBody");
  tbody.innerHTML = "";

  for (const f of state.treeFolders) {
    const tr = document.createElement("tr");
    tr.className = "folder-row";
    const summaryParts = [];
    if (f.newer_local) summaryParts.push(`Lokal Baru: ${f.newer_local}`);
    if (f.newer_peer) summaryParts.push(`Peer Baru: ${f.newer_peer}`);
    if (f.new_local) summaryParts.push(`+Lokal: ${f.new_local}`);
    if (f.new_peer) summaryParts.push(`+Peer: ${f.new_peer}`);

    const isFolderChecked = state.checkedFolders.has(f.rel_path);
    const fAct = state.folderActions[f.rel_path] || "";
    const isDel = fAct === "delete_local" || fAct === "delete_peer";

    tr.innerHTML = `
      <td class="col-check"><input type="checkbox" class="folder-chk" data-folder="${escapeHtml(f.rel_path)}" ${isFolderChecked ? "checked" : ""}></td>
      <td><button type="button" class="folder-link" data-folder="${escapeHtml(f.rel_path)}">&#128193; ${escapeHtml(f.name)}/</button></td>
      <td><span class="badge b-IDENTICAL">${f.total_files} item</span><span class="sub-meta">${escapeHtml(summaryParts.join(" | ") || "Identik")}</span></td>
      <td class="num">${formatBytes(f.local_size || 0)}</td>
      <td class="num">${formatBytes(f.peer_size || 0)}</td>
      <td>
        <select class="action-select folder-action-select ${isDel ? "is-delete" : ""}" data-folder="${escapeHtml(f.rel_path)}">
          <option value="" ${fAct === "" ? "selected" : ""}>— Aksi Folder —</option>
          <option value="default" ${fAct === "default" ? "selected" : ""}>Ikuti Rekomendasi Terbaru</option>
          <option value="push" ${fAct === "push" ? "selected" : ""}>Copy Lokal → Peer (Semua)</option>
          <option value="pull" ${fAct === "pull" ? "selected" : ""}>Copy Peer → Lokal (Semua)</option>
          <option value="delete_local" ${fAct === "delete_local" ? "selected" : ""}>Hapus Lokal (Semua)</option>
          <option value="delete_peer" ${fAct === "delete_peer" ? "selected" : ""}>Hapus Peer (Semua)</option>
          <option value="skip" ${fAct === "skip" ? "selected" : ""}>Lewati Semua (Skip)</option>
        </select>
      </td>
    `;
    tbody.appendChild(tr);
  }

  for (const it of state.treeFiles) {
    tbody.appendChild(buildFileRow(it, it.rel_path.split("/").pop()));
  }
  updateExecSummary();
}

function renderFlatTable() {
  const tbody = el("diffTableBody");
  const limit = 1000;
  const slice = state.items.slice(0, limit);
  tbody.innerHTML = "";
  for (const it of slice) {
    tbody.appendChild(buildFileRow(it, it.rel_path));
  }
  updateExecSummary();
}

function buildFileRow(it, displayName) {
  const tr = document.createElement("tr");
  const act = state.actions[it.rel_path] || "skip";
  const isDel = act === "delete_local" || act === "delete_peer";

  tr.innerHTML = `
    <td class="col-check"><input type="checkbox" class="row-chk" data-path="${escapeHtml(it.rel_path)}" ${state.checked.has(it.rel_path) ? "checked" : ""}></td>
    <td class="mono">${escapeHtml(displayName)}</td>
    <td><span class="badge b-${it.status}">${statusLabel(it.status)}</span></td>
    <td class="num">${it.has_local ? formatBytes(it.local_size) : "—"}<span class="sub-meta">${it.has_local ? formatTime(it.local_mtime_ns) : ""}</span></td>
    <td class="num">${it.has_peer ? formatBytes(it.peer_size) : "—"}<span class="sub-meta">${it.has_peer ? formatTime(it.peer_mtime_ns) : ""}</span></td>
    <td>
      <select class="action-select ${isDel ? "is-delete" : ""}" data-path="${escapeHtml(it.rel_path)}">
        <option value="skip" ${act === "skip" ? "selected" : ""}>Lewati (Skip)</option>
        ${it.has_local ? `<option value="push" ${act === "push" ? "selected" : ""}>Copy Lokal → Peer</option>` : ""}
        ${it.has_peer ? `<option value="pull" ${act === "pull" ? "selected" : ""}>Copy Peer → Lokal</option>` : ""}
        ${it.has_local ? `<option value="delete_local" ${act === "delete_local" ? "selected" : ""}>Hapus Lokal</option>` : ""}
        ${it.has_peer ? `<option value="delete_peer" ${act === "delete_peer" ? "selected" : ""}>Hapus Peer</option>` : ""}
      </select>
    </td>
  `;
  return tr;
}

el("diffTableBody").addEventListener("click", (e) => {
  const btn = e.target.closest(".folder-link");
  if (!btn) return;
  state.currentDir = btn.dataset.folder;
  refreshView();
});

function escapeHtml(s) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

async function applyFolderChange(folder, checked, actionVal) {
  if (checked) state.checkedFolders.add(folder);
  else state.checkedFolders.delete(folder);

  const params = new URLSearchParams({ filter: state.filter, prefix: folder });
  const res = await fetch(`/api/local/items?${params.toString()}`);
  const data = await parseResponseJSON(res);
  const items = data.items || [];
  registerItems(items, false);

  for (const it of items) {
    if (actionVal) {
      applyActionToItem(it, actionVal);
    }
    if (checked) state.checked.add(it.rel_path);
    else state.checked.delete(it.rel_path);
  }
  updateExecSummary();
}

el("diffTableBody").addEventListener("change", async (e) => {
  if (e.target.classList.contains("folder-chk")) {
    const folder = e.target.dataset.folder;
    await applyFolderChange(folder, e.target.checked, state.folderActions[folder] || "");
    return;
  }

  if (e.target.classList.contains("folder-action-select")) {
    const folder = e.target.dataset.folder;
    const val = e.target.value;
    state.folderActions[folder] = val;
    e.target.classList.toggle("is-delete", val === "delete_local" || val === "delete_peer");
    if (val) {
      const rowChk = e.target.closest("tr")?.querySelector(".folder-chk");
      const shouldCheck = val !== "skip";
      if (rowChk) rowChk.checked = shouldCheck;
      await applyFolderChange(folder, shouldCheck, val);
    }
    return;
  }

  const path = e.target.dataset.path;
  if (!path) return;
  if (e.target.classList.contains("row-chk")) {
    if (e.target.checked) state.checked.add(path);
    else state.checked.delete(path);
    updateExecSummary();
  } else if (e.target.classList.contains("action-select")) {
    const val = e.target.value;
    state.actions[path] = val;
    e.target.classList.toggle("is-delete", val === "delete_local" || val === "delete_peer");
    if (val !== "skip") state.checked.add(path);
    updateExecSummary();
  }
});

el("selectAllChk").addEventListener("change", async (e) => {
  const checked = e.target.checked;
  if (state.viewMode === "tree" && !state.search.trim()) {
    for (const it of state.treeFiles) {
      if (checked) state.checked.add(it.rel_path);
      else state.checked.delete(it.rel_path);
    }
    await Promise.all(state.treeFolders.map((f) => applyFolderChange(f.rel_path, checked, "")));
  } else {
    for (const it of state.items) {
      if (checked) state.checked.add(it.rel_path);
      else state.checked.delete(it.rel_path);
    }
  }
  refreshView();
});

el("viewModeBtn").addEventListener("click", () => {
  state.viewMode = state.viewMode === "tree" ? "flat" : "tree";
  el("viewModeBtn").textContent = state.viewMode === "tree" ? "Mode: File Manager" : "Mode: Daftar Datar";
  refreshView();
});

document.querySelectorAll(".stat-chip").forEach((btn) => {
  btn.addEventListener("click", () => {
    document.querySelectorAll(".stat-chip").forEach((b) => b.classList.remove("active"));
    btn.classList.add("active");
    state.filter = btn.dataset.filter;
    refreshView();
  });
});

let searchTimer = null;
el("searchInput").addEventListener("input", (e) => {
  state.search = e.target.value;
  clearTimeout(searchTimer);
  searchTimer = setTimeout(refreshView, 180);
});

el("applyBatchBtn").addEventListener("click", () => {
  const val = el("batchActionSelect").value;
  if (!val) return;
  for (const relPath of state.checked) {
    applyActionToItem(state.itemMap[relPath], val);
  }
  refreshView();
});

async function runVerifyHash(payload, btn, idleLabel, onSuccess) {
  btn.disabled = true;
  btn.textContent = "Memeriksa SHA-256...";
  el("scanSectionTitle").textContent = "Memeriksa SHA-256 Secara Live...";
  el("cancelScanBtn").classList.remove("hidden");
  el("scanProgressSection").classList.remove("hidden");
  el("scanLocalStats").textContent = "0% (0/0 file)";
  el("scanLocalCurrent").textContent = "Memulai hashing...";
  el("scanPeerStats").textContent = "0% (0/0 file)";
  el("scanPeerCurrent").textContent = "Memulai hashing...";

  activeAbort = new AbortController();
  const evtSrc = new EventSource("/api/local/events");
  evtSrc.onmessage = (ev) => {
    const p = JSON.parse(ev.data);
    if (p.type !== "hash_progress") return;
    const etaPart = p.eta_sec >= 0 ? ` • Sisa ${formatETA(p.eta_sec)}` : "";
    const text = `${p.percent}% (${p.done}/${p.total} file)${etaPart}`;
    if (p.side === "local") {
      el("scanLocalStats").textContent = text;
      el("scanLocalCurrent").textContent = p.current || "Selesai.";
    } else if (p.side === "peer") {
      el("scanPeerStats").textContent = text;
      el("scanPeerCurrent").textContent = p.current || "Selesai.";
    }
  };

  try {
    const res = await fetch("/api/local/verify-hash", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
      signal: activeAbort.signal,
    });
    const data = await parseResponseJSON(res);
    applyCompareResult(data);
    el("scanSectionTitle").textContent = "Verifikasi SHA-256 Selesai";
    if (onSuccess) onSuccess();
  } catch (err) {
    if (err.name === "AbortError") {
      el("scanSectionTitle").textContent = "Verifikasi SHA-256 Dibatalkan";
      el("scanLocalCurrent").textContent = "Verifikasi SHA-256 dibatalkan.";
      el("scanPeerCurrent").textContent = "Verifikasi SHA-256 dibatalkan.";
    } else {
      el("scanSectionTitle").textContent = "Verifikasi SHA-256 Gagal";
      alert("Verify SHA-256 gagal: " + err.message);
    }
  } finally {
    activeAbort = null;
    evtSrc.close();
    el("cancelScanBtn").classList.add("hidden");
    btn.disabled = false;
    btn.textContent = idleLabel;
  }
}

el("verifyTextBtn").addEventListener("click", () => {
  runVerifyHash({ mode: "text_candidates" }, el("verifyTextBtn"), "Verify SHA-256 (Teks/Kode)", () => {
    el("verifyHintBar").classList.add("hidden");
  });
});

el("verifyHashBtn").addEventListener("click", () => {
  const targets = Array.from(state.checked).filter((p) => {
    const it = state.itemMap[p];
    return it && it.has_local && it.has_peer;
  });
  if (targets.length === 0) {
    alert("Centang minimal satu file yang ada di kedua sisi untuk verifikasi SHA-256.");
    return;
  }
  runVerifyHash({ rel_paths: targets }, el("verifyHashBtn"), "Verify SHA-256", null);
});

function getSelectedActions() {
  const list = [];
  for (const rel of state.checked) {
    const act = state.actions[rel];
    if (act && act !== "skip") {
      list.push({ rel_path: rel, action: act });
    }
  }
  return list;
}

function computeSpacePlan(list) {
  const perm = el("permanentDeleteChk").checked;
  let localWrite = 0, localNet = 0;
  let peerWrite = 0, peerNet = 0;

  for (const a of list) {
    const it = state.itemMap[a.rel_path];
    if (!it) continue;
    if (a.action === "pull") {
      const pSz = it.peer_size || 0;
      const lSz = it.has_local ? (it.local_size || 0) : 0;
      localWrite += pSz;
      localNet += (pSz - lSz);
    } else if (a.action === "push") {
      const lSz = it.local_size || 0;
      const pSz = it.has_peer ? (it.peer_size || 0) : 0;
      peerWrite += lSz;
      peerNet += (lSz - pSz);
    } else if (a.action === "delete_local" && perm) {
      localNet -= (it.local_size || 0);
    } else if (a.action === "delete_peer" && perm) {
      peerNet -= (it.peer_size || 0);
    }
  }
  return { localWrite, localNet, peerWrite, peerNet };
}

function renderSideSpace(freeElId, needElId, spaceInfo, writeBytes, netDelta) {
  const freeEl = el(freeElId);
  const needEl = el(needElId);
  const hasInfo = spaceInfo && spaceInfo.total_bytes > 0;
  freeEl.textContent = hasInfo
    ? `Tersedia: ${formatBytes(spaceInfo.free_bytes)} / ${formatBytes(spaceInfo.total_bytes)}`
    : "Info space belum tersedia";

  const netNeed = Math.max(0, netDelta);
  let text = netDelta < 0
    ? `Perubahan netto: hemat ${formatBytes(-netDelta)}`
    : `Kebutuhan netto: ${formatBytes(netNeed)}`;
  if (writeBytes > netNeed && writeBytes > 0) {
    text += ` (transfer: ${formatBytes(writeBytes)})`;
  }

  let insufficient = false;
  if (hasInfo) {
    if (netNeed > spaceInfo.free_bytes) {
      insufficient = true;
      text += ` — TIDAK CUKUP (kurang ${formatBytes(netNeed - spaceInfo.free_bytes)})`;
    } else if (writeBytes > spaceInfo.free_bytes) {
      insufficient = true;
      text += ` — TIDAK CUKUP untuk file sementara (kurang ${formatBytes(writeBytes - spaceInfo.free_bytes)})`;
    } else if (writeBytes > 0 || netNeed > 0) {
      text += ` — Cukup`;
    }
  }
  needEl.textContent = text;
  needEl.className = "space-need mono " + (insufficient ? "is-danger" : (hasInfo ? "is-ok" : ""));
  return insufficient;
}

function updateExecSummary() {
  const list = getSelectedActions();
  const counts = { push: 0, pull: 0, delete_local: 0, delete_peer: 0 };
  for (const a of list) counts[a.action] = (counts[a.action] || 0) + 1;
  el("execSummaryText").textContent =
    `${list.length} aksi (Copy→Peer: ${counts.push}, Copy→Lokal: ${counts.pull}, Hapus Lokal: ${counts.delete_local}, Hapus Peer: ${counts.delete_peer})`;

  const plan = computeSpacePlan(list);
  const hasAnySpace = (state.localSpace?.total_bytes > 0) || (state.peerSpace?.total_bytes > 0);
  if (hasAnySpace || list.length > 0) {
    el("spacePanel").classList.remove("hidden");
    state.localInsufficient = renderSideSpace("localSpaceFree", "localSpaceNeed", state.localSpace, plan.localWrite, plan.localNet);
    state.peerInsufficient = renderSideSpace("peerSpaceFree", "peerSpaceNeed", state.peerSpace, plan.peerWrite, plan.peerNet);
  } else {
    el("spacePanel").classList.add("hidden");
    state.localInsufficient = false;
    state.peerInsufficient = false;
  }
}

el("permanentDeleteChk").addEventListener("change", updateExecSummary);

el("executeBtn").addEventListener("click", async () => {
  const actions = getSelectedActions();
  if (actions.length === 0) {
    alert("Tidak ada aksi yang dipilih.");
    return;
  }
  const perm = el("permanentDeleteChk").checked;
  const hasDel = actions.some((a) => a.action.startsWith("delete_"));
  let spaceWarn = "";
  if (state.localInsufficient || state.peerInsufficient) {
    const sides = [];
    if (state.localInsufficient) sides.push("Lokal (PC)");
    if (state.peerInsufficient) sides.push("Peer (Laptop)");
    spaceWarn = `\n\nPERINGATAN KERAS: Ruang disk di ${sides.join(" & ")} TIDAK MENCUKUPI! Proses berisiko gagal di tengah jalan.`;
  }
  const msg = `Jalankan ${actions.length} aksi terpilih?` +
    (hasDel ? (perm ? "\nPERINGATAN: Mode Hapus Permanen aktif!" : "\nFile yang dihapus akan dipindahkan ke Recycle Bin.") : "") +
    spaceWarn;
  if (!confirm(msg)) return;

  el("executeBtn").disabled = true;
  el("progressPanel").classList.remove("hidden");
  el("cancelExecBtn").classList.remove("hidden");
  el("progressFill").style.width = "0%";
  el("progressLabel").textContent = "Memulai eksekusi...";
  el("progressSpeed").textContent = "0.00 MB/s";
  el("execLog").textContent = "Memulai eksekusi...";

  activeAbort = new AbortController();
  const evtSrc = new EventSource("/api/local/events");
  evtSrc.onmessage = (ev) => {
    const p = JSON.parse(ev.data);
    if (p.type === "scan_progress" || p.type === "hash_progress") return;
    const pct = p.total > 0 ? Math.round((p.done / p.total) * 100) : 100;
    el("progressFill").style.width = `${pct}%`;
    el("progressLabel").textContent = `[${p.done}/${p.total}] ${p.current}`;
    el("progressSpeed").textContent = `${p.mbps.toFixed(2)} MB/s • Sisa ${formatETA(p.eta_sec)}`;
  };

  try {
    const res = await fetch("/api/local/execute", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ permanent_delete: perm, actions }),
      signal: activeAbort.signal,
    });
    const out = await parseResponseJSON(res);
    el("progressFill").style.width = "100%";
    el("progressLabel").textContent = `Eksekusi Selesai (${out.completed}/${actions.length} aksi)`;
    el("progressSpeed").textContent = "";
    el("execLog").textContent = `Selesai: ${out.completed} aksi (${formatBytes(out.bytes_moved)}) dalam ${out.duration_ms} ms.` +
      (out.errors?.length ? ` Error: ${out.errors.join(" | ")}` : "");
    el("scanBtn").click();
  } catch (err) {
    if (err.name === "AbortError") {
      el("progressLabel").textContent = "Eksekusi Dibatalkan";
      el("execLog").textContent = "Eksekusi dibatalkan oleh pengguna.";
    } else {
      el("progressLabel").textContent = "Eksekusi Gagal";
      el("execLog").textContent = "Gagal: " + err.message;
    }
  } finally {
    activeAbort = null;
    evtSrc.close();
    el("cancelExecBtn").classList.add("hidden");
    el("executeBtn").disabled = false;
  }
});

async function openBrowseModal(side, targetInputId) {
  if (side === "peer" && !state.connected) {
    alert("Hubungkan ke Peer terlebih dahulu untuk menelusuri folder Laptop.");
    return;
  }
  state.browseSide = side;
  state.browseTargetInput = targetInputId;
  el("browseModalTitle").textContent = side === "local" ? "Pilih Folder Lokal (PC)" : "Pilih Folder Peer (Laptop)";
  el("browseModal").classList.remove("hidden");
  await loadBrowsePath(el(targetInputId).value.trim());
}

async function loadBrowsePath(path) {
  const listEl = el("browseList");
  listEl.innerHTML = "<div class='dir-item'>Memuat...</div>";
  try {
    const params = new URLSearchParams({ side: state.browseSide, path });
    const res = await fetch(`/api/local/browse?${params.toString()}`);
    const data = await parseResponseJSON(res);
    state.browseCurrentPath = data.current_path || "";
    el("browseCurrentPath").value = state.browseCurrentPath || "Daftar Drive";
    listEl.innerHTML = "";
    const dirs = data.dirs || [];
    if (dirs.length === 0) {
      listEl.innerHTML = "<div class='dir-item'>Tidak ada subfolder.</div>";
      return;
    }
    for (const d of dirs) {
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "dir-item";
      btn.innerHTML = `<span>&#128193;</span><span>${escapeHtml(d.name)}</span>`;
      btn.onclick = () => loadBrowsePath(d.path);
      listEl.appendChild(btn);
    }
  } catch (err) {
    if (path !== "") {
      await loadBrowsePath("");
      return;
    }
    listEl.innerHTML = `<div class='dir-item'>Gagal memuat: ${escapeHtml(err.message)}</div>`;
  }
}

el("browseLocalBtn").addEventListener("click", () => openBrowseModal("local", "localPathInput"));
el("browsePeerBtn").addEventListener("click", () => openBrowseModal("peer", "remotePathInput"));
el("closeBrowseBtn").addEventListener("click", () => el("browseModal").classList.add("hidden"));
el("selectCurrentDirBtn").addEventListener("click", () => {
  if (state.browseCurrentPath) {
    el(state.browseTargetInput).value = state.browseCurrentPath;
  }
  el("browseModal").classList.add("hidden");
});
el("browseUpBtn").addEventListener("click", () => {
  const cur = state.browseCurrentPath;
  if (!cur || /^[A-Za-z]:\\?$/.test(cur)) {
    loadBrowsePath("");
    return;
  }
  const clean = cur.replace(/[\\/]+$/, "");
  const idx = Math.max(clean.lastIndexOf("\\"), clean.lastIndexOf("/"));
  if (idx <= 2) {
    loadBrowsePath(clean.slice(0, 3));
  } else {
    loadBrowsePath(clean.slice(0, idx));
  }
});

loadPairs();
