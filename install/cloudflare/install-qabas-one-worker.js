/**
 * Cloudflare Worker for https://install.qabas.one
 *
 * Routes:
 *   GET /              → short help (text)
 *   GET /install.sh    → qabas bootstrap installer (Linux)
 *   GET /install.ps1   → qabas bootstrap installer (Windows)
 *   GET /qabasonectl   → 410 Gone (legacy; removed)
 *
 * Source: GitHub raw main, Contents API, or latest release asset install.sh.
 * Private repo: set Worker secret GITHUB_TOKEN (contents:read).
 *
 * Deploy: see install/cloudflare/README.md
 */

const DEFAULT_REPO = "qabasone/qabasone-cli";
const DEFAULT_REF = "main";
const SCRIPT_PATH = "install/install.sh";
const WINDOWS_SCRIPT_PATH = "install/install.ps1";
const RELEASE_ASSET = "install.sh";
const WINDOWS_RELEASE_ASSET = "install.ps1";

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const path = url.pathname.replace(/\/+$/, "") || "/";

    if (request.method !== "GET" && request.method !== "HEAD") {
      return new Response("method not allowed\n", { status: 405 });
    }

    if (path === "/") {
      const body = [
        "QabasOne install host",
        "",
        "Install qabas CLI + QabasOne (Linux):",
        "  curl -fsSL https://install.qabas.one/install.sh | sudo bash",
        "",
        "CLI only (Linux):",
        "  curl -fsSL https://install.qabas.one/install.sh | sudo bash -s -- --cli-only",
        "",
        "CLI only (Windows):",
        "  iwr -useb https://install.qabas.one/install.ps1 -OutFile install.ps1",
        "  .\\install.ps1 -CliOnly",
        "",
        "Uninstall: apt remove qabas (Linux .deb) / Apps & Features (Windows MSI).",
        "",
        "Private repos: export GITHUB_TOKEN before curl/iwr, or pass Authorization.",
        "Docs: https://github.com/qabasone/qabasone-cli",
        "",
      ].join("\n");
      return text(body, 200);
    }

    if (path === "/qabasonectl" || path === "/qabasonectl/install.sh") {
      return text(
        [
          "gone — /qabasonectl was removed.",
          "",
          "The CLI moved to qabasone-cli and is installed as `qabas`:",
          "  curl -fsSL https://install.qabas.one/install.sh | sudo bash -s -- --cli-only",
          "",
          "Product manifests live in qabasone-deploy (not CLI releases).",
          "",
        ].join("\n"),
        410,
      );
    }

    if (path === "/install.sh") {
      return serveInstallScript(env, SCRIPT_PATH, RELEASE_ASSET);
    }

    if (path === "/install.ps1") {
      return serveInstallScript(env, WINDOWS_SCRIPT_PATH, WINDOWS_RELEASE_ASSET);
    }

    return text("not found\n", 404);
  },
};

function githubHeaders(env) {
  const headers = {
    "User-Agent": "install.qabas.one-worker",
    Accept: "application/vnd.github.raw",
  };
  const token = env.GITHUB_TOKEN;
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  return headers;
}

async function serveInstallScript(env, scriptPath, releaseAsset) {
  const repo = env.REPO || DEFAULT_REPO;
  const primaryRef = env.INSTALL_REF || DEFAULT_REF;
  const refs = uniqueRefs(primaryRef);

  for (const ref of refs) {
    const body = await fetchScriptFromRef(env, repo, ref, scriptPath);
    if (body) {
      return scriptResponse(body);
    }
  }

  for (const tag of uniqueReleaseTags(primaryRef)) {
    const releaseURL = `https://github.com/${repo}/releases/download/${tag}/${releaseAsset}`;
    const res = await fetch(releaseURL, { headers: githubHeaders(env) });
    if (res.ok) {
      return scriptResponse(await res.text());
    }
  }

  const hint = env.GITHUB_TOKEN
    ? ""
    : " (set Worker secret GITHUB_TOKEN if the repo is private)";
  return text(`failed to fetch install script (404)${hint}\n`, 502);
}

function uniqueRefs(primaryRef) {
  const refs = [primaryRef];
  for (const fallback of ["main", "dev"]) {
    if (!refs.includes(fallback)) {
      refs.push(fallback);
    }
  }
  return refs;
}

function uniqueReleaseTags(primaryRef) {
  const tags = ["latest"];
  for (const fallback of [primaryRef, "dev"]) {
    if (fallback && fallback !== "latest" && !tags.includes(fallback)) {
      tags.push(fallback);
    }
  }
  return tags;
}

async function fetchScriptFromRef(env, repo, ref, scriptPath) {
  const rawURL = `https://raw.githubusercontent.com/${repo}/${ref}/${scriptPath}`;
  const headers = githubHeaders(env);

  let res = await fetch(rawURL, {
    cf: { cacheTtl: 60, cacheEverything: true },
    headers,
  });
  if (res.ok) {
    return await res.text();
  }

  const apiURL = `https://api.github.com/repos/${repo}/contents/${scriptPath}?ref=${encodeURIComponent(ref)}`;
  res = await fetch(apiURL, {
    cf: { cacheTtl: 60, cacheEverything: true },
    headers,
  });
  if (res.ok) {
    return await res.text();
  }
  return null;
}

function scriptResponse(body) {
  return new Response(body, {
    status: 200,
    headers: {
      "Content-Type": "text/plain; charset=utf-8",
      "Cache-Control": "public, max-age=60",
      "X-Content-Type-Options": "nosniff",
    },
  });
}

function text(body, status) {
  return new Response(body, {
    status,
    headers: {
      "Content-Type": "text/plain; charset=utf-8",
      "Cache-Control": "public, max-age=60",
    },
  });
}
