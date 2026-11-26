import {spawn} from 'node:child_process';
import {renameSync, writeFileSync} from 'node:fs';

if (process.argv[2] === 'grandchild') {
  process.on('SIGINT', () => {});
  process.on('SIGTERM', () => {});
  // Publish a complete PID only after the handlers that ignore cleanup exist.
  const pidFile = process.argv[3];
  writeFileSync(`${pidFile}.tmp`, String(process.pid));
  renameSync(`${pidFile}.tmp`, pidFile);
  setInterval(() => {}, 1000);
} else {
  spawn(process.execPath, [import.meta.filename, 'grandchild', process.argv[2]], {
    detached: false, stdio: 'ignore'
  });
  process.on('SIGINT', () => process.exit(0));
  process.on('SIGTERM', () => process.exit(0));
  setInterval(() => {}, 1000);
}
