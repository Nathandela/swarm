import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {mkdtempSync, readFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {fileURLToPath} from 'node:url';

const probeDir=fileURLToPath(new URL('.',import.meta.url));
const dir=mkdtempSync(join(tmpdir(),'push-probe-runner-'));
const alive=pid=>{try{process.kill(pid,0);return true}catch(e){if(e.code==='ESRCH')return false;throw e}};
const waitExit=p=>new Promise((resolve,reject)=>{p.on('error',reject);p.on('exit',(code,signal)=>resolve({code,signal}))});
const waitFile=async path=>{for(let i=0;i<250;i++){try{return Number(readFileSync(path,'utf8'))}catch{}await new Promise(r=>setTimeout(r,20))}throw new Error('pid file timeout')};
const waitGone=async pid=>{for(let i=0;i<100;i++){if(!alive(pid))return;await new Promise(r=>setTimeout(r,20))}assert.fail(`grandchild ${pid} survived`)};

// Deadline: leader accepts SIGINT and exits, grandchild ignores it; supervisor must stay
// alive through the grace period and SIGKILL the exact owned group.
let pidFile=join(dir,'timeout.pid');
let p=spawn(process.execPath,['run-bounded.mjs','2',process.execPath,'stubborn-tree.mjs',pidFile],{stdio:'inherit',cwd:probeDir});
let exited=waitExit(p);
let grandchild=await waitFile(pidFile);let result=await exited;assert.equal(result.code,124);await waitGone(grandchild);

// External termination follows the same bounded cleanup path and exit convention.
pidFile=join(dir,'term.pid');
p=spawn(process.execPath,['run-bounded.mjs','30',process.execPath,'stubborn-tree.mjs',pidFile],{stdio:'inherit',cwd:probeDir});
exited=waitExit(p);
grandchild=await waitFile(pidFile);p.kill('SIGTERM');result=await exited;assert.equal(result.code,143);await waitGone(grandchild);
console.log(JSON.stringify({timeoutExit:124,externalTermExit:143,stubbornGrandchildrenGone:true}));
