// Magertron for VS Code — hand a server you have built to the team that governs it.
//
// ⚠ THIS EXTENSION IS A BUTTON. It does not parse manifests, does not scrub
// credentials, does not decide what can be deployed. It shells out to the
// bundled `mcpctl` binary, which does all of that.
//
// That is deliberate and it is the whole architecture. The submission contract
// — which manifest shapes are valid, which values must never leave the
// developer's machine, how a server is classified — lives in ONE
// implementation. An extension that reimplemented any of it in TypeScript
// would drift from the CLI the moment the MCP spec moved, and the drift would
// show up as a developer being told two different things by two Magertron
// tools.
//
// ⚠ SUBMITTING IS NOT DEPLOYING. This records that your server EXISTS so your
// platform team can review it. It creates no route and deploys nothing. Every
// message below says so, because a developer who believes they have deployed
// something will not chase the approval that actually matters.

import * as vscode from 'vscode';
import * as path from 'path';
import * as fs from 'fs';
import { execFile } from 'child_process';
import { promisify } from 'util';

const execFileAsync = promisify(execFile);

// ─── the bundled binary ─────────────────────────────────────────────────────

/**
 * Resolve the mcpctl binary shipped inside this extension.
 *
 * ⚠ BUNDLED, NOT REQUIRED. An extension whose point is "you should not need a
 * terminal" cannot start by asking the developer to install a CLI. The binary
 * ships in bin/<platform>/ and is versioned with the extension, so the
 * submission contract the plugin speaks is always the one its CLI implements.
 */
function bundledMcpctl(ctx: vscode.ExtensionContext): string | undefined {
  const plat = process.platform;   // 'darwin' | 'linux' | 'win32'
  const arch = process.arch;       // 'arm64' | 'x64'

  const dir = `${plat}-${arch}`;
  const exe = plat === 'win32' ? 'mcpctl.exe' : 'mcpctl';
  const p = path.join(ctx.extensionPath, 'bin', dir, exe);

  if (!fs.existsSync(p)) { return undefined; }

  // ⚠ npm and the marketplace do not reliably preserve the executable bit.
  // Set it rather than failing with EACCES, which reads as a broken install.
  if (plat !== 'win32') {
    try { fs.chmodSync(p, 0o755); } catch { /* best effort */ }
  }
  return p;
}

/**
 * Build the environment mcpctl runs with.
 *
 * ⚠ ENV, NOT THE CONFIG FILE. Writing to ~/.config/mcpctl would clobber an
 * interactive `mcpctl login` the developer may already rely on — a plugin has
 * no business overwriting the state of a tool it did not install.
 *
 * ⚠ AND PARTIAL OVERRIDES ARE THE POINT. A developer who has already run
 * `mcpctl login` can leave the token setting empty: mcpctl takes the server URL
 * from here and the credential from their own file. Nothing is duplicated and
 * nothing is stored twice.
 */
function envFor(): NodeJS.ProcessEnv {
  const cfg = vscode.workspace.getConfiguration('magertron');
  const env: NodeJS.ProcessEnv = { ...process.env };

  const url = (cfg.get<string>('serverUrl') ?? '').trim();
  const tok = (cfg.get<string>('token') ?? '').trim();

  if (url) { env.MCPCTL_SERVER = url; }

  // ⚠ Diagnostic 2026-08-17. status reaches the configured server and submit
  // reaches the one in the config FILE, from the same process and the same
  // envFor(). One of those two statements must be false; this says which.
  console.log(`[magertron] envFor: MCPCTL_SERVER=${env.MCPCTL_SERVER ?? "<unset>"}`);
  if (tok) { env.MCPCTL_TOKEN = tok; }
  if (cfg.get<boolean>('insecureTls')) { env.MCPCTL_INSECURE = '1'; }

  // ⚠ Identify the tool, so an operator reviewing a submission can see it came
  // from an editor rather than a terminal. Harmless if wrong — it is a
  // convenience for deciding where to ask a question, not an identity.
  // The editor's own name, not a constant. This extension runs unchanged
  // in Cursor and other VS Code forks, and hardcoding 'vscode' made a
  // Cursor submission claim it came from somewhere it did not.
  env.MCPCTL_CLIENT = vscode.env.appName
    ? vscode.env.appName.toLowerCase().replace(/\s+/g, '-')
    : 'vscode';

  return env;
}

// ─── output ─────────────────────────────────────────────────────────────────

let channel: vscode.OutputChannel;

