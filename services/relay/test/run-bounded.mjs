// Derived from the repository's tested push-probe process-group supervisor.
import { spawn } from "node:child_process";

const [secondsText, command, ...args] = process.argv.slice(2);
const seconds = Number(secondsText);
if (!command || !Number.isFinite(seconds) || seconds <= 0) process.exit(2);

const child = spawn(command, args, { stdio: "inherit", env: process.env, detached: true });
let terminating = false;
let exitCode = 1;
function signalGroup(signal) {
  try { process.kill(-child.pid, signal); } catch (error) { if (error.code !== "ESRCH") throw error; }
}
function finish(code) {
  if (terminating) return;
  terminating = true;
  exitCode = code;
  signalGroup("SIGINT");
  setTimeout(() => {
    signalGroup("SIGKILL");
    setTimeout(() => process.exit(exitCode), 250);
  }, 5000);
}
const deadline = setTimeout(() => finish(124), seconds * 1000);
for (const [signal, code] of Object.entries({ SIGHUP: 129, SIGINT: 130, SIGTERM: 143 })) {
  process.on(signal, () => { clearTimeout(deadline); finish(code); });
}
child.on("error", (error) => { clearTimeout(deadline); console.error(error); process.exit(1); });
child.on("exit", (code, signal) => {
  clearTimeout(deadline);
  finish(code ?? (signal ? 128 : 1));
});
