# Research: Devbox Configuration for Node.js 20 + PM2

**Date:** 2026-04-22  
**Sources:**
- https://jetify.com/docs/devbox/devbox_examples/languages/nodejs (official NodeJS guide)
- https://jetify.com/docs/devbox/configuration (devbox.json reference)
- https://jetify.com/docs/devbox/guides/pinning_packages (version pinning)
- https://www.nixhub.io/packages/nodejs (version lookup)
- https://github.com/jetify-com/devbox/issues/1577 (nodePackages compatibility)

---

## 1. devbox.json Format for Node.js 20

Node.js is installed via the `nodejs` package with `@<version>` pinning:

```json
{
  "packages": [
    "nodejs@20"
  ]
}
```

This installs the **latest available 20.x** from the Devbox/Nix search index (semver matching). To pin an exact version, use e.g. `"nodejs@20.12.2"`.

Available versions can be found via:
```bash
devbox search nodejs
```
Or on Nixhub: https://www.nixhub.io/packages/nodejs

Node.js comes **bundled with npm** — no separate npm package needed.

---

## 2. Installing PM2 Globally in Devbox

**Critical:** `npm install -g` does NOT work in Devbox because the Nix store is immutable. The official approach is to install PM2 as a Nix package using the `nodePackages` namespace:

```json
{
  "packages": [
    "nodejs@20",
    "nodePackages.pm2@latest"
  ]
}
```

This is the **officially documented** method from the Jetify NodeJS guide. The `nodePackages.pm2` package provides the `pm2` binary directly in the Devbox shell PATH.

**Important caveat (from GitHub issue #1577):** The `nodePackages.pm2` package is built against a specific Node.js version by Nix. It may not be the exact same Node.js 20 version you specified. In practice this rarely matters since PM2 is a process manager, but be aware of it. The `pm2` binary will use its own bundled Node.js runtime for its own operation, but will use the system `node` to run your application processes.

### Alternative: npm-based global install via init_hook

If you need the exact same Node.js version for PM2, you can use the `NPM_CONFIG_PREFIX` workaround:

```json
{
  "packages": ["nodejs@20"],
  "env": {
    "NPM_CONFIG_PREFIX": "$PWD/.npm-global"
  },
  "shell": {
    "init_hook": [
      "mkdir -p .npm-global",
      "npm install -g pm2"
    ]
  }
}
```

This installs PM2 into a project-local `.npm-global` directory. However, this runs `npm install -g pm2` on every `devbox shell` startup, which adds latency. You can mitigate with a guard:

```json
{
  "packages": ["nodejs@20"],
  "env": {
    "NPM_CONFIG_PREFIX": "$PWD/.npm-global"
  },
  "shell": {
    "init_hook": [
      "if [ ! -f .npm-global/bin/pm2 ]; then npm install -g pm2; fi"
    ]
  }
}
```

---

## 3. Recommended devbox.json for This Project

The recommended configuration using the native Nix approach (preferred):

```json
{
  "packages": [
    "nodejs@20",
    "nodePackages.pm2@latest"
  ],
  "shell": {
    "init_hook": [
      "echo 'Devbox shell ready. node:' $(node --version) 'pm2:' $(pm2 --version)"
    ],
    "scripts": {
      "start": "pm2 start ecosystem.config.js",
      "stop": "pm2 stop ecosystem.config.js",
      "status": "pm2 status",
      "logs": "pm2 logs"
    }
  }
}
```

---

## 4. Commands to Run

```bash
# Initialize devbox (creates devbox.json if not present)
devbox init

# Install packages defined in devbox.json (generates/updates devbox.lock)
devbox install

# Enter the devbox shell (pm2 and node will be available)
devbox shell

# Or run a single command in the devbox environment
devbox run start

# Add packages interactively
devbox add nodejs@20
devbox add nodePackages.pm2@latest

# Search for available versions
devbox search nodejs
```

---

## 5. Key Notes

| Topic | Detail |
|-------|--------|
| **Node.js versioning** | `"nodejs@20"` = latest 20.x. Pin exact with `"nodejs@20.12.2"` |
| **PM2 as Nix package** | `"nodePackages.pm2@latest"` — official approach |
| **`npm -g` fails** | Nix store is immutable; use `nodePackages.*` instead |
| **init_hook** | Runs on every `devbox shell` and `devbox run` — keep it fast |
| **Scripts** | Defined in `shell.scripts`, run via `devbox run <name>` |
| **Lock file** | `devbox.lock` is auto-generated/updated by `devbox install` |
| **Corepack** | Optional: set `"DEVBOX_COREPACK_ENABLED": "true"` in `env` for yarn/pnpm management |
| **PM2 ecosystem** | PM2 config file (ecosystem.config.js) should be in project root |

---

## 6. Potential Issues

1. **PM2 Node.js version mismatch**: `nodePackages.pm2` may be built against a different Node.js version. PM2 spawns your app using the system `node` binary, so this is generally not an issue.

2. **First install is slow**: `devbox install` downloads Nix packages. Subsequent shells are instant.

3. **PM2 home directory**: PM2 stores runtime files in `$HOME/.pm2`. Inside a devbox shell, this works normally.

4. **Process persistence**: PM2 processes do NOT survive shell exit in devbox. When you exit `devbox shell`, PM2 daemon stops. This is expected behavior — use `devbox run start` or keep the shell open during development.
