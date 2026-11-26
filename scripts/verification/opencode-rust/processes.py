"""Kernel process identity and resource measurements on macOS and Linux."""

import ctypes
import os
from pathlib import Path
import sys


if sys.platform == 'darwin':
    libc = ctypes.CDLL('/usr/lib/libproc.dylib', use_errno=True)
    timebase = (ctypes.c_uint32 * 2)()
    assert ctypes.CDLL(None).mach_timebase_info(timebase) == 0 and all(timebase)
    seconds_per_tick = timebase[0] / timebase[1] * 1e-9
    libc.proc_pidinfo.argtypes = (ctypes.c_int, ctypes.c_int, ctypes.c_uint64,
                                 ctypes.c_void_p, ctypes.c_int)
    libc.proc_pid_rusage.argtypes = (ctypes.c_int, ctypes.c_int, ctypes.c_void_p)

    def proc_info(pid, flavor, buffer):
        ctypes.set_errno(0)
        size = libc.proc_pidinfo(pid, flavor, 0, buffer, ctypes.sizeof(buffer))
        if size <= 0:
            code = ctypes.get_errno()
            if code == 3:
                raise ProcessLookupError(pid)
            raise OSError(code, f'proc_pidinfo({pid}, {flavor}) failed')
        return size

    def birth(pid):
        # struct proc_bsdinfo ends in aligned uint64_t start seconds and microseconds.
        data = (ctypes.c_uint64 * 17)()
        assert proc_info(pid, 3, data) == ctypes.sizeof(data), 'unexpected proc_bsdinfo size'
        return data[15] * 1000000 + data[16]

    def sample(pid):
        # struct proc_taskinfo starts with six uint64_t fields and twelve int32_t fields.
        data = (ctypes.c_uint64 * 12)()
        assert proc_info(pid, 4, data) == ctypes.sizeof(data), 'unexpected proc_taskinfo size'
        descriptors = (ctypes.c_uint64 * 16384)()
        fd_bytes = proc_info(pid, 1, descriptors)
        assert fd_bytes < ctypes.sizeof(descriptors) and fd_bytes % 8 == 0
        usage = (ctypes.c_uint64 * 20)()
        assert libc.proc_pid_rusage(pid, 2, usage) == 0, 'proc_pid_rusage failed'
        return {'read_bytes': usage[18], 'written_bytes': usage[19],
                'rss_bytes': data[1], 'cpu_seconds': (data[2] + data[3]) * seconds_per_tick,
                'fds': fd_bytes // 8, 'threads': data[10] >> 32}
else:
    def proc_stat(pid):
        try:
            return Path(f'/proc/{pid}/stat').read_text().rsplit(') ', 1)[1].split()
        except FileNotFoundError as exc:
            raise ProcessLookupError(pid) from exc

    def birth(pid):
        return int(proc_stat(pid)[19])

    def sample(pid):
        fields = proc_stat(pid)
        ticks = os.sysconf('SC_CLK_TCK')
        usage = dict(row.split(': ', 1) for row in Path(f'/proc/{pid}/io').read_text().splitlines())
        return {'read_bytes': int(usage['read_bytes']), 'written_bytes': int(usage['write_bytes']),
                'rss_bytes': int(fields[21]) * os.sysconf('SC_PAGE_SIZE'),
                'cpu_seconds': (int(fields[11]) + int(fields[12])) / ticks,
                'fds': len(os.listdir(f'/proc/{pid}/fd')), 'threads': int(fields[17])}


def alive(pid, started):
    try:
        return birth(pid) == started
    except ProcessLookupError:
        return False
