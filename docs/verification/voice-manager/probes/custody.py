import ctypes,os
class BSD(ctypes.Structure):
 _fields_=[(n,ctypes.c_uint32) for n in ['flags','status','xstatus','pid','ppid','uid','gid','ruid','rgid','svuid','svgid','rfu']]+[('comm',ctypes.c_char*16),('name',ctypes.c_char*32)]+[(n,ctypes.c_uint32) for n in ['nfiles','pgid','jobc','tdev','tpgid','nice']]+[('sec',ctypes.c_uint64),('usec',ctypes.c_uint64)]
def birth(pid):
 b=BSD();lib=ctypes.CDLL('/usr/lib/libproc.dylib');n=lib.proc_pidinfo(pid,3,0,ctypes.byref(b),ctypes.sizeof(b))
 return [b.sec,b.usec] if n==ctypes.sizeof(b) else None
