// Arithmetic sensitivity, not provider telemetry or an invoice calculator.
import assert from 'node:assert/strict';
const emptyWaitsPerDay = 86400 / 25;
const idleWritesPerDay = emptyWaitsPerDay * 3;
assert.equal(idleWritesPerDay, 10368);
assert.equal(idleWritesPerDay * 10, 103680);
const appendsPerBusyHour = 8 * 3600;
assert.equal(appendsPerBusyHour, 28800);
function ciCost(builds, minutesPerBuild, remainingMinutes) {
  return Math.max(0, builds * minutesPerBuild - remainingMinutes) * 0.006;
}
assert.equal(ciCost(100,30,2000),6);
assert.equal(ciCost(300,30,2000),42);
assert.equal(ciCost(100,30,0),18);
assert.equal(ciCost(300,30,0),54);
assert.equal(Math.round((48.44-10)*12*100)/100,461.28);
console.log(JSON.stringify({modeledIdleWritesPerEndpointDay:idleWritesPerDay,
  tenEndpointIdleWritesPerDay:idleWritesPerDay*10, appendsPerBusyHour,
  privateCIAvailableAllowance:[ciCost(100,30,2000),ciCost(300,30,2000)],
  privateCIExhaustedAllowance:[ciCost(100,30,0),ciCost(300,30,0)],
  annualSavingsAtTenDollarTarget:461.28}));
