import ctypes,os,sys
from pathlib import Path
class BSD(ctypes.Structure):
 _fields_=[(n,ctypes.c_uint32) for n in ['flags','status','xstatus','pid','ppid','uid','gid','ruid','rgid','svuid','svgid','rfu']]+[('comm',ctypes.c_char*16),('name',ctypes.c_char*32)]+[(n,ctypes.c_uint32) for n in ['nfiles','pgid','jobc','tdev','tpgid','nice']]+[('sec',ctypes.c_uint64),('usec',ctypes.c_uint64)]
def birth(pid):
 if sys.platform.startswith('linux'):
  try:
   # Field 22 in /proc/PID/stat is the process start time in clock ticks.
   fields=Path(f'/proc/{pid}/stat').read_text().rsplit(')',1)[1].split()
   return int(fields[19])
  except (FileNotFoundError, ProcessLookupError):
   return None
 b=BSD();lib=ctypes.CDLL('/usr/lib/libproc.dylib');n=lib.proc_pidinfo(pid,3,0,ctypes.byref(b),ctypes.sizeof(b))
 return [b.sec,b.usec] if n==ctypes.sizeof(b) else None