function log(line: string) {
  channel.appendLine(line);
}

// ─── commands ───────────────────────────────────────────────────────────────

async function pickManifest(uri?: vscode.Uri): Promise<vscode.Uri | undefined> {
  if (uri) { return uri; }

  // The active editor is the likeliest intent — a developer with their
  // manifest open and the command palette down means that file.
  const active = vscode.window.activeTextEditor?.document;
  if (active && /\.(mcpb|json)$/i.test(active.fileName)) {
    // ⚠ SAVE FIRST. mcpctl reads from DISK. Submitting the saved version of a
    // file the developer has since edited would send something they never
    // wrote and cannot see — a silent wrong answer, which is worse than a
    // refusal.
    if (active.isDirty) {
      const save = await vscode.window.showWarningMessage(
        `${path.basename(active.fileName)} has unsaved changes. ` +
        `Magertron submits what is on disk.`,
        'Save and submit', 'Cancel');
      if (save !== 'Save and submit') { return undefined; }
      await active.save();
    }
    return active.uri;
  }

  const picked = await vscode.window.showOpenDialog({
    canSelectMany: false,
    openLabel: 'Submit',
    filters: { 'MCP manifests': ['mcpb', 'json'] },
  });
  return picked?.[0];
}

async function submit(ctx: vscode.ExtensionContext, uri?: vscode.Uri) {
  const bin = bundledMcpctl(ctx);
  if (!bin) {
    vscode.window.showErrorMessage(
      `Magertron: no mcpctl binary bundled for ${process.platform}-${process.arch}. ` +
      `Please report this — the extension ships binaries per platform and yours is missing.`);
    return;
  }

  const cfg = vscode.workspace.getConfiguration('magertron');
  if (!(cfg.get<string>('serverUrl') ?? '').trim()) {
    const act = await vscode.window.showErrorMessage(
      'Magertron: set your platform URL before submitting.',
      'Open settings');
    if (act === 'Open settings') {
      vscode.commands.executeCommand('workbench.action.openSettings', 'magertron.serverUrl');
    }
    return;
  }

  const file = await pickManifest(uri);
  if (!file) { return; }

  log(`\n── submit ${file.fsPath}`);

  await vscode.window.withProgress(
    { location: vscode.ProgressLocation.Notification, title: 'Magertron: submitting…' },
    async () => {
      try {
        const { stdout } = await execFileAsync(bin, ['submit', file.fsPath], {
          env: envFor(),
          timeout: 60_000,
        });
        log(stdout.trim());

        // ⚠ Say what did NOT happen. A developer who believes they have
        // deployed something will not chase the approval that actually
        // matters, and their server will sit ungoverned while they assume
        // otherwise.
        const act = await vscode.window.showInformationMessage(
          'Submitted for review. This does not deploy it — your platform team decides.',
          'Show details');
        if (act === 'Show details') { channel.show(); }

      } catch (e: any) {
        // ⚠ mcpctl's stderr is written to be READ BY A DEVELOPER — it names the
        // manifest problem, or says the token lacks a scope. Surfacing "command
        // failed with exit code 1" instead would throw away the only useful
        // part.
        const msg = (e?.stderr || e?.message || String(e)).trim();
        log(msg);
        const act = await vscode.window.showErrorMessage(
          `Magertron: ${msg.split('\n')[0]}`, 'Show details');
        if (act === 'Show details') { channel.show(); }
      }
    });
}

async function status(ctx: vscode.ExtensionContext) {
  const bin = bundledMcpctl(ctx);
  if (!bin) {
    vscode.window.showErrorMessage(
      `Magertron: no mcpctl bundled for ${process.platform}-${process.arch}.`);
    return;
  }
  try {
    const { stdout } = await execFileAsync(bin, ['status'], { env: envFor(), timeout: 20_000 });
    log(`\n── status\n${stdout.trim()}`);
    channel.show();
  } catch (e: any) {
    log((e?.stderr || e?.message || String(e)).trim());
    channel.show();
  }
}

export function activate(ctx: vscode.ExtensionContext) {
  channel = vscode.window.createOutputChannel('Magertron');
  ctx.subscriptions.push(channel);

  ctx.subscriptions.push(
    vscode.commands.registerCommand('magertron.submit',
      (uri?: vscode.Uri) => submit(ctx, uri)),
    vscode.commands.registerCommand('magertron.status',
      () => status(ctx)),
  );
}

export function deactivate() { /* nothing to clean up */ }
