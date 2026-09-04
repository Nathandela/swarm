import {spawn} from 'node:child_process';
import {writeFileSync} from 'node:fs';

if (process.argv[2] === 'grandchild') {
  process.on('SIGINT', () => {});
  process.on('SIGTERM', () => {});
  // Signal readiness only after the handlers that deliberately ignore cleanup exist.
  writeFileSync(process.argv[3], String(process.pid));
  setInterval(() => {}, 1000);
} else {
  spawn(process.execPath, [import.meta.filename, 'grandchild', process.argv[2]], {
    detached: false, stdio: 'ignore'
  });
  process.on('SIGINT', () => process.exit(0));
  process.on('SIGTERM', () => process.exit(0));
  setInterval(() => {}, 1000);
}
