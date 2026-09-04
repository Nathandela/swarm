import {spawn} from 'node:child_process';

const [secondsText, command, ...args] = process.argv.slice(2);
const seconds = Number(secondsText);
if (!command || !Number.isFinite(seconds) || seconds <= 0) process.exit(2);

// A detached POSIX child becomes leader of a new process group. Negative PID signals only
// that exact group, covering npm, Firebase CLI, the probe, and the emulator JVM.
const child = spawn(command, args, {stdio: 'inherit', env: process.env, detached: true});
let timedOut = false;
let terminating = false;
let exitCode = 1;
function signalGroup(signal) {
  try { process.kill(-child.pid, signal); } catch (e) { if (e.code !== 'ESRCH') throw e; }
}
const deadline = setTimeout(() => {
  timedOut = true;
  terminating = true;
  exitCode = 124;
  signalGroup('SIGINT');
  setTimeout(() => {
    signalGroup('SIGKILL');
    // Keep this supervisor alive long enough for the kernel to deliver the group kill.
    setTimeout(() => process.exit(exitCode), 250);
  }, 5000);
}, seconds * 1000);
const signalCodes = {SIGHUP: 129, SIGINT: 130, SIGTERM: 143};
for (const signal of Object.keys(signalCodes)) {
  process.on(signal, () => {
    if (terminating) return;
    terminating = true;
    exitCode = signalCodes[signal];
    clearTimeout(deadline);
    signalGroup(signal);
    setTimeout(() => {
      signalGroup('SIGKILL');
      setTimeout(() => process.exit(exitCode), 250);
    }, 5000);
  });
}
child.on('error', error => { clearTimeout(deadline); console.error(error); process.exit(1); });
child.on('exit', (code, signal) => {
  if (terminating) return; // A descendant may remain; the referenced kill timer owns exit.
  clearTimeout(deadline);
  if (timedOut) console.error(`Timed out after ${seconds}s: ${command}`);
  process.exit(code ?? (signal ? 128 : 1));
});
